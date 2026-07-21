package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	"github.com/eghansah/orchestrator/internal/meshd"
	"github.com/eghansah/orchestrator/internal/nerdctl"
	"github.com/eghansah/orchestrator/internal/proxycfg"
	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/internal/tlsutil"
	"github.com/eghansah/orchestrator/internal/webui"
	"github.com/eghansah/orchestrator/pkg/types"
)

// version is stamped at build time via -ldflags "-X main.version=...".
// It is used as the ingressd image tag so each release is human-identifiable.
var version = "dev"

// meshNetworkName is the per-node nerdctl network managed containers attach to
// for a mesh IP. See docs/mesh-network.md.
const meshNetworkName = "mesh0"

// ── Config ────────────────────────────────────────────────────────────────────

type config struct {
	nodeID         string
	grpcAddr       string
	raftAddr       string
	webAddr        string
	webPrefix      string // URL prefix for the web console, e.g. "/console" (empty = root)
	ingressAddr    string // HTTP ingress proxy listen address; empty = disabled
	ingressTLSAddr string // HTTPS ingress proxy listen address; empty = disabled
	dnsAddr        string // DNS listen address; empty = disabled
	dataAddr       string // routable IP for container traffic; auto-detected if empty
	dataDir        string
	bootstrap      bool
	joinAddr       string // gRPC address of an existing node to join through
	joinToken      string // shared secret required to join the cluster
	adminToken     string // bearer token required for all ControlService RPCs
	webPassword    string // password for the web console login form
	webDisableMFA  bool   // skip TOTP MFA for all logins when true
	webTLS         bool   // serve the web console over HTTPS
	webTLSCert     string // path to a PEM cert (chain) for the web console; empty = use the node's self-signed cert
	webTLSKey      string // path to the PEM private key matching webTLSCert
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

	mesh            bool   // enable the WireGuard mesh overlay
	meshListenPort  int    // local UDP port for WireGuard (>= 1024)
	meshEndpoint    string // externally reachable WireGuard endpoint (host:port); default dataAddr:meshListenPort
	meshBackend     string // "container" (default) or "netstack"

	// Derived at runtime from embedded images, not from flags.
	ingressdImageTag      string // repo:tag the local registry serves, e.g. "ingressd:v1.4.0"
	ingressdImageDigest   string // manifest digest of the embedded image, e.g. "sha256:…"
	meshrouterdImageTag   string // repo:tag for the meshrouterd image
	meshrouterdImageDigest string // manifest digest of the embedded meshrouterd image
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
	flag.StringVar(&cfg.webAddr, "web-addr", ":7948", "web console listen address (empty to disable)")
	flag.StringVar(&cfg.webPrefix, "web-prefix", "", "URL prefix for the web console, e.g. /console (empty = serve at root)")
	flag.BoolVar(&cfg.webTLS, "web-tls", false, "serve the web console over HTTPS (uses the node's self-signed cert unless --web-tls-cert/--web-tls-key are set)")
	flag.StringVar(&cfg.webTLSCert, "web-tls-cert", "", "path to a PEM certificate (chain) for the web console; requires --web-tls-key")
	flag.StringVar(&cfg.webTLSKey, "web-tls-key", "", "path to the PEM private key matching --web-tls-cert")
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
	flag.BoolVar(&cfg.mesh, "mesh", false, "enable the userland WireGuard mesh overlay (see docs/mesh-network.md)")
	flag.IntVar(&cfg.meshListenPort, "mesh-listen-port", 51820, "local UDP port for the mesh WireGuard device (must be ≥1024)")
	flag.StringVar(&cfg.meshEndpoint, "mesh-endpoint", "", "externally reachable WireGuard endpoint host:port (default: data-addr:mesh-listen-port)")
	flag.StringVar(&cfg.meshBackend, "mesh-backend", "container", `WireGuard device backend: "container" (meshrouterd, default) or "netstack" (in-process userland)`)
	flag.StringVar(&cfg.ldapAddr, "ldap-addr", "", "LDAP/AD server address host:port (empty = use local password auth)")
	flag.BoolVar(&cfg.ldapTLS, "ldap-tls", false, "use implicit TLS when connecting to LDAP (LDAPS, typically port 636)")
	flag.BoolVar(&cfg.ldapInsecure, "ldap-insecure", false, "skip TLS certificate verification for LDAP (for self-signed certs)")
	flag.StringVar(&cfg.ldapBindDNTemplate, "ldap-bind-dn-template", "", "how to form the bind DN; %%s is replaced with the username (e.g. %%s@corp.com or uid=%%s,ou=users,dc=corp,dc=com)")
	flag.StringVar(&cfg.ldapBaseDN, "ldap-base-dn", "", "LDAP search base DN for group membership check, e.g. DC=corp,DC=com")
	flag.StringVar(&cfg.ldapUserFilter, "ldap-user-filter", "", "LDAP search filter with %%s for username; required when --ldap-group-dn is set (e.g. (sAMAccountName=%%s))")
	flag.StringVar(&cfg.ldapGroupDN, "ldap-group-dn", "", "optional: restrict login to members of this group DN")
	flag.Parse()

	if (cfg.webTLSCert == "") != (cfg.webTLSKey == "") {
		dieOnErr(fmt.Errorf("--web-tls-cert and --web-tls-key must be set together"), "parse flags")
	}

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
	// Auto-bootstrap when the data directory is fresh (no raft.db) and this
	// node is not joining an existing cluster. This lets a first-time deployment
	// work without any flags beyond what the user actually cares about.
	if !cfg.bootstrap && cfg.joinAddr == "" {
		raftDB := filepath.Join(cfg.dataDir, "raft", "raft.db")
		if _, err := os.Stat(raftDB); os.IsNotExist(err) {
			cfg.bootstrap = true
		}
	}
	cfg.joinToken = resolveJoinToken(cfg.dataDir, cfg.joinToken, cfg.bootstrap)
	cfg.adminToken = resolveAdminToken(cfg.dataDir, cfg.adminToken, cfg.bootstrap)
	cfg.webPassword = resolveWebPassword(cfg.dataDir, cfg.webPassword, cfg.bootstrap)
	if cfg.dataAddr == "" {
		cfg.dataAddr = detectDataIP()
	}

	logConfig(cfg)

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
		imageTag := "ingressd:" + sanitizeImageTag(version)
		reg, err := localregistry.New(localregistry.IngressdTar, imageTag)
		if err != nil {
			slog.Warn("local registry: failed to load ingressd image", "err", err)
			reg, _ = localregistry.New(nil, imageTag) // start empty
		}
		// Record what the registry actually serves so reconcileIngressd can pull
		// it by its readable tag and detect content changes by digest.
		cfg.ingressdImageTag = reg.ImageTag()
		cfg.ingressdImageDigest = reg.Digest()

		// Load the meshrouterd image into the same registry if it was embedded.
		meshTag := "meshrouterd:" + sanitizeImageTag(version)
		if dig, err := reg.Load(localregistry.MeshrouterdTar, meshTag); err != nil {
			slog.Warn("local registry: failed to load meshrouterd image", "err", err)
		} else if dig != "" {
			cfg.meshrouterdImageTag = meshTag
			cfg.meshrouterdImageDigest = dig
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
		Peer:         peer,
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
		webSrv := webui.New(peer, ctrl, ag, cfg.adminToken, cfg.webPassword, cfg.webPrefix, version, cfg.webDisableMFA, webui.LDAPConfig{
			Addr:           cfg.ldapAddr,
			UseTLS:         cfg.ldapTLS,
			Insecure:       cfg.ldapInsecure,
			BindDNTemplate: cfg.ldapBindDNTemplate,
			BaseDN:         cfg.ldapBaseDN,
			UserFilter:     cfg.ldapUserFilter,
			GroupDN:        cfg.ldapGroupDN,
		})
		httpSrv := &http.Server{Addr: cfg.webAddr, Handler: webSrv.Handler()}
		if cfg.webTLS {
			webCert := tlsCert
			if cfg.webTLSCert != "" {
				webCert, err = tls.LoadX509KeyPair(cfg.webTLSCert, cfg.webTLSKey)
				dieOnErr(err, "load web console TLS cert")
			}
			httpSrv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{webCert}}
			go func() {
				slog.Info("web console listening (https)", "addr", cfg.webAddr)
				if err := httpSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
					slog.Error("web console error", "err", err)
				}
			}()
		} else {
			go func() {
				slog.Info("web console listening", "addr", cfg.webAddr)
				if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					slog.Error("web console error", "err", err)
				}
			}()
		}
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
	go proxycfgWriteLoop(ctx, peer, ag, cfg.dataDir, cfg.nodeID, cfg.dataAddr, cfg.dnsAddr)

	// 5e. Ingress config writer ------------------------------------------------
	// Write <dataDir>/ingress/config.json; ingressd watches this file and drives HAProxy.
	_ = ingresscfg.Write(cfg.dataDir, peer.State())
	go ingresscfgWriteLoop(ctx, peer, cfg.dataDir)

	// 5f. Mesh overlay (optional) ---------------------------------------------
	// The node owns its WireGuard identity and publishes its public key + endpoint
	// into cluster state; the leader assigns it a /24. meshd then keeps the local
	// WireGuard peer set in sync with the cluster. See docs/mesh-network.md.
	var meshMgr *meshd.Manager
	var meshPubKey, meshEndpoint string
	if cfg.mesh {
		if cfg.meshListenPort < 1024 {
			dieOnErr(fmt.Errorf("mesh-listen-port %d is below 1024 (rootless cannot bind privileged ports)", cfg.meshListenPort), "mesh config")
		}
		meshEndpoint = cfg.meshEndpoint
		if meshEndpoint == "" {
			meshEndpoint = fmt.Sprintf("%s:%d", cfg.dataAddr, cfg.meshListenPort)
		}
		var deviceFactory meshd.DeviceFactory
		switch cfg.meshBackend {
		case "netstack":
			deviceFactory = func(meshAddr string) (meshd.Device, error) {
				return meshd.NewNetstackDevice(meshAddr, meshd.DefaultMTU)
			}
		default: // "container"
			deviceFactory = meshd.NewContainerDeviceFactory(cfg.dataDir)
		}
		mgr, err := meshd.NewManager(cfg.dataDir, meshEndpoint, cfg.meshListenPort, deviceFactory)
		dieOnErr(err, "create mesh manager")
		meshMgr = mgr
		meshPubKey = mgr.PublicKey()
		defer meshMgr.Close() //nolint:errcheck
		slog.Info("mesh overlay enabled", "endpoint", meshEndpoint, "pubkey", meshPubKey,
			"listen-port", cfg.meshListenPort, "backend", cfg.meshBackend)
	}

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
	go selfRegisterLoop(ctx, peer, cfg.nodeID, cfg.grpcAddr, cfg.dataAddr, ownCertDER, meshPubKey, meshEndpoint)

	// Keep the local WireGuard peer set in sync with cluster state, and ensure the
	// per-node mesh0 nerdctl network exists so managed containers get mesh IPs.
	if meshMgr != nil {
		go meshMgr.Run(ctx, cfg.nodeID, func() map[string]types.Node { return peer.State().Nodes })
		go meshNetworkLoop(ctx, peer, nc, cfg.nodeID, meshNetworkName)
		if cfg.meshBackend == "container" && cfg.meshrouterdImageTag != "" {
			go func() {
				// Brief settle delay so the local registry is accepting connections
				// before reconcileMeshRouter tries to pull the image.
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				reconcileMeshRouterLoop(ctx, peer, cfg)
			}()
		}
	}

	// Once we are leader, register the built-in local registry so the web UI
	// and other cluster components can discover it.
	if cfg.registryAddr != "" {
		go registryRegisterLoop(ctx, peer, cfg.nodeID, cfg.registryAddr)
	}

	// Periodically forward actual state to the leader's ReportState RPC.
	go stateReportLoop(ctx, peer, tlsCert, cfg.nodeID, cfg.grpcAddr, stateCh)

	// If joining an existing cluster, heartbeat the given node so the leader
	// can add us as a Raft voter and register us in the cluster state.
	if cfg.joinAddr != "" {
		go heartbeatLoop(ctx, peer, tlsCert, ownCertDER, cfg.nodeID, cfg.grpcAddr, cfg.raftAddr, cfg.dataAddr, cfg.joinAddr, cfg.joinToken, meshPubKey, meshEndpoint)
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
				ID:           req.NodeId,
				Address:      req.Address,
				Status:       types.NodeHealthy,
				TLSCert:      req.TlsCert,
				DataIP:       req.DataIp,
				MeshPubKey:   req.MeshPubKey,
				MeshEndpoint: req.MeshEndpoint,
				LastSeenAt:   time.Now(),
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
func selfRegisterLoop(ctx context.Context, peer *internraft.Peer, nodeID, grpcAddr, dataIP string, ownCertDER []byte, meshPubKey, meshEndpoint string) {
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
				ID:           nodeID,
				Address:      grpcAddr,
				Status:       types.NodeHealthy,
				TLSCert:      ownCertDER,
				DataIP:       dataIP,
				MeshPubKey:   meshPubKey,
				MeshEndpoint: meshEndpoint,
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

// meshNetworkLoop waits for the leader to assign this node a mesh subnet, then
// ensures the per-node mesh nerdctl network exists with that /24 and enables
// container attachment to it. It re-runs if the assigned subnet ever changes.
// Unlike the WireGuard peer sync (which runs in any case), this governs whether
// newly placed containers receive a mesh IP.
func meshNetworkLoop(ctx context.Context, peer *internraft.Peer, nc *nerdctl.Client, nodeID, netName string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var lastSubnet string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, ok := peer.State().Nodes[nodeID]
			if !ok || n.MeshSubnet == "" || n.MeshSubnet == lastSubnet {
				continue
			}
			if err := nc.EnsureMeshNetwork(ctx, netName, n.MeshSubnet, n.MeshAddr, meshd.DefaultMTU); err != nil {
				slog.Warn("ensure mesh network failed", "network", netName, "subnet", n.MeshSubnet, "err", err)
				continue
			}
			nc.SetMeshNetwork(netName)
			lastSubnet = n.MeshSubnet
			slog.Info("mesh container network enabled", "network", netName, "subnet", n.MeshSubnet, "gateway", n.MeshAddr)
		}
	}
}

