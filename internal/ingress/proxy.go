package ingress

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/pkg/types"
)

// Proxy is an HTTP handler that routes requests to containers based on
// IngressRule records in the Raft state. All nodes run a Proxy instance;
// cross-node traffic goes to node.DataIP:rule.Port over LAN.
type Proxy struct {
	peer *internraft.Peer
}

func New(peer *internraft.Peer) *Proxy {
	return &Proxy{peer: peer}
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

	wl, ok := state.Workloads[rule.WorkloadID]
	if !ok || wl.NodeID == "" {
		http.Error(w, "workload not scheduled", http.StatusBadGateway)
		return
	}

	node, ok := state.Nodes[wl.NodeID]
	if !ok || node.DataIP == "" {
		http.Error(w, "node data IP unavailable", http.StatusBadGateway)
		return
	}

	target, err := url.Parse(fmt.Sprintf("http://%s:%d", node.DataIP, rule.Port))
	if err != nil {
		http.Error(w, "invalid backend address", http.StatusInternalServerError)
		return
	}

	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ServeHTTP(w, r)
}

// matchRule finds the rule with the longest matching PathPrefix for the given host and path.
func (p *Proxy) matchRule(state internraft.ClusterState, host, path string) (types.IngressRule, bool) {
	var best types.IngressRule
	bestLen := -1

	for _, rule := range state.IngressRules {
		if rule.Host != "" && rule.Host != host {
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
