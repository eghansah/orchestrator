package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"io"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/eghansah/orchestrator/internal/agent"
	"github.com/eghansah/orchestrator/internal/control"
	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
	"github.com/eghansah/orchestrator/internal/ingress"
	"github.com/eghansah/orchestrator/internal/ingresscfg"
	"github.com/eghansah/orchestrator/internal/localregistry"
	"github.com/eghansah/orchestrator/internal/nerdctl"
	"github.com/eghansah/orchestrator/internal/proxycfg"
	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/internal/tlsutil"
	"github.com/eghansah/orchestrator/internal/webui"
	"github.com/eghansah/orchestrator/pkg/crypto"
	"github.com/eghansah/orchestrator/pkg/types"
)

// ── Config ────────────────────────────────────────────────────────────────────

type config struct {
	nodeID      string
	grpcAddr    string
	raftAddr    string
	webAddr     string
	webPrefix   string // URL prefix for the web console, e.g. "/console" (empty = root)
	ingressAddr    string // HTTP ingress proxy listen address; empty = disabled
	ingressTLSAddr string // HTTPS ingress proxy listen address; empty = disabled
	dnsAddr     string // DNS listen address; empty = disabled
	dataAddr    string // routable IP for container traffic; auto-detected if empty
	dataDir     string
	bootstrap   bool
	joinAddr    string // gRPC address of an existing node to join through
	joinToken   string // shared secret required to join the cluster
	adminToken  string // bearer token required for all ControlService RPCs
	webPassword    string // password for the web console login form
	webDisableMFA  bool   // skip TOTP MFA for all logins when true
	// LDAP/AD auth
	ldapAddr           string
	ldapTLS            bool
	ldapInsecure       bool
	ldapBindDNTemplate string
	ldapBaseDN         string
	ldapUserFilter     string
	ldapGroupDN        string

	nerdctlBin     string
	namespace      string
	containerdAddr string // containerd socket path; auto-detected from XDG_RUNTIME_DIR if empty

	registryAddr  string // local container registry listen address; empty = disabled
	ingressdHTTP  string // host:port binding for ingressd HTTP  (e.g. 127.0.0.1:8080)
	ingressdHTTPS string // host:port binding for ingressd HTTPS (e.g. 127.0.0.1:8443)
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.nodeID, "node-id", "", "unique node identifier (default: hostname)")
	flag.StringVar(&cfg.grpcAddr, "grpc-addr", "127.0.0.1:7946", "gRPC listen address (host:port)")
	flag.StringVar(&cfg.raftAddr, "raft-addr", "127.0.0.1:7947", "Raft TCP listen address (host:port)")
	flag.StringVar(&cfg.dataDir, "data-dir", "", "persistent data directory (default: ~/.local/share/orchestrator)")
	flag.BoolVar(&cfg.bootstrap, "bootstrap", false, "bootstrap a new single-node cluster")
	flag.StringVar(&cfg.joinAddr, "join", "", "gRPC address of an existing cluster node to join")
	flag.StringVar(&cfg.joinToken, "join-token", "", "shared secret required to join the cluster")
	flag.StringVar(&cfg.adminToken, "admin-token", "", "bearer token for ControlService RPCs (auto-generated if bootstrapping)")
	flag.StringVar(&cfg.webPassword, "web-password", "", "password for web console login (auto-generated if bootstrapping)")
	flag.BoolVar(&cfg.webDisableMFA, "web-disable-mfa", false, "disable TOTP MFA for web console logins")
	flag.StringVar(&cfg.webAddr, "web-addr", ":7948", "web console HTTP listen address (empty to disable)")
	flag.StringVar(&cfg.webPrefix, "web-prefix", "", "URL prefix for the web console, e.g. /console (empty = serve at root)")
	flag.StringVar(&cfg.ingressAddr, "ingress-addr", ":8080", "HTTP ingress proxy listen address (empty to disable)")
	flag.StringVar(&cfg.ingressTLSAddr, "ingress-tls-addr", "", "HTTPS ingress proxy listen address, e.g. :8443 (empty to disable; uses domain TLS certs)")
	flag.StringVar(&cfg.dnsAddr, "dns-addr", "", "DNS listen address for svc.local zone, e.g. :5353 (empty to disable; any port ≥1024 works without root)")
	flag.StringVar(&cfg.dataAddr, "data-addr", "", "routable IP for container traffic (auto-detected if empty)")
	flag.StringVar(&cfg.nerdctlBin, "nerdctl", "nerdctl", "path to nerdctl binary")
	flag.StringVar(&cfg.namespace, "namespace", "orchestrator", "nerdctl namespace for managed containers")
	flag.StringVar(&cfg.containerdAddr, "containerd-addr", "", "containerd socket path (auto-detected from XDG_RUNTIME_DIR if empty)")
	flag.StringVar(&cfg.registryAddr, "registry-addr", "127.0.0.1:5000", "built-in local container registry listen address (empty to disable)")
	flag.StringVar(&cfg.ingressdHTTP, "ingressd-http", "127.0.0.1:8080", "host:port for ingressd HTTP bind (empty to disable ingressd auto-deploy)")
	flag.StringVar(&cfg.ingressdHTTPS, "ingressd-https", "127.0.0.1:8443", "host:port for ingressd HTTPS bind")
	flag.StringVar(&cfg.ldapAddr, "ldap-addr", "", "LDAP/AD server address host:port (empty = use local password auth)")
	flag.BoolVar(&cfg.ldapTLS, "ldap-tls", false, "use implicit TLS when connecting to LDAP (LDAPS, typically port 636)")
	flag.BoolVar(&cfg.ldapInsecure, "ldap-insecure", false, "skip TLS certificate verification for LDAP (for self-signed certs)")
	flag.StringVar(&cfg.ldapBindDNTemplate, "ldap-bind-dn-template", "", "how to form the bind DN; %%s is replaced with the username (e.g. %%s@corp.com or uid=%%s,ou=users,dc=corp,dc=com)")
	flag.StringVar(&cfg.ldapBaseDN, "ldap-base-dn", "", "LDAP search base DN for group membership check, e.g. DC=corp,DC=com")
	flag.StringVar(&cfg.ldapUserFilter, "ldap-user-filter", "", "LDAP search filter with %%s for username; required when --ldap-group-dn is set (e.g. (sAMAccountName=%%s))")
	flag.StringVar(&cfg.ldapGroupDN, "ldap-group-dn", "", "optional: restrict login to members of this group DN")
	flag.Parse()

	if cfg.nodeID == "" {
		hostname, err := os.Hostname()
		dieOnErr(err, "get hostname")
		cfg.nodeID = hostname
	}
	if cfg.dataDir == "" {
		home, err := os.UserHomeDir()
		dieOnErr(err, "get home dir")
		cfg.dataDir = filepath.Join(home, ".local", "share", "orchestrator")
	}
	return cfg
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	cfg := parseFlags()
	cfg.joinToken = resolveJoinToken(cfg.dataDir, cfg.joinToken, cfg.bootstrap)
	cfg.adminToken = resolveAdminToken(cfg.dataDir, cfg.adminToken, cfg.bootstrap)
	cfg.webPassword = resolveWebPassword(cfg.dataDir, cfg.webPassword, cfg.bootstrap)
	if cfg.dataAddr == "" {
		cfg.dataAddr = detectDataIP()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 1. TLS identity ---------------------------------------------------------
	tlsCert, err := tlsutil.LoadOrCreate(cfg.dataDir, cfg.nodeID)
	dieOnErr(err, "load/create TLS cert")
	ownCertDER := tlsutil.CertDER(tlsCert)
	slog.Info("TLS identity ready", "node-id", cfg.nodeID)

	// 2. nerdctl client -------------------------------------------------------
	// Inject DNS IP + port only when DNS server is enabled so containers resolve svc.local.
	var nerdctlDNSIP string
	var nerdctlDNSPort uint32
	if cfg.dnsAddr != "" {
		nerdctlDNSIP = cfg.dataAddr
		nerdctlDNSPort = parseDNSPort(cfg.dnsAddr)
	}
	nc, err := nerdctl.NewClient(cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, cfg.dataDir, nerdctlDNSIP, nerdctlDNSPort)
	dieOnErr(err, "create nerdctl client")
	slog.Info("containerd socket", "address", nc.Address())
	if err := nc.Probe(ctx); err != nil {
		slog.Error("nerdctl probe failed — is nerdctl installed in rootless mode?", "err", err)
		os.Exit(1)
	}
	slog.Info("nerdctl ready", "namespace", cfg.namespace)

	// 2b. Built-in local registry (optional) ----------------------------------
	if cfg.registryAddr != "" {
		reg, err := localregistry.New(localregistry.IngressdTar)
		if err != nil {
			slog.Warn("local registry: failed to load image tarball", "err", err)
			reg, _ = localregistry.New(nil) // start empty
		}
		regSrv := &http.Server{Addr: cfg.registryAddr, Handler: reg.Handler()}
		go func() {
			slog.Info("local registry listening", "addr", cfg.registryAddr, "images", reg.ImageCount())
			if err := regSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("local registry error", "err", err)
			}
		}()
		defer regSrv.Shutdown(context.Background()) //nolint:errcheck

		// Auto-deploy the ingressd container after a short settle delay so the
		// registry is accepting connections before nerdctl tries to pull.
		if cfg.ingressdHTTP != "" {
			go func() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				reconcileIngressd(ctx, cfg)
			}()
		}
	}

	// 3. Raft peer ------------------------------------------------------------
	peer, err := internraft.NewPeer(internraft.Config{
		NodeID:    cfg.nodeID,
		BindAddr:  cfg.raftAddr,
		DataDir:   filepath.Join(cfg.dataDir, "raft"),
		Bootstrap: cfg.bootstrap,
	})
	dieOnErr(err, "create raft peer")
	slog.Info("raft peer started", "node-id", cfg.nodeID, "raft-addr", cfg.raftAddr)
	if cfg.joinToken == "" {
		slog.Warn("no join token configured; any node can join this cluster")
	}

	// 4. Agent ----------------------------------------------------------------
	stateCh := make(chan types.ActualWorkloadState, 4)
	ag := agent.New(nc, agent.Config{
		NodeID:       cfg.nodeID,
		StateUpdates: stateCh,
	})

	// 5. gRPC server ----------------------------------------------------------
	// isPinned checks cluster state for a matching TLS cert DER.
	isPinned := func(certDER []byte) bool {
		for _, n := range peer.State().Nodes {
			if bytes.Equal(n.TLSCert, certDER) {
				return true
			}
		}
		return false
	}

	ns := &nodeServer{
		Agent:      ag,
		peer:       peer,
		grpcAddr:   cfg.grpcAddr,
		joinToken:  cfg.joinToken,
		ownCertDER: ownCertDER,
	}
	secretsKey := crypto.DeriveKey(cfg.joinToken)
	ctrl := control.New(peer, cfg.nodeID, cfg.grpcAddr, tlsCert, secretsKey)

	// 5b. Web console (optional) ----------------------------------------------
	if cfg.webAddr != "" {
		webSrv := webui.New(peer, ctrl, ag, cfg.adminToken, cfg.webPassword, cfg.webPrefix, cfg.webDisableMFA, webui.LDAPConfig{
			Addr:           cfg.ldapAddr,
			UseTLS:         cfg.ldapTLS,
			Insecure:       cfg.ldapInsecure,
			BindDNTemplate: cfg.ldapBindDNTemplate,
			BaseDN:         cfg.ldapBaseDN,
			UserFilter:     cfg.ldapUserFilter,
			GroupDN:        cfg.ldapGroupDN,
		}, secretsKey)
		httpSrv := &http.Server{Addr: cfg.webAddr, Handler: webSrv.Handler()}
		go func() {
			slog.Info("web console listening", "addr", cfg.webAddr)
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("web console error", "err", err)
			}
		}()
		defer httpSrv.Shutdown(context.Background()) //nolint:errcheck
	}

	// 5c. Ingress proxy (optional) --------------------------------------------
	var proxy *ingress.Proxy
	if cfg.ingressAddr != "" || cfg.ingressTLSAddr != "" {
		proxy = ingress.New(peer, cfg.nodeID, tlsCert)
	}
	if cfg.ingressAddr != "" {
		ingressSrv := &http.Server{Addr: cfg.ingressAddr, Handler: proxy}
		go func() {
			slog.Info("ingress proxy listening", "addr", cfg.ingressAddr)
			if err := ingressSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("ingress proxy error", "err", err)
			}
		}()
		defer ingressSrv.Shutdown(context.Background()) //nolint:errcheck
	}
	if cfg.ingressTLSAddr != "" {
		ingressTLSSrv := &http.Server{Addr: cfg.ingressTLSAddr, Handler: proxy, TLSConfig: proxy.TLSConfig()}
		go func() {
			slog.Info("ingress TLS proxy listening", "addr", cfg.ingressTLSAddr)
			if err := ingressTLSSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				slog.Error("ingress TLS proxy error", "err", err)
			}
		}()
		defer ingressTLSSrv.Shutdown(context.Background()) //nolint:errcheck
	}

	// 5d. Proxy config writer -------------------------------------------------
	// Write the initial config immediately, then re-write whenever Raft state changes.
	// proxyd watches this file and reloads; the orchestrator is not in the data path.
	_ = proxycfg.Write(cfg.dataDir, cfg.nodeID, cfg.dataAddr, cfg.dnsAddr, peer.State())
	go proxycfgWriteLoop(ctx, peer, cfg.dataDir, cfg.nodeID, cfg.dataAddr, cfg.dnsAddr)

	// 5e. Ingress config writer ------------------------------------------------
	// Write <dataDir>/ingress/config.json; ingressd watches this file and drives HAProxy.
	_ = ingresscfg.Write(cfg.dataDir, peer.State())
	go ingresscfgWriteLoop(ctx, peer, cfg.dataDir)

	serverTLS := tlsutil.ServerTLSConfig(tlsCert, isPinned)
	grpcSrv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.ChainUnaryInterceptor(
			tlsutil.NodeAuthInterceptor(isPinned),
			tlsutil.ControlAuthInterceptor(cfg.adminToken),
		),
	)
	gen.RegisterNodeServiceServer(grpcSrv, ns)
	gen.RegisterControlServiceServer(grpcSrv, ctrl)

	lis, err := net.Listen("tcp", cfg.grpcAddr)
	dieOnErr(err, "listen "+cfg.grpcAddr)
	slog.Info("gRPC server listening", "addr", cfg.grpcAddr)

	// 6. Goroutines -----------------------------------------------------------

	go func() {
		if err := grpcSrv.Serve(lis); err != nil {
			slog.Error("gRPC serve error", "err", err)
		}
	}()

	go func() {
		if err := ag.Run(ctx); err != nil && err != context.Canceled {
			slog.Error("agent run error", "err", err)
		}
	}()

	go control.NewReconciler(ctrl, ag).Run(ctx)

	// Once we are leader, register this node in the cluster state.
	go selfRegisterLoop(ctx, peer, cfg.nodeID, cfg.grpcAddr, cfg.dataAddr, ownCertDER)

	// Periodically forward actual state to the leader's ReportState RPC.
	go stateReportLoop(ctx, peer, tlsCert, cfg.nodeID, cfg.grpcAddr, stateCh)

	// If joining an existing cluster, heartbeat the given node so the leader
	// can add us as a Raft voter and register us in the cluster state.
	if cfg.joinAddr != "" {
		go heartbeatLoop(ctx, peer, tlsCert, ownCertDER, cfg.nodeID, cfg.grpcAddr, cfg.raftAddr, cfg.dataAddr, cfg.joinAddr, cfg.joinToken)
	}

	slog.Info("orchestrator running", "node-id", cfg.nodeID)
	<-ctx.Done()
	slog.Info("shutting down")

	// Hard-kill after 15 s so Ctrl+C always works even if a subsystem stalls.
	go func() {
		time.Sleep(15 * time.Second)
		slog.Warn("shutdown timed out; forcing exit")
		os.Exit(1)
	}()

	if err := peer.Shutdown(); err != nil {
		slog.Warn("raft shutdown error", "err", err)
	}
	grpcSrv.GracefulStop()
}

