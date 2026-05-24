package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
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
	"github.com/eghansah/orchestrator/internal/nerdctl"
	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/internal/tlsutil"
	"github.com/eghansah/orchestrator/internal/webui"
	"github.com/eghansah/orchestrator/pkg/types"
)

// ── Config ────────────────────────────────────────────────────────────────────

type config struct {
	nodeID      string
	grpcAddr    string
	raftAddr    string
	webAddr     string
	ingressAddr string // HTTP ingress proxy listen address; empty = disabled
	dataAddr    string // routable IP for container traffic; auto-detected if empty
	dataDir     string
	bootstrap   bool
	joinAddr    string // gRPC address of an existing node to join through
	joinToken   string // shared secret required to join the cluster
	nerdctlBin  string
	namespace   string
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.nodeID, "node-id", "", "unique node identifier (default: hostname)")
	flag.StringVar(&cfg.grpcAddr, "grpc-addr", ":7946", "gRPC listen address (host:port)")
	flag.StringVar(&cfg.raftAddr, "raft-addr", ":7947", "Raft TCP listen address (host:port)")
	flag.StringVar(&cfg.dataDir, "data-dir", "", "persistent data directory (default: ~/.local/share/orchestrator)")
	flag.BoolVar(&cfg.bootstrap, "bootstrap", false, "bootstrap a new single-node cluster")
	flag.StringVar(&cfg.joinAddr, "join", "", "gRPC address of an existing cluster node to join")
	flag.StringVar(&cfg.joinToken, "join-token", "", "shared secret required to join the cluster")
	flag.StringVar(&cfg.webAddr, "web-addr", ":7948", "web console HTTP listen address (empty to disable)")
	flag.StringVar(&cfg.ingressAddr, "ingress-addr", ":8080", "HTTP ingress proxy listen address (empty to disable)")
	flag.StringVar(&cfg.dataAddr, "data-addr", "", "routable IP for container traffic (auto-detected if empty)")
	flag.StringVar(&cfg.nerdctlBin, "nerdctl", "nerdctl", "path to nerdctl binary")
	flag.StringVar(&cfg.namespace, "namespace", "orchestrator", "nerdctl namespace for managed containers")
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
	nc, err := nerdctl.NewClient(cfg.nerdctlBin, cfg.namespace, cfg.dataDir)
	dieOnErr(err, "create nerdctl client")
	if err := nc.Probe(ctx); err != nil {
		slog.Error("nerdctl probe failed — is nerdctl installed in rootless mode?", "err", err)
		os.Exit(1)
	}
	slog.Info("nerdctl ready", "namespace", cfg.namespace)

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
	ctrl := control.New(peer, cfg.nodeID, cfg.grpcAddr, tlsCert)

	// 5b. Web console (optional) ----------------------------------------------
	if cfg.webAddr != "" {
		webSrv := webui.New(peer, ctrl)
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
	if cfg.ingressAddr != "" {
		proxy := ingress.New(peer)
		ingressSrv := &http.Server{Addr: cfg.ingressAddr, Handler: proxy}
		go func() {
			slog.Info("ingress proxy listening", "addr", cfg.ingressAddr)
			if err := ingressSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("ingress proxy error", "err", err)
			}
		}()
		defer ingressSrv.Shutdown(context.Background()) //nolint:errcheck
	}

	serverTLS := tlsutil.ServerTLSConfig(tlsCert, isPinned)
	grpcSrv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.UnaryInterceptor(tlsutil.NodeAuthInterceptor(isPinned)),
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