// registryRegisterLoop polls until this node becomes leader, then writes an entry
// for the built-in local registry into the Raft state so the web UI and scheduler
// can discover it. The registry is loopback-only; the URL reflects that.
func registryRegisterLoop(ctx context.Context, peer *internraft.Peer, nodeID, registryAddr string) {
	id := nodeID + ":local"
	url := "http://" + registryAddr
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
			if _, ok := peer.State().Registries[id]; ok {
				return // already registered
			}
			r := types.Registry{
				ID:        id,
				Name:      "local (" + nodeID + ")",
				URL:       url,
				CreatedAt: time.Now(),
			}
			if err := peer.ApplyRegistry(r); err != nil {
				slog.Warn("local registry auto-register failed", "err", err)
				continue
			}
			slog.Info("registered local registry in cluster state", "url", url)
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
	nodeID, grpcAddr, raftAddr, dataIP, seedAddr, token, meshPubKey, meshEndpoint string,
) {
	knownLeaderAddr := seedAddr
	var knownLeaderCert []byte // nil = TOFU until first successful heartbeat

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Send an immediate first heartbeat without waiting for the ticker.
	addr, cert := doHeartbeat(ctx, knownLeaderAddr, knownLeaderCert, ownCert, ownCertDER, nodeID, grpcAddr, raftAddr, dataIP, token, meshPubKey, meshEndpoint)
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
			addr, cert := doHeartbeat(ctx, knownLeaderAddr, knownLeaderCert, ownCert, ownCertDER, nodeID, grpcAddr, raftAddr, dataIP, token, meshPubKey, meshEndpoint)
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
	nodeID, grpcAddr, raftAddr, dataIP, token, meshPubKey, meshEndpoint string,
) (newLeaderAddr string, leaderCert []byte) {
	tlsCfg := tlsutil.ClientTLSConfig(ownCert, targetCertDER)
	conn, err := grpc.NewClient(targetAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		slog.Warn("heartbeat dial failed", "target", targetAddr, "err", err)
		return "", nil
	}
	defer conn.Close()

	resp, err := gen.NewNodeServiceClient(conn).Heartbeat(ctx, &gen.HeartbeatRequest{
		NodeId:       nodeID,
		Address:      grpcAddr,
		RaftAddress:  raftAddr,
		JoinToken:    token,
		TlsCert:      ownCertDER,
		DataIp:       dataIP,
		MeshPubKey:   meshPubKey,
		MeshEndpoint: meshEndpoint,
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

func setOrUnset(s string) string {
	if s != "" {
		return "(set)"
	}
	return "(not set)"
}

func logConfig(cfg config) {
	slog.Info("configuration",
		"node-id", cfg.nodeID,
		"grpc-addr", cfg.grpcAddr,
		"raft-addr", cfg.raftAddr,
		"web-addr", cfg.webAddr,
		"web-prefix", cfg.webPrefix,
		"web-disable-mfa", cfg.webDisableMFA,
		"web-tls", cfg.webTLS,
		"web-tls-cert", cfg.webTLSCert,
		"ingress-addr", cfg.ingressAddr,
		"ingress-tls-addr", cfg.ingressTLSAddr,
		"dns-addr", cfg.dnsAddr,
		"data-addr", cfg.dataAddr,
		"data-dir", cfg.dataDir,
		"bootstrap", cfg.bootstrap,
		"join", cfg.joinAddr,
		"join-token", setOrUnset(cfg.joinToken),
		"admin-token", setOrUnset(cfg.adminToken),
		"web-password", setOrUnset(cfg.webPassword),
		"nerdctl", cfg.nerdctlBin,
		"namespace", cfg.namespace,
		"containerd-addr", cfg.containerdAddr,
		"registry-addr", cfg.registryAddr,
		"ingressd-http", cfg.ingressdHTTP,
		"ingressd-https", cfg.ingressdHTTPS,
		"mesh", cfg.mesh,
		"mesh-listen-port", cfg.meshListenPort,
		"mesh-endpoint", cfg.meshEndpoint,
		"ldap-addr", cfg.ldapAddr,
		"ldap-tls", cfg.ldapTLS,
		"ldap-insecure", cfg.ldapInsecure,
		"ldap-bind-dn-template", cfg.ldapBindDNTemplate,
		"ldap-base-dn", cfg.ldapBaseDN,
		"ldap-user-filter", cfg.ldapUserFilter,
		"ldap-group-dn", cfg.ldapGroupDN,
	)
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
func proxycfgWriteLoop(ctx context.Context, peer *internraft.Peer, ag *agent.Agent, dataDir, nodeID, dataIP, dnsAddr string) {
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
			meshEntries := proxycfg.BuildMeshEntries(ag.AllStates(), state)
			if err := proxycfg.WriteWithMesh(dataDir, nodeID, dataIP, dnsAddr, state, meshEntries); err != nil {
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
		h ^= uint64(svc.SystemPort)*2654435761 ^ fnvStr(svc.Name) ^ fnvStr(svc.ContainerFQDN)
	}
	for _, rule := range state.IngressRules {
		h ^= uint64(rule.SystemPort)*2654435761 ^ fnvStr(rule.ID) ^ fnvStr(rule.ContainerFQDN)
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
	ImageDigest  string `json:"image_digest"` // manifest digest of the image this container runs
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

// runningImageDigest resolves the manifest digest that the named container's
// image reference currently points to in the local image store. It does not
// rely on any locally cached bookkeeping (the ingressd.json/meshrouterd.json
// state files) — those can be stale, missing, or predate digest tracking
// entirely, in which case a reconciler that trusts them alone can conclude
// "no action needed" while the running container is actually serving
// different content. Returns "" if the container isn't running or its image
// can't be resolved, so callers should treat "" as "unknown", not "match".
func runningImageDigest(ctx context.Context, bin, namespace, sockAddr, name string) string {
	imgOut, err := nerdctlCmd(ctx, bin, namespace, sockAddr,
		"inspect", "--type=container", "--format", "{{.Image}}", name).Output()
	if err != nil {
		return ""
	}
	imageRef := strings.TrimSpace(string(imgOut))
	if imageRef == "" {
		return ""
	}
	digOut, err := nerdctlCmd(ctx, bin, namespace, sockAddr,
		"inspect", "--type=image", "--format", "{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}", imageRef).Output()
	if err != nil {
		return ""
	}
	repoDigest := strings.TrimSpace(string(digOut))
	if i := strings.LastIndex(repoDigest, "@"); i >= 0 {
		return repoDigest[i+1:]
	}
	return ""
}

// reconcileIngressd ensures the ingressd container is running with the ports
// specified in cfg. If the container exists with different settings it is stopped
// and restarted. Called once on startup after the local registry is up.
func reconcileIngressd(ctx context.Context, cfg config) {
	const name = "ingressd"
	tag := cfg.ingressdImageTag
	if tag == "" {
		tag = "ingressd:latest"
	}
	image := cfg.registryAddr + "/" + tag

	prev, hasPrev := readIngressdState(cfg.dataDir)
	// Replace the container when its bind settings change or when the embedded
	// image content changes. The version tag is for human readability; the
	// digest is the source of truth, so a rebuilt image (even one reusing the
	// same version string, e.g. a dirty dev build) is still detected here.
	stale := !hasPrev ||
		prev.RegistryAddr != cfg.registryAddr ||
		prev.HTTPBind != cfg.ingressdHTTP ||
		prev.HTTPSBind != cfg.ingressdHTTPS ||
		prev.ImageDigest != cfg.ingressdImageDigest

	// Check whether the container is currently running.
	inspectOut, err := nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr,
		"inspect", "--format", "{{.State.Status}}", name).Output()
	running := err == nil && strings.TrimSpace(string(inspectOut)) == "running"

	// Our own bookkeeping can say "unchanged" while the running container is
	// actually serving different content (stale/missing/pre-digest state
	// file). Verify against ground truth before trusting it.
	if running && !stale && cfg.ingressdImageDigest != "" {
		if actual := runningImageDigest(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, name); actual != "" && actual != cfg.ingressdImageDigest {
			slog.Info("ingressd running container's image does not match embedded image, replacing",
				"running_digest", actual, "expected_digest", cfg.ingressdImageDigest)
			stale = true
		}
	}

	if stale && running {
		slog.Info("ingressd settings or image changed, replacing container",
			"old_digest", prev.ImageDigest, "new_digest", cfg.ingressdImageDigest)
		_ = nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, "stop", name).Run()
		_ = nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, "rm", "-f", name).Run()
		// Drop the cached image so the re-pull below fetches the new content even
		// when the tag string is unchanged; a mutable tag would otherwise resolve
		// to the stale local copy.
		_ = nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, "rmi", "-f", image).Run()
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
		ImageDigest:  cfg.ingressdImageDigest,
	})
}

// ── meshrouterd reconciliation ───────────────────────────────────────────────

type meshrouterdState struct {
	ContainerID  string `json:"container_id"`
	RegistryAddr string `json:"registry_addr"`
	MeshAddr     string `json:"mesh_addr"`
	MeshCIDR     string `json:"mesh_cidr"`
	ListenPort   int    `json:"listen_port"`
	ImageDigest  string `json:"image_digest"`
	RlkPortID    int    `json:"rlk_port_id"` // rootlesskit port-mapping ID; 0 if not registered
}

func meshrouterdStatePath(dataDir string) string {
	return filepath.Join(dataDir, "system", "meshrouterd.json")
}

func readMeshrouterdState(dataDir string) (meshrouterdState, bool) {
	data, err := os.ReadFile(meshrouterdStatePath(dataDir))
	if err != nil {
		return meshrouterdState{}, false
	}
	var s meshrouterdState
	if err := json.Unmarshal(data, &s); err != nil {
		return meshrouterdState{}, false
	}
	return s, true
}

func writeMeshrouterdState(dataDir string, s meshrouterdState) error {
	dir := filepath.Join(dataDir, "system")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(meshrouterdStatePath(dataDir), data, 0o600)
}

// reconcileMeshRouterLoop polls Raft state until the node gets a mesh address
// assigned, then calls reconcileMeshRouter. Re-reconciles whenever the mesh
// address, cluster CIDR, or embedded image digest changes.
func reconcileMeshRouterLoop(ctx context.Context, peer *internraft.Peer, cfg config) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var lastKey string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, ok := peer.State().Nodes[cfg.nodeID]
			if !ok || n.MeshAddr == "" {
				continue
			}
			meshCIDR := peer.State().MeshCIDR
			if meshCIDR == "" {
				meshCIDR = "100.64.0.0/10"
			}
			key := n.MeshAddr + "\x00" + meshCIDR + "\x00" + cfg.meshrouterdImageDigest
			if key == lastKey {
				continue
			}
			reconcileMeshRouter(ctx, cfg, n.MeshAddr, meshCIDR)
			lastKey = key
		}
	}
}