// ── nodeServer ────────────────────────────────────────────────────────────────

// nodeServer wraps agent.Agent and overrides Heartbeat with Raft membership logic.
type nodeServer struct {
	*agent.Agent
	peer       *internraft.Peer
	grpcAddr   string
	joinToken  string // empty = warn-only; non-empty = enforced
	ownCertDER []byte // returned in HeartbeatResponse so joining nodes can pin us
}

func (ns *nodeServer) Heartbeat(_ context.Context, req *gen.HeartbeatRequest) (*gen.HeartbeatResponse, error) {
	if req.NodeId == "" || req.Address == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and address are required")
	}

	// Enforce join token when configured. Constant-time compare prevents timing attacks.
	if ns.joinToken != "" {
		if subtle.ConstantTimeCompare([]byte(req.JoinToken), []byte(ns.joinToken)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "invalid or missing join token")
		}
	}

	// When we are the leader, register unknown callers in the cluster state
	// and admit them as Raft voters.
	if ns.peer.IsLeader() {
		state := ns.peer.State()
		if existing, known := state.Nodes[req.NodeId]; known {
			if existing.Address != req.Address {
				slog.Warn("heartbeat from known node with mismatched address — ignoring",
					"node", req.NodeId, "registered", existing.Address, "received", req.Address)
			}
		} else {
			n := types.Node{
				ID:         req.NodeId,
				Address:    req.Address,
				Status:     types.NodeHealthy,
				TLSCert:    req.TlsCert,
				DataIP:     req.DataIp,
				LastSeenAt: time.Now(),
			}
			if req.Resources != nil {
				n.Resources = types.NodeResources{
					CPUCores:    req.Resources.CpuCores,
					MemoryBytes: req.Resources.MemoryBytes,
					DiskBytes:   req.Resources.DiskBytes,
				}
			}
			if err := ns.peer.RegisterNode(n); err != nil {
				slog.Warn("register node failed", "node", req.NodeId, "err", err)
			}
			if req.RaftAddress != "" {
				if err := ns.peer.AddVoter(req.NodeId, req.RaftAddress); err != nil {
					slog.Warn("add voter failed", "node", req.NodeId, "raft-addr", req.RaftAddress, "err", err)
				}
			}
			slog.Info("admitted new node", "node-id", req.NodeId, "grpc", req.Address, "raft", req.RaftAddress)
		}
	}

	// Resolve the leader's gRPC address from cluster state (not the Raft transport addr).
	leaderID := ns.peer.LeaderID()
	leaderGRPCAddr := ""
	if s := ns.peer.State(); leaderID != "" {
		if n, ok := s.Nodes[leaderID]; ok {
			leaderGRPCAddr = n.Address
		}
	}

	return &gen.HeartbeatResponse{
		LeaderId:      leaderID,
		LeaderAddress: leaderGRPCAddr,
		TlsCert:       ns.ownCertDER, // joining node pins this for future mTLS connections
	}, nil
}

