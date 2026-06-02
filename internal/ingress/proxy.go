package ingress

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/internal/tlsutil"
	"github.com/eghansah/orchestrator/pkg/types"
)

// Proxy is an HTTP handler that routes inbound requests to containers using
// IngressRule records stored in Raft. Containers are bound to 127.0.0.1 (loopback)
// only. For local containers the proxy dials directly; for remote containers it
// tunnels the request through the mTLS gRPC ForwardHTTP RPC so that no container
// port is reachable from outside the cluster.
type Proxy struct {
	peer        *internraft.Peer
	localNodeID string
	ownCert     tls.Certificate
}

func New(peer *internraft.Peer, localNodeID string, ownCert tls.Certificate) *Proxy {
	return &Proxy{peer: peer, localNodeID: localNodeID, ownCert: ownCert}
}

// TLSConfig returns a *tls.Config that performs SNI-based certificate selection
// using Domain records stored in Raft state. Falls back to the node's own cert
// when no domain matches the SNI name.
func (p *Proxy) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hello.ServerName != "" {
				for _, d := range p.peer.State().Domains {
					if d.Enabled && d.Name == hello.ServerName && d.TLSCert != "" && d.TLSKey != "" {
						cert, err := tls.X509KeyPair([]byte(d.TLSCert), []byte(d.TLSKey))
						if err == nil {
							return &cert, nil
						}
					}
				}
			}
			return &p.ownCert, nil
		},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	state := p.peer.State()

	host := r.Host
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}

	rule, ok := p.matchRule(state, host, r.URL.Path)
	if !ok {
		http.Error(w, "no matching ingress rule", http.StatusBadGateway)
		return
	}

	var svc types.Service
	var svcFound bool
	for _, s := range state.Services {
		if s.Name == rule.ServiceName {
			svc = s
			svcFound = true
			break
		}
	}
	if !svcFound {
		http.Error(w, "service not found", http.StatusBadGateway)
		return
	}

	var wl types.Workload
	for _, w := range state.Workloads {
		if w.Name() == svc.WorkloadName {
			wl = w
			break
		}
	}
	if wl.NodeID == "" {
		http.Error(w, "workload not scheduled", http.StatusBadGateway)
		return
	}

	allocatedPort := allocatedPortFor(wl, svc.TargetPort)
	if allocatedPort == 0 {
		http.Error(w, "no allocated port for service target port", http.StatusBadGateway)
		return
	}

	if wl.NodeID == p.localNodeID {
		// Container is on this node — dial loopback directly.
		target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", allocatedPort))
		if err != nil {
			http.Error(w, "invalid backend address", http.StatusInternalServerError)
			return
		}
		httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
		return
	}

	// Container is on a remote node — tunnel via mTLS gRPC.
	node, ok := state.Nodes[wl.NodeID]
	if !ok {
		http.Error(w, "target node not found", http.StatusBadGateway)
		return
	}
	p.forwardRemote(w, r, node, allocatedPort)
}

// forwardRemote tunnels the HTTP request to the remote node's ForwardHTTP gRPC RPC.
func (p *Proxy) forwardRemote(w http.ResponseWriter, r *http.Request, node types.Node, allocatedPort uint32) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read request body: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Build the path + query string.
	path := r.URL.RequestURI()

	// Collect headers (skip hop-by-hop).
	var headers []*gen.HttpHeader
	for name, vals := range r.Header {
		if isHopByHop(name) {
			continue
		}
		for _, v := range vals {
			headers = append(headers, &gen.HttpHeader{Name: name, Value: v})
		}
	}

	tlsCfg := tlsutil.ClientTLSConfig(p.ownCert, node.TLSCert)
	conn, err := grpc.NewClient(node.Address, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		http.Error(w, "dial remote node: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Close()

	client := gen.NewNodeServiceClient(conn)
	resp, err := client.ForwardHTTP(r.Context(), &gen.ForwardHTTPRequest{
		AllocatedPort: allocatedPort,
		Method:        r.Method,
		Path:          path,
		Headers:       headers,
		Body:          body,
	})
	if err != nil {
		http.Error(w, "remote forward: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Copy response headers.
	for _, h := range resp.Headers {
		if isHopByHop(h.Name) {
			continue
		}
		w.Header().Add(h.Name, h.Value)
	}
	w.WriteHeader(int(resp.StatusCode))
	_, _ = w.Write(resp.Body)
}

// allocatedPortFor returns the auto-assigned host port bound to the given
// container port, or 0 if not found.
func allocatedPortFor(wl types.Workload, containerPort uint32) uint32 {
	for _, pa := range wl.PortAllocations {
		if pa.ContainerPort == containerPort {
			return pa.AllocatedPort
		}
	}
	return 0
}

// matchRule finds the rule with the longest matching PathPrefix for the given host and path.
func (p *Proxy) matchRule(state internraft.ClusterState, host, path string) (types.IngressRule, bool) {
	var best types.IngressRule
	bestLen := -1

	for _, rule := range state.IngressRules {
		// Derive host from the linked domain when available.
		ruleHost := rule.Host
		if rule.DomainID != "" {
			if d, ok := state.Domains[rule.DomainID]; ok {
				if !d.Enabled {
					continue // domain is disabled; skip this rule
				}
				ruleHost = d.Name
			}
		}
		if ruleHost != "" && ruleHost != host {
			continue
		}
		prefix := rule.PathPrefix
		if prefix == "" {
			prefix = "/"
		}
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		if len(prefix) > bestLen {
			best = rule
			bestLen = len(prefix)
		}
	}

	return best, bestLen >= 0
}

func splitHostPort(hostport string) (host, port string, err error) {
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return hostport, "", fmt.Errorf("no port")
	}
	return hostport[:i], hostport[i+1:], nil
}

// isHopByHop reports whether the header should not be forwarded.
func isHopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