// reconcileMeshRouter ensures the meshrouterd container is running in the
// RootlessKit network namespace. If it already exists with the same config it
// is left untouched; if config or image changed it is replaced. After a
// successful start the WireGuard UDP port is registered with rootlesskit so
// inbound encrypted packets reach the container.
func reconcileMeshRouter(ctx context.Context, cfg config, meshAddr, meshCIDR string) {
	const name = "meshrouterd"
	tag := cfg.meshrouterdImageTag
	if tag == "" {
		tag = "meshrouterd:latest"
	}
	image := cfg.registryAddr + "/" + tag

	prev, hasPrev := readMeshrouterdState(cfg.dataDir)
	stale := !hasPrev ||
		prev.RegistryAddr != cfg.registryAddr ||
		prev.MeshAddr != meshAddr ||
		prev.MeshCIDR != meshCIDR ||
		prev.ListenPort != cfg.meshListenPort ||
		prev.ImageDigest != cfg.meshrouterdImageDigest

	inspectOut, err := nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr,
		"inspect", "--format", "{{.State.Status}}", name).Output()
	running := err == nil && strings.TrimSpace(string(inspectOut)) == "running"

	// Our own bookkeeping can say "unchanged" while the running container is
	// actually serving different content (stale/missing/pre-digest state
	// file). Verify against ground truth before trusting it.
	if running && !stale && cfg.meshrouterdImageDigest != "" {
		if actual := runningImageDigest(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, name); actual != "" && actual != cfg.meshrouterdImageDigest {
			slog.Info("meshrouterd running container's image does not match embedded image, replacing",
				"running_digest", actual, "expected_digest", cfg.meshrouterdImageDigest)
			stale = true
		}
	}

	if stale && running {
		slog.Info("meshrouterd settings or image changed, replacing container",
			"old_digest", prev.ImageDigest, "new_digest", cfg.meshrouterdImageDigest)
		_ = nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, "stop", name).Run()
		if prev.RlkPortID != 0 {
			if err := rlkDeletePort(xdgRuntimeDir(), prev.RlkPortID); err != nil {
				slog.Warn("rootlesskit port delete failed", "id", prev.RlkPortID, "err", err)
			}
		}
		_ = nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, "rm", "-f", name).Run()
		_ = nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, "rmi", "-f", image).Run()
		running = false
	}

	if running {
		slog.Info("meshrouterd already running, no action needed")
		return
	}

	slog.Info("pulling meshrouterd image", "image", image)
	if out, err := nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr,
		"pull", "--insecure-registry", image).CombinedOutput(); err != nil {
		slog.Error("meshrouterd image pull failed", "err", err, "output", strings.TrimSpace(string(out)))
		return
	}

	_ = os.MkdirAll(filepath.Join(cfg.dataDir, "mesh"), 0o700)

	rlkNetnsPath := filepath.Join(xdgRuntimeDir(), "containerd-rootless", "netns")

	args := []string{
		"run", "-d", "--name", name,
		"--restart", "always",
		"--network", "ns:" + rlkNetnsPath,
		"--cap-add", "NET_ADMIN",
		"--device", "/dev/net/tun",
		"--insecure-registry",
		"-v", filepath.Join(cfg.dataDir, "mesh") + ":/data/mesh",
		image,
		"--mesh-addr", meshAddr,
		"--mesh-cidr", meshCIDR,
		"--listen-port", fmt.Sprintf("%d", cfg.meshListenPort),
		"--private-key-file", "/data/mesh/private.key",
		"--config", "/data/mesh/peers.conf",
	}

	out, err := nerdctlCmd(ctx, cfg.nerdctlBin, cfg.namespace, cfg.containerdAddr, args...).CombinedOutput()
	if err != nil {
		slog.Error("meshrouterd container start failed", "err", err, "output", strings.TrimSpace(string(out)))
		return
	}
	containerID := strings.TrimSpace(string(out))
	slog.Info("meshrouterd container started", "id", containerID, "mesh_addr", meshAddr)

	portID, err := rlkRegisterPort(xdgRuntimeDir(), cfg.meshListenPort)
	if err != nil {
		slog.Warn("rootlesskit UDP port registration failed — inbound WireGuard will not be forwarded",
			"port", cfg.meshListenPort, "err", err)
	} else {
		slog.Info("rootlesskit UDP port registered", "port", cfg.meshListenPort, "rlk_id", portID)
	}

	_ = writeMeshrouterdState(cfg.dataDir, meshrouterdState{
		ContainerID:  containerID,
		RegistryAddr: cfg.registryAddr,
		MeshAddr:     meshAddr,
		MeshCIDR:     meshCIDR,
		ListenPort:   cfg.meshListenPort,
		ImageDigest:  cfg.meshrouterdImageDigest,
		RlkPortID:    portID,
	})
}