// ForwardHTTP tunnels an HTTP request to a container bound on 127.0.0.1 on this node.
// Called by the ingress proxy on a remote node when the target container is local here.
func (ns *nodeServer) ForwardHTTP(ctx context.Context, req *gen.ForwardHTTPRequest) (*gen.ForwardHTTPResponse, error) {
	if req.AllocatedPort == 0 {
		return nil, status.Error(codes.InvalidArgument, "allocated_port is required")
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", req.AllocatedPort, req.Path)
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, bytes.NewReader(req.Body))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build request: %v", err)
	}
	for _, h := range req.Headers {
		httpReq.Header.Add(h.Name, h.Value)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "forward: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read body: %v", err)
	}
	out := &gen.ForwardHTTPResponse{StatusCode: int32(resp.StatusCode), Body: body}
	for name, vals := range resp.Header {
		for _, v := range vals {
			out.Headers = append(out.Headers, &gen.HttpHeader{Name: name, Value: v})
		}
	}
	return out, nil
}

// ForwardTCP tunnels raw TCP bytes to a container bound on 127.0.0.1 on this node.
// The first stream message must supply the allocated_port; subsequent messages carry data.
func (ns *nodeServer) ForwardTCP(stream gen.NodeService_ForwardTCPServer) error {
	// First message: get the port to dial.
	first, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "receive port: %v", err)
	}
	portMsg, ok := first.Payload.(*gen.ForwardTCPRequest_AllocatedPort)
	if !ok || portMsg.AllocatedPort == 0 {
		return status.Error(codes.InvalidArgument, "first message must set allocated_port")
	}

	backend, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", portMsg.AllocatedPort))
	if err != nil {
		return status.Errorf(codes.Unavailable, "dial backend: %v", err)
	}
	defer backend.Close()

	ctx := stream.Context()
	errCh := make(chan error, 2)

	// stream → backend
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				backend.(*net.TCPConn).CloseWrite() //nolint:errcheck
				errCh <- err
				return
			}
			if d, ok := msg.Payload.(*gen.ForwardTCPRequest_Data); ok {
				if _, err := backend.Write(d.Data); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	// backend → stream
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := backend.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				if sendErr := stream.Send(&gen.ForwardTCPChunk{Data: chunk}); sendErr != nil {
					errCh <- sendErr
					return
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err == io.EOF {
			return nil
		}
		return err
	}
}

// ── Background goroutines ─────────────────────────────────────────────────────

// selfRegisterLoop polls until this node becomes leader, then writes its own
// Node record into the Raft state so the cluster knows its gRPC address and cert.
func selfRegisterLoop(ctx context.Context, peer *internraft.Peer, nodeID, grpcAddr, dataIP string, ownCertDER []byte) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !peer.IsLeader() {
				continue
			}
			if _, ok := peer.State().Nodes[nodeID]; ok {
				return // already registered
			}
			n := types.Node{
				ID:      nodeID,
				Address: grpcAddr,
				Status:  types.NodeHealthy,
				TLSCert: ownCertDER,
				DataIP:  dataIP,
				Resources: types.NodeResources{
					CPUCores: uint32(runtime.NumCPU()),
				},
				LastSeenAt: time.Now(),
			}
			if err := peer.RegisterNode(n); err != nil {
				slog.Warn("self-register failed", "err", err)
				continue
			}
			slog.Info("registered self in cluster state", "node-id", nodeID)
			return
		}
	}
}

// heartbeatLoop sends periodic gRPC Heartbeats to the leader so the leader can
// add this node as a Raft voter and track it in cluster state.
//
// The first heartbeat uses TOFU (no pinned server cert). The HeartbeatResponse
// carries the leader's cert, which is pinned for all subsequent heartbeats.
func heartbeatLoop(
	ctx context.Context,
	peer *internraft.Peer,
	ownCert tls.Certificate,
	ownCertDER []byte,
	nodeID, grpcAddr, raftAddr, dataIP, seedAddr, token string,
) {
	knownLeaderAddr := seedAddr
	var knownLeaderCert []byte // nil = TOFU until first successful heartbeat

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Send an immediate first heartbeat without waiting for the ticker.
	addr, cert := doHeartbeat(ctx, knownLeaderAddr, knownLeaderCert, ownCert, ownCertDER, nodeID, grpcAddr, raftAddr, dataIP, token)
	if addr != "" {
		knownLeaderAddr = addr
	}
	if cert != nil {
		knownLeaderCert = cert
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if peer.IsLeader() {
				return // we became leader; no need to heartbeat outward
			}
			addr, cert := doHeartbeat(ctx, knownLeaderAddr, knownLeaderCert, ownCert, ownCertDER, nodeID, grpcAddr, raftAddr, dataIP, token)
			if addr != "" {
				knownLeaderAddr = addr
				knownLeaderCert = nil // reset pin when following a redirect to a new leader
			}
			if cert != nil {
				knownLeaderCert = cert
			}
		}
	}
}