// xdgRuntimeDir returns $XDG_RUNTIME_DIR or falls back to /run/user/<uid>.
func xdgRuntimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return v
	}
	return fmt.Sprintf("/run/user/%d", os.Getuid())
}

// rlkRegisterPort calls the rootlesskit port API to add a UDP forward from
// parentPort on the host into the RootlessKit netns. Returns the port-mapping
// ID that must be passed to rlkDeletePort to remove the forward.
func rlkRegisterPort(rlkDir string, port int) (int, error) {
	sockPath := filepath.Join(rlkDir, "containerd-rootless", "api.sock")
	c := rlkHTTPClient(sockPath)

	body, _ := json.Marshal(map[string]any{
		"proto":      "udp",
		"parentIP":   "",
		"parentPort": port,
		"childIP":    "",
		"childPort":  port,
	})
	resp, err := c.Post("http://local/v1/ports", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("POST /v1/ports: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("POST /v1/ports: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var result struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	return result.ID, nil
}

// rlkDeletePort removes a rootlesskit port forward by its ID.
func rlkDeletePort(rlkDir string, portID int) error {
	sockPath := filepath.Join(rlkDir, "containerd-rootless", "api.sock")
	c := rlkHTTPClient(sockPath)

	req, err := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("http://local/v1/ports/%d", portID), nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE /v1/ports/%d: %w", portID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("DELETE /v1/ports/%d: status %d: %s", portID, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// rlkHTTPClient returns an http.Client that dials the given Unix socket.
// The "local" host in request URLs is a placeholder; the transport ignores it.
func rlkHTTPClient(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
}

// sanitizeImageTag coerces an arbitrary version string into a valid OCI tag:
// up to 128 chars from [A-Za-z0-9_.-], with anything else replaced by '-'.
// git-describe output (e.g. "v1.4.0-5-gabc123-dirty") already qualifies.
func sanitizeImageTag(v string) string {
	if v == "" {
		return "latest"
	}
	var sb strings.Builder
	for i, c := range v {
		if i >= 128 {
			break
		}
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '.', c == '-':
			sb.WriteRune(c)
		default:
			sb.WriteRune('-')
		}
	}
	return sb.String()
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
			// ingressd polls this file by mtime and reloads HAProxy in place, so
			// publishing a new HTTP service takes effect without restarting the
			// container — no dropped connections.
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
		h ^= fnvStr(rule.ID) ^ fnvStr(rule.Host) ^ fnvStr(rule.PathPrefix) ^ fnvStr(rule.ContainerFQDN) ^ fnvStr(rule.DomainID) ^ uint64(rule.ContainerPort)*2246822519
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