// doHeartbeat dials targetAddr and sends one Heartbeat RPC.
// targetCertDER is the pinned server cert; nil means TOFU (skip verification).
// Returns (newLeaderAddr, leaderCertDER) — both may be empty/nil on failure.
func doHeartbeat(
	ctx context.Context,
	targetAddr string,
	targetCertDER []byte,
	ownCert tls.Certificate,
	ownCertDER []byte,
	nodeID, grpcAddr, raftAddr, dataIP, token string,
) (newLeaderAddr string, leaderCert []byte) {
	tlsCfg := tlsutil.ClientTLSConfig(ownCert, targetCertDER)
	conn, err := grpc.NewClient(targetAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		slog.Warn("heartbeat dial failed", "target", targetAddr, "err", err)
		return "", nil
	}
	defer conn.Close()

	resp, err := gen.NewNodeServiceClient(conn).Heartbeat(ctx, &gen.HeartbeatRequest{
		NodeId:      nodeID,
		Address:     grpcAddr,
		RaftAddress: raftAddr,
		JoinToken:   token,
		TlsCert:     ownCertDER,
		DataIp:      dataIP,
	})
	if err != nil {
		slog.Warn("heartbeat RPC failed", "target", targetAddr, "err", err)
		return "", nil
	}

	cert := resp.TlsCert
	if resp.LeaderAddress != "" && resp.LeaderAddress != targetAddr {
		slog.Info("leader redirect", "from", targetAddr, "to", resp.LeaderAddress)
		return resp.LeaderAddress, cert
	}
	return "", cert
}

// stateReportLoop drains the agent's StateUpdates channel and forwards each
// snapshot to the current leader via ReportState. Uses mTLS with the leader's
// pinned cert from cluster state.
func stateReportLoop(
	ctx context.Context,
	peer *internraft.Peer,
	ownCert tls.Certificate,
	nodeID, selfGRPCAddr string,
	stateCh <-chan types.ActualWorkloadState,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case snapshot := <-stateCh:
			if peer.IsLeader() {
				// Leader already holds local state; nothing to forward.
				continue
			}

			leaderID := peer.LeaderID()
			if leaderID == "" {
				continue
			}
			state := peer.State()
			leaderNode, ok := state.Nodes[leaderID]
			if !ok || leaderNode.Address == "" || leaderNode.Address == selfGRPCAddr {
				continue
			}

			tlsCfg := tlsutil.ClientTLSConfig(ownCert, leaderNode.TLSCert)
			conn, err := grpc.NewClient(leaderNode.Address, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
			if err != nil {
				slog.Warn("state report dial failed", "leader", leaderNode.Address, "err", err)
				continue
			}
			_, err = gen.NewNodeServiceClient(conn).ReportState(ctx, &gen.ReportStateRequest{
				State: types.ActualWorkloadStateToProto(snapshot),
			})
			conn.Close()
			if err != nil {
				slog.Warn("ReportState failed", "node", nodeID, "leader", leaderNode.Address, "err", err)
			}
		}
	}
}

// ── Utilities ─────────────────────────────────────────────────────────────────

// resolveJoinToken returns the join token to use, in priority order:
//  1. Explicit --join-token flag value (saved to disk when bootstrapping).
//  2. Token previously saved to <data-dir>/join-token.
//  3. Auto-generated random token (bootstrap only — saved and printed).
//
// Returns "" on non-bootstrap nodes with no token configured; the caller
// emits the "cluster is open" warning in that case.
func resolveJoinToken(dataDir, flagToken string, bootstrap bool) string {
	tokenFile := filepath.Join(dataDir, "join-token")

	if flagToken != "" {
		if bootstrap {
			if err := os.MkdirAll(dataDir, 0o700); err == nil {
				_ = os.WriteFile(tokenFile, []byte(flagToken), 0o600)
			}
		}
		return flagToken
	}

	// Load from a previous run.
	if raw, err := os.ReadFile(tokenFile); err == nil {
		if t := strings.TrimSpace(string(raw)); t != "" {
			slog.Info("loaded join token from file", "path", tokenFile)
			return t
		}
	}

	if !bootstrap {
		return "" // non-bootstrap with no token — warning emitted by caller
	}

	// Bootstrap with no token: generate, persist, and print.
	token := generateToken()
	if err := os.MkdirAll(dataDir, 0o700); err == nil {
		if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
			slog.Warn("could not save join token to file", "path", tokenFile, "err", err)
		}
	}
	fmt.Printf("\n  *** Join token: %s ***\n", token)
	fmt.Printf("  Joining nodes require: --join-token %s\n\n", token)
	return token
}

// resolveAdminToken follows the same precedence as resolveJoinToken:
// explicit flag → saved file → auto-generate (bootstrap only).
func resolveAdminToken(dataDir, flagToken string, bootstrap bool) string {
	tokenFile := filepath.Join(dataDir, "admin-token")

	if flagToken != "" {
		if bootstrap {
			if err := os.MkdirAll(dataDir, 0o700); err == nil {
				_ = os.WriteFile(tokenFile, []byte(flagToken), 0o600)
			}
		}
		return flagToken
	}

	if raw, err := os.ReadFile(tokenFile); err == nil {
		if t := strings.TrimSpace(string(raw)); t != "" {
			slog.Info("loaded admin token from file", "path", tokenFile)
			return t
		}
	}

	if !bootstrap {
		slog.Warn("no admin token configured; ControlService RPCs are unauthenticated")
		return ""
	}

	token := generateToken()
	if err := os.MkdirAll(dataDir, 0o700); err == nil {
		if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
			slog.Warn("could not save admin token to file", "path", tokenFile, "err", err)
		}
	}
	fmt.Printf("\n  *** Admin token: %s ***\n", token)
	fmt.Printf("  Store this in ~/.config/orchestrator/token or use: --token %s\n\n", token)
	return token
}

// resolveWebPassword follows the same precedence as resolveAdminToken.
// The web password is separate from the admin token — it authenticates the
// browser login form and is never shown again after bootstrap.
func resolveWebPassword(dataDir, flagVal string, bootstrap bool) string {
	pwFile := filepath.Join(dataDir, "web-password")

	if flagVal != "" {
		if bootstrap {
			if err := os.MkdirAll(dataDir, 0o700); err == nil {
				_ = os.WriteFile(pwFile, []byte(flagVal), 0o600)
			}
		}
		return flagVal
	}

	if raw, err := os.ReadFile(pwFile); err == nil {
		if p := strings.TrimSpace(string(raw)); p != "" {
			slog.Info("loaded web console password from file", "path", pwFile)
			return p
		}
	}

	if !bootstrap {
		slog.Warn("no web console password configured; web console login is disabled")
		return ""
	}

	pw := generateToken()
	if err := os.MkdirAll(dataDir, 0o700); err == nil {
		if err := os.WriteFile(pwFile, []byte(pw), 0o600); err != nil {
			slog.Warn("could not save web console password to file", "path", pwFile, "err", err)
		}
	}
	fmt.Printf("\n  *** Web console password: %s ***\n", pw)
	fmt.Printf("  Login at the web console with username: admin\n\n")
	return pw
}

func generateToken() string {
	b := make([]byte, 16) // 128-bit, 32 hex chars
	if _, err := crand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func detectDataIP() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ip, _, err := net.ParseCIDR(addr.String()); err == nil && ip.To4() != nil {
				return ip.String()
			}
		}
	}
	return "127.0.0.1"
}

func dieOnErr(err error, msg string) {
	if err != nil {
		slog.Error(msg, "err", err)
		os.Exit(1)
	}
}

// proxycfgWriteLoop polls Raft state every 2 seconds and rewrites the proxyd config file
// whenever it changes. proxyd watches the file and reloads; the orchestrator is not in the
// data path for DNS or TCP forwarding.
func proxycfgWriteLoop(ctx context.Context, peer *internraft.Peer, dataDir, nodeID, dataIP, dnsAddr string) {
	var lastHash uint64
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state := peer.State()
			h := stateHash(state)
			if h == lastHash {
				continue
			}
			if err := proxycfg.Write(dataDir, nodeID, dataIP, dnsAddr, state); err != nil {
				slog.Warn("proxycfg write failed", "err", err)
				continue
			}
			lastHash = h
		}
	}
}

// stateHash returns a cheap summary of the parts of ClusterState that affect the proxy config.
func stateHash(state internraft.ClusterState) uint64 {
	var h uint64
	for _, svc := range state.Services {
		h ^= uint64(svc.SystemPort)*2654435761 ^ fnvStr(svc.Name) ^ fnvStr(svc.WorkloadName)
	}
	for _, wl := range state.Workloads {
		h ^= fnvStr(wl.NodeID) ^ fnvStr(wl.Name())
		for _, pa := range wl.PortAllocations {
			h ^= uint64(pa.AllocatedPort) * 2246822519
		}
	}
	for _, n := range state.Nodes {
		h ^= fnvStr(n.DataIP) ^ fnvStr(n.ID)
	}
	return h
}

// ── ingressd system container ─────────────────────────────────────────────────

type ingressdState struct {
	ContainerID  string `json:"container_id"`
	RegistryAddr string `json:"registry_addr"`
	HTTPBind     string `json:"http_bind"`
	HTTPSBind    string `json:"https_bind"`
}

func ingressdStatePath(dataDir string) string {
	return filepath.Join(dataDir, "system", "ingressd.json")
}

func readIngressdState(dataDir string) (ingressdState, bool) {
	data, err := os.ReadFile(ingressdStatePath(dataDir))
	if err != nil {
		return ingressdState{}, false
	}
	var s ingressdState
	if err := json.Unmarshal(data, &s); err != nil {
		return ingressdState{}, false
	}
	return s, true
}

func writeIngressdState(dataDir string, s ingressdState) error {
	dir := filepath.Join(dataDir, "system")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ingressdStatePath(dataDir), data, 0o600)
}

// nerdctlCmd builds a nerdctl exec.Cmd with the given namespace and socket.
func nerdctlCmd(ctx context.Context, bin, namespace, sockAddr string, args ...string) *exec.Cmd {
	full := []string{"--namespace", namespace}
	if sockAddr != "" {
		full = append(full, "--address", sockAddr)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if sockAddr != "" {
		cmd.Env = append(os.Environ(), "CONTAINERD_ADDRESS="+sockAddr)
	}
	return cmd
}

// reconcileIngressd ensures the ingressd container is running with the ports
// specified in cfg. If the container exists with different settings it is stopped
// and restarted. Called once on startup after the local registry is up.
func reconcileIngressd(ctx context.Context, cfg config) {
	const name = "ingressd"
	image := cfg.registryAddr + "/" + name + ":latest"

	prev, hasPrev := readIngressdState(cfg.dataDir)
	stale := hasPrev && (prev.RegistryAddr != cfg.registryAddr ||
		prev.HTTPBind != cfg.ingressdHTTP ||
		prev.HTTPSBind != cfg.ingressdHTTPS)

	// Check whether the container is currently running.
	inspectOut, err := nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr,
		"inspect", "--format", "{{.State.Status}}", name).Output()
	running := err == nil && strings.TrimSpace(string(inspectOut)) == "running"

	if stale && running {
		slog.Info("ingressd config changed, replacing container")
		_ = nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, "stop", name).Run()
		_ = nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, "rm", "-f", name).Run()
		running = false
	}

	if running {
		slog.Info("ingressd already running, no action needed")
		return
	}

	// Pull the image from the local registry.
	slog.Info("pulling ingressd image", "image", image)
	if out, err := nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr,
		"pull", "--insecure-registry", image).CombinedOutput(); err != nil {
		slog.Error("ingressd image pull failed", "err", err, "output", strings.TrimSpace(string(out)))
		return
	}

	// Ensure the ingress config directory exists before bind-mounting it.
	_ = os.MkdirAll(filepath.Join(cfg.dataDir, "ingress"), 0o700)

	args := []string{
		"run", "-d", "--name", name,
		"--restart", "always",
		"--insecure-registry",
		"-v", filepath.Join(cfg.dataDir, "ingress") + ":/data/ingress",
		"-p", cfg.ingressdHTTP + ":80",
	}
	if cfg.ingressdHTTPS != "" {
		args = append(args, "-p", cfg.ingressdHTTPS+":443")
	}
	args = append(args, image)

	out, err := nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, args...).CombinedOutput()
	if err != nil {
		slog.Error("ingressd container start failed", "err", err, "output", strings.TrimSpace(string(out)))
		return
	}
	containerID := strings.TrimSpace(string(out))
	slog.Info("ingressd container started", "id", containerID,
		"http", cfg.ingressdHTTP, "https", cfg.ingressdHTTPS)

	_ = writeIngressdState(cfg.dataDir, ingressdState{
		ContainerID:  containerID,
		RegistryAddr: cfg.registryAddr,
		HTTPBind:     cfg.ingressdHTTP,
		HTTPSBind:    cfg.ingressdHTTPS,
	})
}

// ingresscfgWriteLoop polls Raft state every 2 seconds and rewrites the ingressd config file
// whenever ingress-relevant state changes. ingressd watches the file and reloads HAProxy.
func ingresscfgWriteLoop(ctx context.Context, peer *internraft.Peer, dataDir string) {
	var lastHash uint64
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state := peer.State()
			h := ingressStateHash(state)
			if h == lastHash {
				continue
			}
			if err := ingresscfg.Write(dataDir, state); err != nil {
				slog.Warn("ingresscfg write failed", "err", err)
				continue
			}
			lastHash = h
		}
	}
}

// ingressStateHash returns a cheap summary of the parts of ClusterState that affect the ingress config.
func ingressStateHash(state internraft.ClusterState) uint64 {
	var h uint64
	for _, rule := range state.IngressRules {
		h ^= fnvStr(rule.ID) ^ fnvStr(rule.Host) ^ fnvStr(rule.PathPrefix) ^ fnvStr(rule.ServiceName) ^ fnvStr(rule.DomainID)
	}
	for _, svc := range state.Services {
		h ^= fnvStr(svc.Name) ^ uint64(svc.SystemPort)*2654435761
	}
	for _, d := range state.Domains {
		h ^= fnvStr(d.ID) ^ fnvStr(d.Name) ^ fnvStr(d.TLSCert)
		if d.Enabled {
			h ^= 1
		}
	}
	for _, wl := range state.Workloads {
		h ^= fnvStr(wl.Name()) ^ fnvStr(wl.NodeID)
	}
	for _, n := range state.Nodes {
		h ^= fnvStr(n.ID) ^ fnvStr(n.DataIP)
	}
	return h
}

func fnvStr(s string) uint64 {
	const prime = 1099511628211
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// parseDNSPort extracts the port number from a listen address like ":5353" or "0.0.0.0:5353".
// Returns 53 if the address cannot be parsed.
func parseDNSPort(addr string) uint32 {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 53
	}
	var p uint32
	if _, err := fmt.Sscanf(portStr, "%d", &p); err != nil || p == 0 {
		return 53
	}
	return p
}
