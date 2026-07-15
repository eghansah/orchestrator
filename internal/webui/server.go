package webui

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pquerna/otp/totp"

	"github.com/eghansah/orchestrator/docs"
	"github.com/eghansah/orchestrator/internal/agent"
	"github.com/eghansah/orchestrator/internal/baoclient"
	"github.com/eghansah/orchestrator/internal/control"
	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/internal/registry"
	"github.com/eghansah/orchestrator/pkg/export"
	"github.com/eghansah/orchestrator/pkg/types"
)

// Server serves the Cloudscape web console and JSON REST API.
type Server struct {
	peer          *internraft.Peer
	ctrl          *control.Server
	agent         *agent.Agent
	adminToken    string // required Bearer token for /api/* routes; empty = no auth
	webPassword   string // password for the /api/auth/login endpoint; empty = login disabled
	prefix        string // URL path prefix, e.g. "/console" (no trailing slash, may be "")
	disableMFA    bool   // when true, skip TOTP step and issue session on password success
	ldap          LDAPConfig
	version       string // stamped at build time; "dev" in local builds

	sessionsMu sync.RWMutex
	sessions   map[string]time.Time // per-login token → expiry

	pendingMu sync.Mutex
	pending   map[string]pendingMFA // short-lived token → pending MFA state
}

const sessionTTL = 8 * time.Hour
const pendingTTL = 5 * time.Minute

type pendingMFA struct {
	username string
	isSetup  bool   // true = first-time enrollment; false = regular verify
	secret   string // non-empty only during setup (new secret, not yet persisted)
	expiry   time.Time
}

func (s *Server) newSession() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	token := fmt.Sprintf("%x", b)
	s.sessionsMu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	s.sessionsMu.Unlock()
	return token
}

func (s *Server) isValidSession(token string) bool {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *Server) revokeSession(token string) {
	s.sessionsMu.Lock()
	delete(s.sessions, token)
	s.sessionsMu.Unlock()
}

func (s *Server) newPendingToken(pm pendingMFA) string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	token := fmt.Sprintf("%x", b)
	s.pendingMu.Lock()
	s.pending[token] = pm
	s.pendingMu.Unlock()
	return token
}

func (s *Server) consumePendingToken(token string) (pendingMFA, bool) {
	s.pendingMu.Lock()
	pm, ok := s.pending[token]
	delete(s.pending, token)
	s.pendingMu.Unlock()
	if !ok || time.Now().After(pm.expiry) {
		return pendingMFA{}, false
	}
	return pm, true
}

// New creates a Server. prefix is an optional URL subdirectory (e.g. "/console");
// pass "" to serve at the root. A trailing slash is stripped automatically.
func New(peer *internraft.Peer, ctrl *control.Server, ag *agent.Agent, adminToken, webPassword, prefix, version string, disableMFA bool, ldapCfg LDAPConfig) *Server {
	p := strings.TrimRight(prefix, "/")
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if ldapCfg.UserFilter == "" {
		ldapCfg.UserFilter = "(sAMAccountName=%s)"
	}
	if version == "" {
		version = "dev"
	}
	return &Server{
		peer:        peer,
		ctrl:        ctrl,
		agent:       ag,
		adminToken:  adminToken,
		webPassword: webPassword,
		prefix:      p,
		version:     version,
		disableMFA:  disableMFA,
		ldap:        ldapCfg,
		sessions:    make(map[string]time.Time),
		pending:     make(map[string]pendingMFA),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated routes.
	mux.Handle("GET /api/version", http.HandlerFunc(s.handleVersion))
	mux.Handle("POST /api/auth/login", http.HandlerFunc(s.handleLogin))
	mux.Handle("POST /api/auth/logout", http.HandlerFunc(s.handleLogout))
	mux.Handle("POST /api/auth/mfa", http.HandlerFunc(s.handleMFA))

	// API routes — all require a valid admin token when one is configured.
	a := s.auth
	mux.Handle("GET /api/state", a(s.handleState))
	mux.Handle("GET /api/workloads/{id}", a(s.handleGetWorkload))
	mux.Handle("POST /api/workloads/run", a(s.handleRun))
	mux.Handle("POST /api/workloads/stack", a(s.handleStack))
	mux.Handle("POST /api/workloads/{id}/remove", a(s.handleRemove))
	mux.Handle("POST /api/nodes/{id}/drain", a(s.handleDrain))
	mux.Handle("GET /api/ingress", a(s.handleListIngress))
	mux.Handle("POST /api/ingress", a(s.handleCreateIngress))
	mux.Handle("POST /api/ingress/{id}/update", a(s.handleUpdateIngress))
	mux.Handle("POST /api/ingress/{id}/delete", a(s.handleDeleteIngress))
	mux.Handle("GET /api/services", a(s.handleListServices))
	mux.Handle("POST /api/services", a(s.handleCreateService))
	mux.Handle("POST /api/services/{id}/update", a(s.handleUpdateService))
	mux.Handle("POST /api/services/{id}/delete", a(s.handleDeleteService))
	mux.Handle("GET /api/domains", a(s.handleListDomains))
	mux.Handle("POST /api/domains", a(s.handleCreateDomain))
	mux.Handle("POST /api/domains/{id}/update", a(s.handleUpdateDomain))
	mux.Handle("POST /api/domains/{id}/toggle", a(s.handleToggleDomain))
	mux.Handle("POST /api/domains/{id}/delete", a(s.handleDeleteDomain))
	mux.Handle("POST /api/domains/{id}/regenerate", a(s.handleRegenerateDomainKeys))
	mux.Handle("POST /api/domains/{id}/import-cert", a(s.handleImportDomainCert))
	mux.Handle("GET /api/users", a(s.handleListUsers))
	mux.Handle("POST /api/users", a(s.handleCreateUser))
	mux.Handle("POST /api/users/{id}/toggle", a(s.handleToggleUser))
	mux.Handle("POST /api/users/{id}/delete", a(s.handleDeleteUser))
	mux.Handle("POST /api/users/{id}/reset-mfa", a(s.handleResetUserMFA))
	mux.Handle("GET /api/registries", a(s.handleListRegistries))
	mux.Handle("POST /api/registries", a(s.handleCreateRegistry))
	mux.Handle("POST /api/registries/{id}/update", a(s.handleUpdateRegistry))
	mux.Handle("POST /api/registries/{id}/delete", a(s.handleDeleteRegistry))
	mux.Handle("GET /api/templates", a(s.handleListTemplates))
	mux.Handle("POST /api/templates", a(s.handleCreateTemplate))
	mux.Handle("POST /api/templates/{id}/update", a(s.handleUpdateTemplate))
	mux.Handle("POST /api/templates/{id}/delete", a(s.handleDeleteTemplate))
	mux.Handle("POST /api/templates/{id}/deploy", a(s.handleDeployTemplate))
	mux.Handle("GET /api/containers/{name}/logs", a(s.handleContainerLogs))
	mux.Handle("GET /api/containers/{name}/inspect", a(s.handleInspectContainer))
	mux.Handle("POST /api/containers/{name}/restart", a(s.handleRestartContainer))
	mux.Handle("GET /api/registries/{id}/catalog", a(s.handleRegistryCatalog))
	mux.Handle("GET /api/registries/{id}/tags", a(s.handleRegistryTags))
	mux.Handle("GET /api/registries/{id}/env", a(s.handleRegistryEnv))
	mux.Handle("GET /api/secrets", a(s.handleListSecrets))
	mux.Handle("POST /api/secrets", a(s.handleCreateSecret))
	mux.Handle("POST /api/secrets/{id}/delete", a(s.handleDeleteSecret))
	mux.Handle("GET /api/openbao/status", a(s.handleOpenBaoStatus))
	mux.Handle("POST /api/openbao/config", a(s.handleSetOpenBaoConfig))
	mux.Handle("GET /api/trusted-cas", a(s.handleListTrustedCAs))
	mux.Handle("POST /api/trusted-cas", a(s.handleCreateTrustedCA))
	mux.Handle("POST /api/trusted-cas/{id}/update", a(s.handleUpdateTrustedCA))
	mux.Handle("POST /api/trusted-cas/{id}/delete", a(s.handleDeleteTrustedCA))
	mux.Handle("POST /api/admin/compact", a(s.handleAdminCompact))
	mux.Handle("GET /api/export", a(s.handleExport))
	mux.Handle("POST /api/import", a(s.handleImport))
	mux.Handle("GET /api/docs/{name}", a(s.handleDocs))
	mux.Handle("GET /api/networks", a(s.handleListNetworks))
	mux.Handle("GET /api/networks/{name}/inspect", a(s.handleInspectNetwork))
	mux.Handle("GET /api/volumes", a(s.handleListVolumes))
	mux.Handle("GET /api/volumes/{name}/inspect", a(s.handleInspectVolume))
	mux.Handle("GET /api/system/services", a(s.handleSystemServices))
	mux.Handle("POST /api/system/services/{name}/start", a(s.handleSystemServiceStart))
	mux.Handle("POST /api/system/services/{name}/stop", a(s.handleSystemServiceStop))
	mux.Handle("GET /api/system/changelog", a(s.handleChangelog))

	// SPA: serve embedded dist/ with index.html fallback for client-side routing.
	sub, _ := fs.Sub(distFS, "dist")
	mux.Handle("/", spaHandler{fs: http.FS(sub), prefix: s.prefix})

	if s.prefix == "" {
		return mux
	}

	// Mount the inner mux under the prefix and redirect bare prefix → prefix/.
	outer := http.NewServeMux()
	outer.Handle(s.prefix+"/", http.StripPrefix(s.prefix, mux))
	outer.HandleFunc(s.prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.prefix+"/", http.StatusMovedPermanently)
	})
	return outer
}

// auth wraps a handler to require either a valid session token (from login) or
// the static adminToken (for CLI/programmatic access).
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		staticOK := s.adminToken != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(s.adminToken)) == 1
		if !staticOK && !s.isValidSession(provided) {
			writeError(w, http.StatusUnauthorized, "invalid or missing token")
			return
		}
		next(w, r)
	})
}

// ── Auth handlers ─────────────────────────────────────────────────────────────

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	state := s.peer.State()

	if hasActiveUsers(state.Users) {
		// User-store mode: bootstrap admin is fully bypassed.
		// Authenticate registered AD users via LDAP bind.
		u, found := findUserByUsername(state.Users, req.Username)
		if !found || !u.Enabled {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if s.ldap.Addr == "" {
			writeError(w, http.StatusServiceUnavailable, "LDAP not configured")
			return
		}
		if err := ldapAuthenticate(s.ldap, req.Username, req.Password); err != nil {
			slog.Warn("LDAP auth failed", "user", req.Username, "err", err)
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if s.disableMFA {
			writeJSON(w, map[string]string{"token": s.newSession()})
			return
		}
		if !u.MFAEnabled {
			key, err := totp.Generate(totp.GenerateOpts{
				Issuer:      "Orchestrator",
				AccountName: u.Username,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "mfa setup error")
				return
			}
			pt := s.newPendingToken(pendingMFA{
				username: u.Username,
				isSetup:  true,
				secret:   key.Secret(),
				expiry:   time.Now().Add(pendingTTL),
			})
			writeJSON(w, map[string]any{
				"status":        "mfa_setup",
				"pending_token": pt,
				"secret":        key.Secret(),
				"qr_uri":        key.URL(),
			})
			return
		}
		pt := s.newPendingToken(pendingMFA{
			username: u.Username,
			isSetup:  false,
			expiry:   time.Now().Add(pendingTTL),
		})
		writeJSON(w, map[string]any{
			"status":        "mfa_required",
			"pending_token": pt,
		})
		return
	}

	// Bootstrap mode: no enabled users exist — only local admin account is active.
	if s.webPassword == "" {
		writeError(w, http.StatusServiceUnavailable, "web console login is not configured")
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte("admin"))
	passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(s.webPassword))
	if userOK != 1 || passOK != 1 {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	writeJSON(w, map[string]string{"token": s.newSession()})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.revokeSession(token)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleMFA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PendingToken string `json:"pending_token"`
		Code         string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	pm, ok := s.consumePendingToken(req.PendingToken)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid or expired pending token")
		return
	}
	state := s.peer.State()
	u, found := findUserByUsername(state.Users, pm.username)
	if !found || !u.Enabled {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	secret := pm.secret
	if !pm.isSetup {
		secret = u.MFASecret
	}
	if !totp.Validate(req.Code, secret) {
		writeError(w, http.StatusUnauthorized, "invalid MFA code")
		return
	}
	if pm.isSetup {
		if !s.peer.IsLeader() {
			writeError(w, http.StatusServiceUnavailable, "not the leader")
			return
		}
		u.MFASecret = secret
		u.MFAEnabled = true
		if err := s.peer.ApplyUser(u); err != nil {
			writeError(w, http.StatusInternalServerError, "save mfa: "+err.Error())
			return
		}
	}
	writeJSON(w, map[string]string{"token": s.newSession()})
}

// ── API handlers ──────────────────────────────────────────────────────────────

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	state := s.peer.State()
	leaderID := s.peer.LeaderID()
	isLeader := s.peer.IsLeader()

	leaderAddr := ""
	if !isLeader {
		if n, ok := state.Nodes[leaderID]; ok {
			leaderAddr = n.Address
		}
	}

	type nodeJSON struct {
		ID             string `json:"id"`
		Address        string `json:"address"`
		Status         string `json:"status"`
		CPUCores       uint32 `json:"cpu_cores"`
		MemoryBytes    uint64 `json:"memory_bytes"`
		DiskBytes      uint64 `json:"disk_bytes"`
		DataIP         string `json:"data_ip"`
		LastSeenAt     int64  `json:"last_seen_at"`
		MemTotalBytes  uint64 `json:"mem_total_bytes"`
		MemUsedBytes   uint64 `json:"mem_used_bytes"`
		DiskTotalBytes uint64 `json:"disk_total_bytes"`
		DiskUsedBytes  uint64 `json:"disk_used_bytes"`
	}
	type containerStatsJSON struct {
		WorkloadID    string  `json:"workload_id"`
		ContainerID   string  `json:"container_id"`
		Name          string  `json:"name"`
		CPUPercent    float64 `json:"cpu_percent"`
		MemUsedBytes  uint64  `json:"mem_used_bytes"`
		MemLimitBytes uint64  `json:"mem_limit_bytes"`
	}
	type portAllocJSON struct {
		ContainerPort uint32 `json:"container_port"`
		AllocatedPort uint32 `json:"allocated_port"`
		Protocol      string `json:"protocol"`
	}
	type workloadJSON struct {
		ID              string          `json:"id"`
		NodeID          string          `json:"node_id"`
		Phase           string          `json:"phase"`
		Kind            string          `json:"kind"`
		Name            string          `json:"name"`
		CreatedAt       int64           `json:"created_at"`
		PortAllocations []portAllocJSON `json:"port_allocations"`
	}
	type actualContainerJSON struct {
		WorkloadID  string `json:"workload_id"`
		ContainerID string `json:"container_id"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		StartedAt   int64  `json:"started_at"` // unix seconds; 0 when not running
	}
	type actualStackJSON struct {
		WorkloadID string               `json:"workload_id"`
		Name       string               `json:"name"`
		Services   []actualContainerJSON `json:"services"`
	}
	startedAt := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.Unix()
	}
	type resp struct {
		LeaderID         string               `json:"leader_id"`
		IsLeader         bool                 `json:"is_leader"`
		LeaderAddr       string               `json:"leader_addr,omitempty"`
		Nodes            []nodeJSON           `json:"nodes"`
		Workloads        []workloadJSON       `json:"workloads"`
		ActualContainers []actualContainerJSON `json:"actual_containers"`
		ActualStacks     []actualStackJSON    `json:"actual_stacks"`
		ContainerStats   []containerStatsJSON `json:"container_stats"`
		Registries       []registryJSON       `json:"registries"`
		Secrets          []secretJSON         `json:"secrets"`
		TrustedCAs       []trustedCAJSON      `json:"trusted_cas"`
	}

	allStates := s.agent.AllStates()

	out := resp{
		LeaderID:         leaderID,
		IsLeader:         isLeader,
		LeaderAddr:       leaderAddr,
		Nodes:            []nodeJSON{},
		Workloads:        []workloadJSON{},
		ActualContainers: []actualContainerJSON{},
		ActualStacks:     []actualStackJSON{},
		ContainerStats:   []containerStatsJSON{},
		Registries:       []registryJSON{},
		Secrets:          []secretJSON{},
		TrustedCAs:       []trustedCAJSON{},
	}

	for _, n := range state.Nodes {
		nm := allStates[n.ID].Metrics
		out.Nodes = append(out.Nodes, nodeJSON{
			ID:             n.ID,
			Address:        n.Address,
			Status:         nodeStatusString(n.Status),
			CPUCores:       n.Resources.CPUCores,
			MemoryBytes:    n.Resources.MemoryBytes,
			DiskBytes:      n.Resources.DiskBytes,
			DataIP:         n.DataIP,
			LastSeenAt:     startedAt(n.LastSeenAt),
			MemTotalBytes:  nm.MemTotalBytes,
			MemUsedBytes:   nm.MemUsedBytes,
			DiskTotalBytes: nm.DiskTotalBytes,
			DiskUsedBytes:  nm.DiskUsedBytes,
		})
	}
	for _, wl := range state.Workloads {
		wj := workloadJSON{
			ID:              wl.ID,
			NodeID:          wl.NodeID,
			Phase:           wl.Phase.String(),
			Kind:            kindString(wl.Kind),
			Name:            workloadName(wl),
			CreatedAt:       wl.CreatedAt.Unix(),
			PortAllocations: []portAllocJSON{},
		}
		for _, pa := range wl.PortAllocations {
			wj.PortAllocations = append(wj.PortAllocations, portAllocJSON{
				ContainerPort: pa.ContainerPort,
				AllocatedPort: pa.AllocatedPort,
				Protocol:      pa.Protocol,
			})
		}
		out.Workloads = append(out.Workloads, wj)
	}

	for _, ns := range allStates {
		for _, c := range ns.Containers {
			out.ActualContainers = append(out.ActualContainers, actualContainerJSON{
				WorkloadID:  c.WorkloadID,
				ContainerID: c.ContainerID,
				Name:        c.Name,
				Status:      c.Status,
				StartedAt:   startedAt(c.StartedAt),
			})
		}
		for _, st := range ns.Stacks {
			svcs := make([]actualContainerJSON, 0, len(st.Services))
			for _, svc := range st.Services {
				svcs = append(svcs, actualContainerJSON{
					WorkloadID:  svc.WorkloadID,
					ContainerID: svc.ContainerID,
					Name:        svc.Name,
					Status:      svc.Status,
					StartedAt:   startedAt(svc.StartedAt),
				})
			}
			sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
			out.ActualStacks = append(out.ActualStacks, actualStackJSON{
				WorkloadID: st.WorkloadID,
				Name:       st.Name,
				Services:   svcs,
			})
		}
		for _, cs := range ns.ContainerStats {
			out.ContainerStats = append(out.ContainerStats, containerStatsJSON{
				WorkloadID:    cs.WorkloadID,
				ContainerID:   cs.ContainerID,
				Name:          cs.Name,
				CPUPercent:    cs.CPUPercent,
				MemUsedBytes:  cs.MemUsedBytes,
				MemLimitBytes: cs.MemLimitBytes,
			})
		}
	}

	for _, r := range state.Registries {
		out.Registries = append(out.Registries, registryJSON{
			ID:        r.ID,
			Name:      r.Name,
			URL:       r.URL,
			Username:  r.Username,
			CreatedAt: r.CreatedAt.Unix(),
		})
	}

	for _, sec := range state.Secrets {
		out.Secrets = append(out.Secrets, secretJSON{ID: sec.ID, Name: sec.Name, CreatedAt: sec.CreatedAt.Unix()})
	}

	for _, ca := range state.TrustedCAs {
		out.TrustedCAs = append(out.TrustedCAs, trustedCAToJSON(ca))
	}

	// The slices above are built by ranging over Go maps, whose iteration
	// order is randomized per request. Sort by a stable key so the web UI
	// (which polls every few seconds) doesn't reshuffle rows on each refresh.
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	sort.Slice(out.Workloads, func(i, j int) bool { return out.Workloads[i].ID < out.Workloads[j].ID })
	sort.Slice(out.ActualContainers, func(i, j int) bool { return out.ActualContainers[i].Name < out.ActualContainers[j].Name })
	sort.Slice(out.ActualStacks, func(i, j int) bool { return out.ActualStacks[i].Name < out.ActualStacks[j].Name })
	sort.Slice(out.ContainerStats, func(i, j int) bool { return out.ContainerStats[i].Name < out.ContainerStats[j].Name })
	sort.Slice(out.Registries, func(i, j int) bool { return out.Registries[i].Name < out.Registries[j].Name })
	sort.Slice(out.Secrets, func(i, j int) bool { return out.Secrets[i].Name < out.Secrets[j].Name })
	sort.Slice(out.TrustedCAs, func(i, j int) bool { return out.TrustedCAs[i].Label < out.TrustedCAs[j].Label })

	writeJSON(w, out)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string   `json:"name"`
		Image   string   `json:"image"`
		Command []string `json:"command"`
		Env     []string `json:"env"`
		Ports   []string `json:"ports"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var ports []*gen.PortMapping
	for _, p := range req.Ports {
		pm, err := parsePort(p)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		ports = append(ports, pm)
	}

	resp, err := s.ctrl.SubmitContainer(r.Context(), &gen.SubmitContainerRequest{
		Spec: &gen.ContainerSpec{
			Name:    req.Name,
			Image:   req.Image,
			Command: req.Command,
			Env:     req.Env,
			Ports:   ports,
		},
	})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleStack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		ComposeYAML string `json:"compose_yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resp, err := s.ctrl.SubmitStack(r.Context(), &gen.SubmitStackRequest{
		Spec: &gen.ComposeStackSpec{Name: req.Name, ComposeYaml: req.ComposeYAML},
	})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.ctrl.RemoveWorkload(r.Context(), &gen.RemoveWorkloadRequest{WorkloadId: id})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleGetWorkload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.peer.State()
	wl, ok := state.Workloads[id]
	if !ok {
		writeError(w, http.StatusNotFound, "workload not found")
		return
	}

	type portSpecJSON struct {
		ContainerPort uint32 `json:"container_port"`
		Protocol      string `json:"protocol"`
	}
	type volumeSpecJSON struct {
		Source   string `json:"source"`
		Target   string `json:"target"`
		ReadOnly bool   `json:"read_only"`
	}
	type workloadSpecJSON struct {
		ID          string            `json:"id"`
		Kind        string            `json:"kind"`
		Name        string            `json:"name"`
		Image       string            `json:"image,omitempty"`
		Command     []string          `json:"command,omitempty"`
		Env         []string          `json:"env,omitempty"`
		Ports       []portSpecJSON    `json:"ports,omitempty"`
		Volumes     []volumeSpecJSON  `json:"volumes,omitempty"`
		Labels      map[string]string `json:"labels,omitempty"`
		Namespace   string            `json:"namespace,omitempty"`
		ComposeYAML string            `json:"compose_yaml,omitempty"`
	}

	out := workloadSpecJSON{
		ID:   wl.ID,
		Kind: kindString(wl.Kind),
		Name: workloadName(wl),
	}
	if wl.Container != nil {
		c := wl.Container
		out.Image = c.Image
		out.Command = c.Command
		out.Env = c.Env
		out.Labels = c.Labels
		out.Namespace = c.Namespace
		for _, p := range c.Ports {
			out.Ports = append(out.Ports, portSpecJSON{ContainerPort: p.ContainerPort, Protocol: p.Protocol})
		}
		for _, v := range c.Volumes {
			out.Volumes = append(out.Volumes, volumeSpecJSON{Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly})
		}
	}
	if wl.Stack != nil {
		out.ComposeYAML = wl.Stack.ComposeYAML
	}
	writeJSON(w, out)
}

func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.ctrl.DrainNode(r.Context(), &gen.DrainNodeRequest{NodeId: id})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, resp)
}

// ── Ingress API handlers ──────────────────────────────────────────────────────

type ingressRuleJSON struct {
	ID            string `json:"id"`
	DomainID      string `json:"domain_id"`
	Host          string `json:"host"`
	PathPrefix    string `json:"path_prefix"`
	StripPrefix   bool   `json:"strip_prefix"`
	ContainerFQDN string `json:"container_fqdn"`
	ContainerPort uint32 `json:"container_port"`
	SystemPort    uint32 `json:"system_port"`
	CreatedAt     int64  `json:"created_at"`
}

func (s *Server) handleListIngress(w http.ResponseWriter, _ *http.Request) {
	state := s.peer.State()
	rules := make([]ingressRuleJSON, 0, len(state.IngressRules))
	for _, r := range state.IngressRules {
		rules = append(rules, ingressRuleJSON{
			ID:            r.ID,
			DomainID:      r.DomainID,
			Host:          r.Host,
			PathPrefix:    r.PathPrefix,
			StripPrefix:   r.StripPrefix,
			ContainerFQDN: r.ContainerFQDN,
			ContainerPort: r.ContainerPort,
			SystemPort:    r.SystemPort,
			CreatedAt:     r.CreatedAt.Unix(),
		})
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Host != rules[j].Host {
			return rules[i].Host < rules[j].Host
		}
		if rules[i].PathPrefix != rules[j].PathPrefix {
			return rules[i].PathPrefix < rules[j].PathPrefix
		}
		return rules[i].ID < rules[j].ID
	})
	writeJSON(w, rules)
}

func (s *Server) handleCreateIngress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainID      string `json:"domain_id"`
		PathPrefix    string `json:"path_prefix"`
		StripPrefix   bool   `json:"strip_prefix"`
		ContainerFQDN string `json:"container_fqdn"`
		ContainerPort uint32 `json:"container_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DomainID == "" {
		writeError(w, http.StatusBadRequest, "domain_id is required")
		return
	}
	if req.ContainerFQDN == "" {
		writeError(w, http.StatusBadRequest, "container_fqdn is required")
		return
	}
	if req.ContainerPort == 0 {
		writeError(w, http.StatusBadRequest, "container_port is required")
		return
	}
	if msg := s.checkContainerPort(req.ContainerFQDN, req.ContainerPort); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if !s.peer.IsLeader() {
		writeError(w, http.StatusServiceUnavailable, "not the leader")
		return
	}
	state := s.peer.State()
	domain, ok := state.Domains[req.DomainID]
	if !ok {
		writeError(w, http.StatusBadRequest, "domain not found")
		return
	}
	rule := types.IngressRule{
		ID:            newIngressID(),
		DomainID:      domain.ID,
		Host:          domain.Name,
		PathPrefix:    req.PathPrefix,
		StripPrefix:   req.StripPrefix,
		ContainerFQDN: req.ContainerFQDN,
		ContainerPort: req.ContainerPort,
		CreatedAt:     time.Now(),
	}
	if err := s.peer.ApplyIngress(rule); err != nil {
		writeError(w, http.StatusInternalServerError, "apply ingress: "+err.Error())
		return
	}
	committed := s.peer.State().IngressRules[rule.ID]
	writeJSON(w, map[string]any{"rule_id": rule.ID, "system_port": committed.SystemPort, "accepted": true})
}

func newIngressID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (s *Server) handleDeleteIngress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.ctrl.DeleteIngress(r.Context(), &gen.DeleteIngressRequest{RuleId: id})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleUpdateIngress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		DomainID      string `json:"domain_id"`
		PathPrefix    string `json:"path_prefix"`
		StripPrefix   bool   `json:"strip_prefix"`
		ContainerFQDN string `json:"container_fqdn"`
		ContainerPort uint32 `json:"container_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DomainID == "" || req.ContainerFQDN == "" || req.ContainerPort == 0 {
		writeError(w, http.StatusBadRequest, "domain_id, container_fqdn, and container_port are required")
		return
	}
	if !s.peer.IsLeader() {
		writeError(w, http.StatusServiceUnavailable, "not the leader")
		return
	}
	state := s.peer.State()
	existing, ok := state.IngressRules[id]
	if !ok {
		writeError(w, http.StatusNotFound, "ingress rule not found")
		return
	}
	domain, ok := state.Domains[req.DomainID]
	if !ok {
		writeError(w, http.StatusBadRequest, "domain not found")
		return
	}
	if msg := s.checkContainerPort(req.ContainerFQDN, req.ContainerPort); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	updated := types.IngressRule{
		ID:            existing.ID,
		DomainID:      domain.ID,
		Host:          domain.Name,
		PathPrefix:    req.PathPrefix,
		StripPrefix:   req.StripPrefix,
		ContainerFQDN: req.ContainerFQDN,
		ContainerPort: req.ContainerPort,
		SystemPort:    existing.SystemPort,
		CreatedAt:     existing.CreatedAt,
	}
	if err := s.peer.ApplyIngress(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "apply ingress: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"id":             updated.ID,
		"domain_id":      updated.DomainID,
		"host":           updated.Host,
		"path_prefix":    updated.PathPrefix,
		"strip_prefix":   updated.StripPrefix,
		"container_fqdn": updated.ContainerFQDN,
		"container_port": updated.ContainerPort,
		"system_port":    updated.SystemPort,
		"created_at":     updated.CreatedAt.Unix(),
	})
}

// ── Services API handlers ─────────────────────────────────────────────────────

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	resp, err := s.ctrl.ListService(r.Context(), &gen.ListServiceRequest{})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	type svcJSON struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ContainerFQDN string `json:"container_fqdn"`
		ContainerPort uint32 `json:"container_port"`
		SystemPort    uint32 `json:"system_port"`
		CreatedAt     int64  `json:"created_at"`
	}
	svcs := make([]svcJSON, 0, len(resp.Services))
	for _, s := range resp.Services {
		svcs = append(svcs, svcJSON{
			ID:            s.Id,
			Name:          s.Name,
			ContainerFQDN: s.ContainerFqdn,
			ContainerPort: s.ContainerPort,
			SystemPort:    s.SystemPort,
			CreatedAt:     s.CreatedAt,
		})
	}
	sort.Slice(svcs, func(i, j int) bool {
		if svcs[i].Name != svcs[j].Name {
			return svcs[i].Name < svcs[j].Name
		}
		return svcs[i].ID < svcs[j].ID
	})
	writeJSON(w, svcs)
}

// checkContainerPort returns a non-empty error string when no host port has
// been allocated for containerPort on the workload identified by containerFQDN.
// Returns "" when a valid allocation is found. Callers must reject the request
// on a non-empty return — without an AllocatedPort, proxyd has nothing to route to.
func (s *Server) checkContainerPort(containerFQDN string, containerPort uint32) string {
	workloadName := containerFQDN
	if i := strings.LastIndex(containerFQDN, "."); i >= 0 {
		workloadName = containerFQDN[i+1:]
	}
	state := s.peer.State()
	for _, wl := range state.Workloads {
		if wl.Name() != workloadName {
			continue
		}
		for _, pa := range wl.PortAllocations {
			if pa.ContainerPort == containerPort && pa.AllocatedPort != 0 {
				return ""
			}
		}
		return fmt.Sprintf(
			"no host port allocated for container port %d on workload %q — "+
				"add a ports entry (e.g. ports: [\"%d\"]) to the workload definition and redeploy",
			containerPort, workloadName, containerPort)
	}
	return fmt.Sprintf("workload %q not found — deploy the workload before creating a service", workloadName)
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"name"`
		ContainerFQDN string `json:"container_fqdn"`
		ContainerPort uint32 `json:"container_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := s.checkContainerPort(req.ContainerFQDN, req.ContainerPort); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	resp, err := s.ctrl.CreateService(r.Context(), &gen.CreateServiceRequest{
		Name:          req.Name,
		ContainerFqdn: req.ContainerFQDN,
		ContainerPort: req.ContainerPort,
	})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, map[string]any{
		"service_id":  resp.ServiceId,
		"system_port": resp.SystemPort,
		"accepted":    resp.Accepted,
		"reason":      resp.Reason,
	})
}

func (s *Server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name          string `json:"name"`
		ContainerFQDN string `json:"container_fqdn"`
		ContainerPort uint32 `json:"container_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.ContainerFQDN == "" || req.ContainerPort == 0 {
		writeError(w, http.StatusBadRequest, "name, container_fqdn, and container_port are required")
		return
	}
	state := s.peer.State()
	existing, ok := state.Services[id]
	if !ok {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}
	updated := types.Service{
		ID:            existing.ID,
		Name:          req.Name,
		ContainerFQDN: req.ContainerFQDN,
		ContainerPort: req.ContainerPort,
		SystemPort:    existing.SystemPort,
		CreatedAt:     existing.CreatedAt,
	}
	if msg := s.checkContainerPort(req.ContainerFQDN, req.ContainerPort); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.peer.ApplyService(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "apply service: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"id":             updated.ID,
		"name":           updated.Name,
		"container_fqdn": updated.ContainerFQDN,
		"container_port": updated.ContainerPort,
		"system_port":    updated.SystemPort,
		"created_at":     updated.CreatedAt.Unix(),
	})
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.ctrl.DeleteService(r.Context(), &gen.DeleteServiceRequest{ServiceId: id})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, resp)
}

// ── Docs handler ─────────────────────────────────────────────────────────────

var allowedDocs = map[string]string{
	"deploy":     "deploy.md",
	"production": "production.md",
	"changelog":  "CHANGELOG.md",
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	filename, ok := allowedDocs[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := docs.FS.ReadFile(filename)
	if err != nil {
		http.Error(w, "doc not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write(data)
}

// ── SPA handler ───────────────────────────────────────────────────────────────

// spaHandler serves static files from an http.FileSystem, falling back to
// index.html for any path that doesn't resolve to an existing file.
// When serving index.html it injects <meta name="base-path"> so the frontend
// knows the URL prefix it was mounted under.
type spaHandler struct {
	fs     http.FileSystem
	prefix string // e.g. "/console" or ""
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	serveIndex := path == "/" || path == "/index.html"
	if !serveIndex {
		f, err := h.fs.Open(path)
		if err != nil {
			serveIndex = true
		} else {
			f.Close()
		}
	}
	if serveIndex {
		h.serveIndex(w)
		return
	}
	http.FileServer(h.fs).ServeHTTP(w, r)
}

func (h spaHandler) serveIndex(w http.ResponseWriter) {
	f, err := h.fs.Open("/index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	// Replace the placeholder meta tag with the actual prefix value so the
	// frontend JavaScript can construct absolute API URLs at runtime.
	data = bytes.ReplaceAll(data,
		[]byte(`<meta name="base-path" content="" />`),
		[]byte(`<meta name="base-path" content="`+h.prefix+`" />`),
	)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// ── Networks ──────────────────────────────────────────────────────────────────

func (s *Server) handleListNetworks(w http.ResponseWriter, r *http.Request) {
	networks, err := s.agent.ListNetworks(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "list networks: "+err.Error())
		return
	}
	type networkJSON struct {
		NetworkID string `json:"network_id"`
		Name      string `json:"name"`
		Driver    string `json:"driver"`
		IPv4      string `json:"ipv4"`
		Labels    string `json:"labels"`
	}
	out := make([]networkJSON, 0, len(networks))
	for _, n := range networks {
		out = append(out, networkJSON{
			NetworkID: n.NetworkID,
			Name:      n.Name,
			Driver:    n.Driver,
			IPv4:      n.IPv4,
			Labels:    n.Labels,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, out)
}

func (s *Server) handleInspectNetwork(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	detail, err := s.agent.InspectNetwork(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "inspect network: "+err.Error())
		return
	}
	type containerOnNet struct {
		Name        string `json:"name"`
		IPv4Address string `json:"ipv4_address"`
	}
	type networkDetailJSON struct {
		Name       string            `json:"name"`
		ID         string            `json:"id"`
		Driver     string            `json:"driver"`
		Subnet     string            `json:"subnet"`
		Gateway    string            `json:"gateway"`
		Containers []containerOnNet  `json:"containers"`
		Labels     map[string]string `json:"labels"`
	}
	out := networkDetailJSON{
		Name:       detail.Name,
		ID:         detail.ID,
		Driver:     detail.Driver,
		Containers: []containerOnNet{},
		Labels:     detail.Labels,
	}
	if len(detail.IPAM.Config) > 0 {
		out.Subnet = detail.IPAM.Config[0].Subnet
		out.Gateway = detail.IPAM.Config[0].Gateway
	}
	for _, c := range detail.Containers {
		out.Containers = append(out.Containers, containerOnNet{
			Name:        c.Name,
			IPv4Address: c.IPv4Address,
		})
	}
	sort.Slice(out.Containers, func(i, j int) bool { return out.Containers[i].Name < out.Containers[j].Name })
	writeJSON(w, out)
}

// ── Volumes ───────────────────────────────────────────────────────────────────

func (s *Server) handleListVolumes(w http.ResponseWriter, r *http.Request) {
	volumes, err := s.agent.ListVolumes(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "list volumes: "+err.Error())
		return
	}
	type volumeJSON struct {
		Name       string `json:"name"`
		Driver     string `json:"driver"`
		Mountpoint string `json:"mountpoint"`
		Labels     string `json:"labels"`
	}
	out := make([]volumeJSON, 0, len(volumes))
	for _, v := range volumes {
		out = append(out, volumeJSON{
			Name:       v.Name,
			Driver:     v.Driver,
			Mountpoint: v.Mountpoint,
			Labels:     v.Labels,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, out)
}

func (s *Server) handleInspectVolume(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	detail, err := s.agent.InspectVolume(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "inspect volume: "+err.Error())
		return
	}
	type volumeDetailJSON struct {
		Name       string            `json:"name"`
		Driver     string            `json:"driver"`
		Mountpoint string            `json:"mountpoint"`
		Labels     map[string]string `json:"labels"`
		Scope      string            `json:"scope"`
	}
	writeJSON(w, volumeDetailJSON{
		Name:       detail.Name,
		Driver:     detail.Driver,
		Mountpoint: detail.Mountpoint,
		Labels:     detail.Labels,
		Scope:      detail.Scope,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func grpcHTTPStatus(c codes.Code) int {
	switch c {
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.NotFound:
		return http.StatusNotFound
	case codes.FailedPrecondition:
		return http.StatusConflict
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func parsePort(s string) (*gen.PortMapping, error) {
	proto := "tcp"
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		proto = s[idx+1:]
		s = s[:idx]
	}
	parts := strings.SplitN(s, ":", 2)
	// Accept both CONTAINER and HOST:CONTAINER; host port is always ignored.
	ctrStr := parts[len(parts)-1]
	cont, err := strconv.ParseUint(ctrStr, 10, 32)
	if err != nil {
		return nil, &portError{s}
	}
	return &gen.PortMapping{
		ContainerPort: uint32(cont),
		Protocol:      proto,
	}, nil
}

type portError struct{ raw string }

func (e *portError) Error() string {
	return "invalid port mapping " + strconv.Quote(e.raw) + ": expected HOST:CONTAINER[/proto]"
}

func nodeStatusString(s types.NodeStatus) string {
	switch s {
	case types.NodeHealthy:
		return "healthy"
	case types.NodeDraining:
		return "draining"
	case types.NodeUnreachable:
		return "unreachable"
	default:
		return "unknown"
	}
}

func kindString(k types.WorkloadKind) string {
	if k == types.KindStack {
		return "stack"
	}
	return "container"
}

// ── Domains API handlers ──────────────────────────────────────────────────────

// domainJSON is the public representation of a Domain — the private key is
// intentionally omitted and never returned after the initial create response.
type domainJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TLSCert   string `json:"tls_cert"`
	CSR       string `json:"csr"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// domainWithKeyJSON is returned only once, at creation or key regeneration,
// so the operator can copy the private key. Subsequent list reads never include it.
type domainWithKeyJSON struct {
	domainJSON
	TLSKey string `json:"tls_key"`
}

func domainToJSON(d types.Domain) domainJSON {
	return domainJSON{
		ID:        d.ID,
		Name:      d.Name,
		TLSCert:   d.TLSCert,
		CSR:       d.CSR,
		Enabled:   d.Enabled,
		CreatedAt: d.CreatedAt.Unix(),
	}
}

func (s *Server) handleListDomains(w http.ResponseWriter, _ *http.Request) {
	state := s.peer.State()
	out := make([]domainJSON, 0, len(state.Domains))
	for _, d := range state.Domains {
		out = append(out, domainToJSON(d))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, out)
}

func (s *Server) handleCreateDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		TLSCert string `json:"tls_cert"`
		TLSKey  string `json:"tls_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	// Pre-flight uniqueness check.
	for _, d := range s.peer.State().Domains {
		if d.Name == req.Name {
			writeError(w, http.StatusConflict, "domain name already exists")
			return
		}
	}

	// Auto-generate self-signed cert when either field is missing.
	if req.TLSCert == "" || req.TLSKey == "" {
		cert, key, err := generateSelfSignedCert(req.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "generate cert: "+err.Error())
			return
		}
		req.TLSCert = cert
		req.TLSKey = key
	}

	csr, err := generateCSR(req.Name, req.TLSKey, csrSubject{})
	if err != nil {
		slog.Warn("domain CSR generation failed", "name", req.Name, "err", err)
	}

	d := types.Domain{
		ID:        newDomainID(),
		Name:      req.Name,
		TLSCert:   req.TLSCert,
		TLSKey:    req.TLSKey,
		CSR:       csr,
		Enabled:   true,
		CreatedAt: time.Now(),
	}
	if err := s.peer.ApplyDomain(d); err != nil {
		writeError(w, http.StatusInternalServerError, "apply domain: "+err.Error())
		return
	}
	writeJSON(w, domainWithKeyJSON{
		domainJSON: domainToJSON(d),
		TLSKey:     d.TLSKey,
	})
}

func (s *Server) handleUpdateDomain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name    string `json:"name"`
		TLSCert string `json:"tls_cert"`
		TLSKey  string `json:"tls_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	state := s.peer.State()
	existing, ok := state.Domains[id]
	if !ok {
		writeError(w, http.StatusNotFound, "domain not found")
		return
	}

	// If name changed, enforce uniqueness.
	newName := existing.Name
	if req.Name != "" {
		newName = req.Name
	}
	if newName != existing.Name {
		for _, d := range state.Domains {
			if d.Name == newName && d.ID != id {
				writeError(w, http.StatusConflict, "domain name already exists")
				return
			}
		}
	}

	newCert := req.TLSCert
	newKey := req.TLSKey
	newCSR := existing.CSR

	// If both cert and key are left empty, regenerate all three.
	if newCert == "" && newKey == "" {
		cert, key, err := generateSelfSignedCert(newName)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "generate cert: "+err.Error())
			return
		}
		newCert = cert
		newKey = key
		if csr, err := generateCSR(newName, newKey, csrSubject{}); err == nil {
			newCSR = csr
		} else {
			slog.Warn("domain CSR generation failed", "name", newName, "err", err)
		}
	} else if newCert == "" {
		newCert = existing.TLSCert
	} else if newKey == "" {
		newKey = existing.TLSKey
	} else {
		// Both provided — regenerate CSR from the new key.
		if csr, err := generateCSR(newName, newKey, csrSubject{}); err == nil {
			newCSR = csr
		} else {
			slog.Warn("domain CSR generation failed", "name", newName, "err", err)
		}
	}

	updated := types.Domain{
		ID:        existing.ID,
		Name:      newName,
		TLSCert:   newCert,
		TLSKey:    newKey,
		CSR:       newCSR,
		Enabled:   existing.Enabled,
		CreatedAt: existing.CreatedAt,
	}
	if err := s.peer.ApplyDomain(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "apply domain: "+err.Error())
		return
	}
	writeJSON(w, domainToJSON(updated))
}

func (s *Server) handleToggleDomain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.peer.State()
	existing, ok := state.Domains[id]
	if !ok {
		writeError(w, http.StatusNotFound, "domain not found")
		return
	}
	existing.Enabled = !existing.Enabled
	if err := s.peer.ApplyDomain(existing); err != nil {
		writeError(w, http.StatusInternalServerError, "apply domain: "+err.Error())
		return
	}
	writeJSON(w, domainToJSON(existing))
}

func (s *Server) handleDeleteDomain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.peer.RemoveDomain(id); err != nil {
		writeError(w, http.StatusInternalServerError, "remove domain: "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

func (s *Server) handleRegenerateDomainKeys(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.peer.State()
	existing, ok := state.Domains[id]
	if !ok {
		writeError(w, http.StatusNotFound, "domain not found")
		return
	}
	var csrDetails csrSubject
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&csrDetails); err != nil {
			writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
	}
	cert, key, err := generateSelfSignedCert(existing.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate cert: "+err.Error())
		return
	}
	csr, err := generateCSR(existing.Name, key, csrDetails)
	if err != nil {
		slog.Warn("domain CSR generation failed", "name", existing.Name, "err", err)
	}
	updated := types.Domain{
		ID:        existing.ID,
		Name:      existing.Name,
		TLSCert:   cert,
		TLSKey:    key,
		CSR:       csr,
		Enabled:   existing.Enabled,
		CreatedAt: existing.CreatedAt,
	}
	if err := s.peer.ApplyDomain(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "apply domain: "+err.Error())
		return
	}
	writeJSON(w, domainWithKeyJSON{
		domainJSON: domainToJSON(updated),
		TLSKey:     updated.TLSKey,
	})
}

func (s *Server) handleImportDomainCert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		TLSCert string `json:"tls_cert"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TLSCert == "" {
		writeError(w, http.StatusBadRequest, "tls_cert is required")
		return
	}
	state := s.peer.State()
	existing, ok := state.Domains[id]
	if !ok {
		writeError(w, http.StatusNotFound, "domain not found")
		return
	}
	// Verify the new cert pairs with the stored key.
	if _, err := tls.X509KeyPair([]byte(req.TLSCert), []byte(existing.TLSKey)); err != nil {
		writeError(w, http.StatusBadRequest, "certificate does not match stored private key: "+err.Error())
		return
	}
	updated := existing
	updated.TLSCert = req.TLSCert
	if err := s.peer.ApplyDomain(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "apply domain: "+err.Error())
		return
	}
	writeJSON(w, domainToJSON(updated))
}

// ── Users API handlers ────────────────────────────────────────────────────────

type userJSON struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Enabled    bool   `json:"enabled"`
	MFAEnabled bool   `json:"mfa_enabled"`
	CreatedAt  int64  `json:"created_at"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, _ *http.Request) {
	state := s.peer.State()
	out := make([]userJSON, 0, len(state.Users))
	for _, u := range state.Users {
		out = append(out, userJSON{
			ID:         u.ID,
			Username:   u.Username,
			Enabled:    u.Enabled,
			MFAEnabled: u.MFAEnabled,
			CreatedAt:  u.CreatedAt.Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Username != out[j].Username {
			return out[i].Username < out[j].Username
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if !s.peer.IsLeader() {
		writeError(w, http.StatusServiceUnavailable, "not the leader")
		return
	}
	if _, found := findUserByUsername(s.peer.State().Users, req.Username); found {
		writeError(w, http.StatusConflict, "username already exists")
		return
	}
	u := types.User{
		ID:        newUserID(),
		Username:  req.Username,
		Enabled:   true,
		CreatedAt: time.Now(),
	}
	if err := s.peer.ApplyUser(u); err != nil {
		writeError(w, http.StatusInternalServerError, "apply user: "+err.Error())
		return
	}
	writeJSON(w, userJSON{
		ID:         u.ID,
		Username:   u.Username,
		Enabled:    u.Enabled,
		MFAEnabled: u.MFAEnabled,
		CreatedAt:  u.CreatedAt.Unix(),
	})
}

func (s *Server) handleToggleUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.peer.State()
	existing, ok := state.Users[id]
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	existing.Enabled = !existing.Enabled
	if err := s.peer.ApplyUser(existing); err != nil {
		writeError(w, http.StatusInternalServerError, "apply user: "+err.Error())
		return
	}
	writeJSON(w, userJSON{
		ID:         existing.ID,
		Username:   existing.Username,
		Enabled:    existing.Enabled,
		MFAEnabled: existing.MFAEnabled,
		CreatedAt:  existing.CreatedAt.Unix(),
	})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.peer.RemoveUser(id); err != nil {
		writeError(w, http.StatusInternalServerError, "remove user: "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

func (s *Server) handleResetUserMFA(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.peer.State()
	u, ok := state.Users[id]
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if !s.peer.IsLeader() {
		writeError(w, http.StatusServiceUnavailable, "not the leader")
		return
	}
	u.MFASecret = ""
	u.MFAEnabled = false
	if err := s.peer.ApplyUser(u); err != nil {
		writeError(w, http.StatusInternalServerError, "reset mfa: "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ── Registry API handlers ─────────────────────────────────────────────────────

type registryJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Username  string `json:"username"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Server) handleListRegistries(w http.ResponseWriter, _ *http.Request) {
	state := s.peer.State()
	out := make([]registryJSON, 0, len(state.Registries))
	for _, r := range state.Registries {
		out = append(out, registryJSON{
			ID:        r.ID,
			Name:      r.Name,
			URL:       r.URL,
			Username:  r.Username,
			CreatedAt: r.CreatedAt.Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, out)
}

func (s *Server) handleCreateRegistry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.URL == "" {
		writeError(w, http.StatusBadRequest, "name and url are required")
		return
	}
	reg := types.Registry{
		ID:        newRegistryID(),
		Name:      req.Name,
		URL:       strings.TrimRight(req.URL, "/"),
		Username:  req.Username,
		Password:  req.Password,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.peer.ApplyRegistry(reg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, registryJSON{
		ID:        reg.ID,
		Name:      reg.Name,
		URL:       reg.URL,
		Username:  reg.Username,
		CreatedAt: reg.CreatedAt.Unix(),
	})
}

func (s *Server) handleUpdateRegistry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	state := s.peer.State()
	existing, ok := state.Registries[id]
	if !ok {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}

	updated := types.Registry{
		ID:        existing.ID,
		Name:      existing.Name,
		URL:       existing.URL,
		Username:  existing.Username,
		Password:  existing.Password,
		CreatedAt: existing.CreatedAt,
	}
	if req.Name != "" {
		updated.Name = req.Name
	}
	if req.URL != "" {
		updated.URL = strings.TrimRight(req.URL, "/")
	}
	updated.Username = req.Username
	// Only replace password if a new one is provided; empty string keeps existing.
	if req.Password != "" {
		updated.Password = req.Password
	}

	if err := s.peer.ApplyRegistry(updated); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, registryJSON{
		ID:        updated.ID,
		Name:      updated.Name,
		URL:       updated.URL,
		Username:  updated.Username,
		CreatedAt: updated.CreatedAt.Unix(),
	})
}

func (s *Server) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.peer.RemoveRegistry(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

func (s *Server) registryClient(id string) (*registry.Client, error) {
	state := s.peer.State()
	reg, ok := state.Registries[id]
	if !ok {
		return nil, fmt.Errorf("registry not found")
	}
	return registry.New(reg.URL, reg.Username, reg.Password), nil
}

func (s *Server) handleRegistryCatalog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	search := r.URL.Query().Get("search")

	client, err := s.registryClient(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	repos, err := client.ListRepos(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "list repos: "+err.Error())
		return
	}
	if search != "" {
		filtered := repos[:0]
		lower := strings.ToLower(search)
		for _, repo := range repos {
			if strings.Contains(strings.ToLower(repo), lower) {
				filtered = append(filtered, repo)
			}
		}
		repos = filtered
	}
	if repos == nil {
		repos = []string{}
	}
	writeJSON(w, map[string][]string{"repos": repos})
}

func (s *Server) handleRegistryTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		writeError(w, http.StatusBadRequest, "repo query param is required")
		return
	}
	client, err := s.registryClient(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	tags, err := client.ListTags(r.Context(), repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, "list tags: "+err.Error())
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, map[string][]string{"tags": tags})
}

func (s *Server) handleRegistryEnv(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	repo := r.URL.Query().Get("repo")
	tag := r.URL.Query().Get("tag")
	if repo == "" || tag == "" {
		writeError(w, http.StatusBadRequest, "repo and tag query params are required")
		return
	}
	client, err := s.registryClient(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	env, err := client.GetImageEnv(r.Context(), repo, tag)
	if err != nil {
		writeError(w, http.StatusBadGateway, "get env: "+err.Error())
		return
	}
	if env == nil {
		env = []string{}
	}
	writeJSON(w, map[string][]string{"env": env})
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name is required")
		return
	}
	tail := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 2000 {
				n = 2000
			}
			tail = n
		}
	}
	logs, err := s.agent.ContainerLogs(r.Context(), name, tail)
	if err != nil {
		writeError(w, http.StatusBadGateway, "get logs: "+err.Error())
		return
	}
	writeJSON(w, map[string]string{"logs": logs})
}

func (s *Server) handleInspectContainer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	detail, err := s.agent.InspectContainer(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "inspect container: "+err.Error())
		return
	}

	type portBinding struct {
		HostIP   string `json:"host_ip"`
		HostPort string `json:"host_port"`
	}
	type networkEntry struct {
		Name       string `json:"name"`
		IPAddress  string `json:"ip_address"`
		Gateway    string `json:"gateway"`
		MacAddress string `json:"mac_address"`
	}
	type mountEntry struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Source      string `json:"source"`
		Destination string `json:"destination"`
		Mode        string `json:"mode"`
		RW          bool   `json:"rw"`
	}
	type resp struct {
		ID           string                   `json:"id"`
		Name         string                   `json:"name"`
		Status       string                   `json:"status"`
		Running      bool                     `json:"running"`
		Pid          int                      `json:"pid"`
		StartedAt    string                   `json:"started_at"`
		Image        string                   `json:"image"`
		Env          []string                 `json:"env"`
		PortBindings map[string][]portBinding `json:"port_bindings"`
		Networks     []networkEntry           `json:"networks"`
		Mounts       []mountEntry             `json:"mounts"`
	}

	out := resp{
		ID:        detail.ID,
		Name:      strings.TrimPrefix(detail.Name, "/"),
		Status:    detail.State.Status,
		Running:   detail.State.Running,
		Pid:       detail.State.Pid,
		StartedAt: detail.State.StartedAt,
		Image:     detail.Config.Image,
		Env:       detail.Config.Env,
		Mounts:    []mountEntry{},
		Networks:  []networkEntry{},
	}

	out.PortBindings = make(map[string][]portBinding, len(detail.HostConfig.PortBindings))
	for proto, bindings := range detail.HostConfig.PortBindings {
		for _, b := range bindings {
			out.PortBindings[proto] = append(out.PortBindings[proto], portBinding{b.HostIP, b.HostPort})
		}
	}

	for netName, n := range detail.NetworkSettings.Networks {
		out.Networks = append(out.Networks, networkEntry{
			Name:       netName,
			IPAddress:  n.IPAddress,
			Gateway:    n.Gateway,
			MacAddress: n.MacAddress,
		})
	}

	for _, m := range detail.Mounts {
		out.Mounts = append(out.Mounts, mountEntry{
			Type:        m.Type,
			Name:        m.Name,
			Source:      m.Source,
			Destination: m.Destination,
			Mode:        m.Mode,
			RW:          m.RW,
		})
	}

	writeJSON(w, out)
}

func (s *Server) handleRestartContainer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.agent.RestartContainer(r.Context(), name); err != nil {
		writeError(w, http.StatusBadGateway, "restart container: "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func newRegistryID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return fmt.Sprintf("%x", b)
}

// ── Templates ─────────────────────────────────────────────────────────────────

type templateRequestJSON struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Kind        string   `json:"kind"` // "container" | "stack"
	ComposeYAML string   `json:"compose_yaml,omitempty"`
	Image       string   `json:"image,omitempty"`
	Command     []string `json:"command,omitempty"`
	Env         []string `json:"env,omitempty"`
	Ports       []struct {
		ContainerPort uint32 `json:"container_port"`
		Protocol      string `json:"protocol"`
	} `json:"ports,omitempty"`
	Volumes []struct {
		Source   string `json:"source"`
		Target   string `json:"target"`
		ReadOnly bool   `json:"read_only"`
	} `json:"volumes,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	Namespace        string            `json:"namespace,omitempty"`
	InsecureRegistry bool              `json:"insecure_registry,omitempty"`
}

type templateJSON struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Kind             string `json:"kind"`
	ComposeYAML      string `json:"compose_yaml,omitempty"`
	Image            string `json:"image,omitempty"`
	InsecureRegistry bool   `json:"insecure_registry,omitempty"`
	CreatedAt        int64  `json:"created_at"`
}

func templateToJSON(t types.WorkloadTemplate) templateJSON {
	out := templateJSON{
		ID:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Kind:        kindString(t.Kind),
		CreatedAt:   t.CreatedAt.Unix(),
	}
	if t.Stack != nil {
		out.ComposeYAML = t.Stack.ComposeYAML
		out.InsecureRegistry = t.Stack.InsecureRegistry
	}
	if t.Container != nil {
		out.Image = t.Container.Image
		out.InsecureRegistry = t.Container.InsecureRegistry
	}
	return out
}

func templateFromRequest(req templateRequestJSON) (types.WorkloadTemplate, error) {
	t := types.WorkloadTemplate{
		Name:        req.Name,
		Description: req.Description,
	}
	switch req.Kind {
	case "stack":
		t.Kind = types.KindStack
		t.Stack = &types.ComposeStackSpec{ComposeYAML: req.ComposeYAML, InsecureRegistry: req.InsecureRegistry}
	case "container":
		t.Kind = types.KindContainer
		spec := types.ContainerSpec{
			Image:            req.Image,
			Command:          req.Command,
			Env:              req.Env,
			Labels:           req.Labels,
			Namespace:        req.Namespace,
			InsecureRegistry: req.InsecureRegistry,
		}
		for _, p := range req.Ports {
			spec.Ports = append(spec.Ports, types.PortMapping{ContainerPort: p.ContainerPort, Protocol: p.Protocol})
		}
		for _, v := range req.Volumes {
			spec.Volumes = append(spec.Volumes, types.VolumeMount{Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly})
		}
		t.Container = &spec
	default:
		return t, fmt.Errorf("kind must be \"container\" or \"stack\"")
	}
	return t, nil
}

func (s *Server) handleListTemplates(w http.ResponseWriter, _ *http.Request) {
	templates := s.peer.State().Templates
	out := make([]templateJSON, 0, len(templates))
	for _, t := range templates {
		out = append(out, templateToJSON(t))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, out)
}

func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var req templateRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	t, err := templateFromRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t.ID = newTemplateID()
	t.CreatedAt = time.Now()
	if err := s.peer.ApplyTemplate(t); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, templateToJSON(t))
}

func (s *Server) handleUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.peer.State()
	if _, ok := state.Templates[id]; !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	var req templateRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	t, err := templateFromRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t.ID = id
	t.CreatedAt = state.Templates[id].CreatedAt
	if err := s.peer.ApplyTemplate(t); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, templateToJSON(t))
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.peer.RemoveTemplate(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

func (s *Server) handleDeployTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.peer.State()
	t, ok := state.Templates[id]
	if !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	// Redeploy semantics: a deployed template's workload carries the template
	// name, so tear down any existing workload(s) with the same name before
	// submitting the new one. RemoveWorkload is synchronous (stops + removes on
	// the node, then deletes from Raft), so the new workload starts clean — no
	// lingering container or port allocation from the previous deploy.
	for _, wl := range state.Workloads {
		if workloadName(wl) != t.Name {
			continue
		}
		if _, err := s.ctrl.RemoveWorkload(r.Context(), &gen.RemoveWorkloadRequest{WorkloadId: wl.ID}); err != nil {
			st, _ := status.FromError(err)
			writeError(w, grpcHTTPStatus(st.Code()), "stop previous workload: "+st.Message())
			return
		}
	}

	var resp *gen.SubmitResponse
	var err error
	switch t.Kind {
	case types.KindStack:
		if t.Stack == nil {
			writeError(w, http.StatusBadRequest, "template has no stack spec")
			return
		}
		stackSpec := types.ComposeStackSpecToProto(*t.Stack)
		stackSpec.Name = t.Name
		resp, err = s.ctrl.SubmitStack(r.Context(), &gen.SubmitStackRequest{Spec: stackSpec})
	case types.KindContainer:
		if t.Container == nil {
			writeError(w, http.StatusBadRequest, "template has no container spec")
			return
		}
		resp, err = s.ctrl.SubmitContainer(r.Context(), &gen.SubmitContainerRequest{
			Spec: types.ContainerSpecToProto(*t.Container),
		})
	default:
		writeError(w, http.StatusBadRequest, "unknown template kind")
		return
	}
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, resp)
}

func newTemplateID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return fmt.Sprintf("%x", b)
}

func newUserID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return fmt.Sprintf("%x", b)
}

func hasActiveUsers(users map[string]types.User) bool {
	for _, u := range users {
		if u.Enabled {
			return true
		}
	}
	return false
}

func findUserByUsername(users map[string]types.User, username string) (types.User, bool) {
	for _, u := range users {
		if u.Username == username {
			return u, true
		}
	}
	return types.User{}, false
}

// generateSelfSignedCert creates an ECDSA P-256 self-signed certificate for
// the given DNS name, valid for one year. Returns PEM-encoded cert and key.
func generateSelfSignedCert(name string) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := crand.Int(crand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(crand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	certBuf := &bytes.Buffer{}
	if err := pem.Encode(certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return "", "", err
	}
	keyDer, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	keyBuf := &bytes.Buffer{}
	if err := pem.Encode(keyBuf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer}); err != nil {
		return "", "", err
	}
	return certBuf.String(), keyBuf.String(), nil
}

// csrSubject holds the optional X.509 subject fields a user can supply when
// generating a CSR. The Common Name is always taken from the domain name.
type csrSubject struct {
	Organization       string `json:"organization"`
	OrganizationalUnit string `json:"organizational_unit"`
	Country            string `json:"country"`
	State              string `json:"state"`
	Locality           string `json:"locality"`
	EmailAddress       string `json:"email_address"`
}

// generateCSR creates a PEM-encoded certificate signing request from the
// given PEM private key. Handles EC PRIVATE KEY, RSA PRIVATE KEY, and PKCS8.
func generateCSR(name, keyPEM string, subj csrSubject) (string, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}
	var privKey crypto.Signer
	var err error
	switch block.Type {
	case "EC PRIVATE KEY":
		privKey, err = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		privKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			return "", fmt.Errorf("parse PKCS8 key: %w", e)
		}
		var ok bool
		privKey, ok = k.(crypto.Signer)
		if !ok {
			return "", fmt.Errorf("unsupported key type in PKCS8 block")
		}
	default:
		return "", fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}
	if err != nil {
		return "", fmt.Errorf("parse key: %w", err)
	}
	subject := pkix.Name{CommonName: name}
	if subj.Organization != "" {
		subject.Organization = []string{subj.Organization}
	}
	if subj.OrganizationalUnit != "" {
		subject.OrganizationalUnit = []string{subj.OrganizationalUnit}
	}
	if subj.Country != "" {
		subject.Country = []string{subj.Country}
	}
	if subj.State != "" {
		subject.Province = []string{subj.State}
	}
	if subj.Locality != "" {
		subject.Locality = []string{subj.Locality}
	}
	template := &x509.CertificateRequest{
		Subject:        subject,
		DNSNames:       []string{name},
		EmailAddresses: nil,
	}
	if subj.EmailAddress != "" {
		template.EmailAddresses = []string{subj.EmailAddress}
	}
	csrDER, err := x509.CreateCertificateRequest(crand.Reader, template, privKey)
	if err != nil {
		return "", fmt.Errorf("create CSR: %w", err)
	}
	buf := &bytes.Buffer{}
	if err := pem.Encode(buf, &pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// newDomainID generates a short random hex ID for domains.
func newDomainID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return fmt.Sprintf("%x", b)
}

func workloadName(wl types.Workload) string {
	if wl.Container != nil {
		return wl.Container.Name
	}
	if wl.Stack != nil {
		return wl.Stack.Name
	}
	return ""
}

// ── Secret API handlers ────────────────────────────────────────────────────────

type secretJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
}

func newSecretID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (s *Server) handleListSecrets(w http.ResponseWriter, _ *http.Request) {
	state := s.peer.State()
	out := make([]secretJSON, 0, len(state.Secrets))
	for _, sec := range state.Secrets {
		out = append(out, secretJSON{ID: sec.ID, Name: sec.Name, CreatedAt: sec.CreatedAt.Unix()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, out)
}

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Value == "" {
		writeError(w, http.StatusBadRequest, "name and value are required")
		return
	}
	bao, err := s.openBaoClient()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := bao.Health(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "openbao unreachable: "+err.Error())
		return
	}
	sec := types.Secret{
		ID:        newSecretID(),
		Name:      req.Name,
		BaoPath:   "orchestrator/" + req.Name,
		CreatedAt: time.Now().UTC(),
	}
	if err := bao.Write(r.Context(), sec.BaoPath, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, "write to openbao: "+err.Error())
		return
	}
	if err := s.peer.ApplySecret(sec); err != nil {
		_ = bao.Delete(r.Context(), sec.BaoPath)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, secretJSON{ID: sec.ID, Name: sec.Name, CreatedAt: sec.CreatedAt.Unix()})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state := s.peer.State()
	sec, ok := state.Secrets[id]
	if !ok {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	if bao, err := s.openBaoClient(); err == nil {
		_ = bao.Delete(r.Context(), sec.BaoPath)
	}
	if err := s.peer.RemoveSecret(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

// openBaoClient returns a baoclient.Client from current Raft config, or an error
// if OpenBao has not been configured.
func (s *Server) openBaoClient() (*baoclient.Client, error) {
	cfg := s.peer.State().OpenBaoConfig
	if cfg == nil || cfg.Address == "" {
		return nil, fmt.Errorf("OpenBao is not configured — add connection details on the Secrets page")
	}
	caBundle := types.OpenBaoTrustBundle(s.peer.State().TrustedCAs)
	return baoclient.New(cfg.Address, cfg.Token, cfg.Mount, caBundle, cfg.InsecureSkipVerify), nil
}

// ── OpenBao API handlers ───────────────────────────────────────────────────────

type openBaoStatusJSON struct {
	Configured         bool   `json:"configured"`
	Address            string `json:"address,omitempty"`
	Mount              string `json:"mount,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	Connected          bool   `json:"connected"`
	Error              string `json:"error,omitempty"`
}

func (s *Server) handleOpenBaoStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.peer.State().OpenBaoConfig
	if cfg == nil || cfg.Address == "" {
		writeJSON(w, openBaoStatusJSON{Configured: false})
		return
	}
	out := openBaoStatusJSON{
		Configured:         true,
		Address:            cfg.Address,
		Mount:              cfg.Mount,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
	caBundle := types.OpenBaoTrustBundle(s.peer.State().TrustedCAs)
	bao := baoclient.New(cfg.Address, cfg.Token, cfg.Mount, caBundle, cfg.InsecureSkipVerify)
	if err := bao.Health(r.Context()); err != nil {
		out.Error = err.Error()
	} else {
		out.Connected = true
	}
	writeJSON(w, out)
}

func (s *Server) handleSetOpenBaoConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address            string `json:"address"`
		Token              string `json:"token"`
		Mount              string `json:"mount"`
		InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Address == "" {
		writeError(w, http.StatusBadRequest, "address is required")
		return
	}
	caBundle := types.OpenBaoTrustBundle(s.peer.State().TrustedCAs)
	bao := baoclient.New(req.Address, req.Token, req.Mount, caBundle, req.InsecureSkipVerify)
	if err := bao.Health(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "health check failed: "+err.Error())
		return
	}
	cfg := types.OpenBaoConfig{
		Address:            req.Address,
		Token:              req.Token,
		Mount:              req.Mount,
		InsecureSkipVerify: req.InsecureSkipVerify,
	}
	if err := s.peer.SetOpenBaoConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

// ── Trusted CA API handlers ────────────────────────────────────────────────────

type trustedCAJSON struct {
	ID                  string `json:"id"`
	Label               string `json:"label"`
	PEM                 string `json:"pem"`
	AppliesToOpenBao    bool   `json:"appliesToOpenBao"`
	AppliesToRegistries bool   `json:"appliesToRegistries"`
	NotAfter            int64  `json:"notAfter,omitempty"`
	Expired             bool   `json:"expired"`
	CreatedAt           int64  `json:"createdAt"`
}

func trustedCAToJSON(ca types.TrustedCA) trustedCAJSON {
	out := trustedCAJSON{
		ID:                  ca.ID,
		Label:               ca.Label,
		PEM:                 ca.PEM,
		AppliesToOpenBao:    ca.AppliesToOpenBao,
		AppliesToRegistries: ca.AppliesToRegistries,
		Expired:             ca.Expired(),
		CreatedAt:           ca.CreatedAt.Unix(),
	}
	if cert, err := ca.ParseCert(); err == nil {
		out.NotAfter = cert.NotAfter.Unix()
	}
	return out
}

func newTrustedCAID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (s *Server) handleListTrustedCAs(w http.ResponseWriter, _ *http.Request) {
	state := s.peer.State()
	out := make([]trustedCAJSON, 0, len(state.TrustedCAs))
	for _, ca := range state.TrustedCAs {
		out = append(out, trustedCAToJSON(ca))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, out)
}

func (s *Server) handleCreateTrustedCA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label               string `json:"label"`
		PEM                 string `json:"pem"`
		AppliesToOpenBao    bool   `json:"appliesToOpenBao"`
		AppliesToRegistries bool   `json:"appliesToRegistries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Label == "" || req.PEM == "" {
		writeError(w, http.StatusBadRequest, "label and pem are required")
		return
	}
	ca := types.TrustedCA{
		ID:                  newTrustedCAID(),
		Label:               req.Label,
		PEM:                 req.PEM,
		AppliesToOpenBao:    req.AppliesToOpenBao,
		AppliesToRegistries: req.AppliesToRegistries,
		CreatedAt:           time.Now().UTC(),
	}
	if _, err := ca.ParseCert(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid PEM certificate: "+err.Error())
		return
	}
	if err := s.peer.ApplyTrustedCA(ca); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, trustedCAToJSON(ca))
}

func (s *Server) handleUpdateTrustedCA(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Label               string `json:"label"`
		PEM                 string `json:"pem"`
		AppliesToOpenBao    bool   `json:"appliesToOpenBao"`
		AppliesToRegistries bool   `json:"appliesToRegistries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	state := s.peer.State()
	existing, ok := state.TrustedCAs[id]
	if !ok {
		writeError(w, http.StatusNotFound, "trusted CA not found")
		return
	}

	updated := types.TrustedCA{
		ID:                  existing.ID,
		Label:               existing.Label,
		PEM:                 existing.PEM,
		AppliesToOpenBao:    req.AppliesToOpenBao,
		AppliesToRegistries: req.AppliesToRegistries,
		CreatedAt:           existing.CreatedAt,
	}
	if req.Label != "" {
		updated.Label = req.Label
	}
	// Only replace the certificate if a new one is provided; blank keeps the existing one.
	if req.PEM != "" {
		updated.PEM = req.PEM
	}
	if _, err := updated.ParseCert(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid PEM certificate: "+err.Error())
		return
	}

	if err := s.peer.ApplyTrustedCA(updated); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, trustedCAToJSON(updated))
}

func (s *Server) handleDeleteTrustedCA(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.peer.RemoveTrustedCA(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

func (s *Server) handleAdminCompact(w http.ResponseWriter, _ *http.Request) {
	if err := s.peer.ForceSnapshot(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

// ── System services ───────────────────────────────────────────────────────────

// systemServiceNames lists the containers the orchestrator manages internally.
var systemServiceNames = []string{"ingressd", "meshrouterd"}

type systemServiceInfo struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	Kind         string `json:"kind"`         // "container" | "process"
	Status       string `json:"status"`       // "running" | "stopped" | "not found" | "unknown"
	Controllable bool   `json:"controllable"` // false for host processes
}

func (s *Server) handleSystemServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := make([]systemServiceInfo, 0, 3)

	roles := map[string]string{
		"ingressd":    "HTTP/TCP ingress (HAProxy wrapper)",
		"meshrouterd": "WireGuard mesh router",
	}
	for _, name := range systemServiceNames {
		info := systemServiceInfo{
			Name:         name,
			Role:         roles[name],
			Kind:         "container",
			Status:       "unknown",
			Controllable: true,
		}
		detail, err := s.agent.InspectContainer(ctx, name)
		if err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "No such") {
				info.Status = "not found"
			}
		} else {
			if strings.EqualFold(detail.State.Status, "running") {
				info.Status = "running"
			} else {
				info.Status = "stopped"
			}
		}
		out = append(out, info)
	}

	// proxyd runs as a host process; detect via /proc/*/comm.
	proxydInfo := systemServiceInfo{
		Name:         "proxyd",
		Role:         "TCP proxy + DNS (svc.local / mesh zones)",
		Kind:         "process",
		Controllable: false,
	}
	if isHostProcessRunning("proxyd") {
		proxydInfo.Status = "running"
	} else {
		proxydInfo.Status = "stopped"
	}
	out = append(out, proxydInfo)

	writeJSON(w, out)
}

func (s *Server) handleSystemServiceStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isSystemService(name) {
		writeError(w, http.StatusBadRequest, "unknown system service")
		return
	}
	if err := s.agent.StartContainer(r.Context(), name); err != nil {
		writeError(w, http.StatusBadGateway, "start "+name+": "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSystemServiceStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isSystemService(name) {
		writeError(w, http.StatusBadRequest, "unknown system service")
		return
	}
	if err := s.agent.StopContainer(r.Context(), name); err != nil {
		writeError(w, http.StatusBadGateway, "stop "+name+": "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func isSystemService(name string) bool {
	for _, n := range systemServiceNames {
		if n == name {
			return true
		}
	}
	return false
}

func isHostProcessRunning(name string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == name {
			return true
		}
	}
	return false
}

// ── Version ───────────────────────────────────────────────────────────────────

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"version": s.version})
}

// ── Export / Import ───────────────────────────────────────────────────────────

func (s *Server) handleExport(w http.ResponseWriter, _ *http.Request) {
	bundle := export.FromState(s.peer.State())
	data, err := bundle.Marshal()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="cluster-export.yaml"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

type importReport struct {
	Imported map[string]int `json:"imported"`
	Skipped  []string       `json:"skipped,omitempty"`
	Errors   []string       `json:"errors,omitempty"`
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	overwrite := r.URL.Query().Get("overwrite") == "true"
	data, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bundle, err := export.Unmarshal(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.peer.IsLeader() {
		writeError(w, http.StatusServiceUnavailable, "not the leader")
		return
	}
	report := s.applyBundle(r.Context(), bundle, overwrite)
	writeJSON(w, report)
}

func (s *Server) applyBundle(ctx context.Context, b *export.Bundle, overwrite bool) importReport {
	report := importReport{Imported: map[string]int{
		"workloads": 0, "domains": 0, "ingress_rules": 0,
		"services": 0, "secrets": 0, "registries": 0, "templates": 0,
	}}
	state := s.peer.State()

	// ── 1. Domains ────────────────────────────────────────────────────────────
	importedDomainIDs := make(map[string]string) // domain name → ID on this cluster
	for _, existing := range state.Domains {
		importedDomainIDs[existing.Name] = existing.ID // pre-seed with existing
	}
	for _, de := range b.Domains {
		if existingID, ok := importedDomainIDs[de.Name]; ok && !overwrite {
			report.Skipped = append(report.Skipped, "domain "+de.Name+" already exists")
			importedDomainIDs[de.Name] = existingID
			continue
		}
		id := newDomainID()
		if existingID, ok := importedDomainIDs[de.Name]; ok && overwrite {
			id = existingID // keep stable ID on overwrite
		}
		d := types.Domain{
			ID:        id,
			Name:      de.Name,
			TLSCert:   de.TLSCert,
			TLSKey:    de.TLSKey,
			CSR:       de.CSR,
			Enabled:   de.Enabled,
			CreatedAt: time.Now(),
		}
		if err := s.peer.ApplyDomain(d); err != nil {
			report.Errors = append(report.Errors, "domain "+de.Name+": "+err.Error())
			continue
		}
		importedDomainIDs[de.Name] = id
		report.Imported["domains"]++
	}

	// ── 2. Workloads ──────────────────────────────────────────────────────────
	existingWorkloadNames := make(map[string]bool)
	for _, wl := range state.Workloads {
		existingWorkloadNames[wl.Name()] = true
	}
	for _, we := range b.Workloads {
		name := ""
		if we.Container != nil {
			name = we.Container.Name
		} else if we.Stack != nil {
			name = we.Stack.Name
		}
		if existingWorkloadNames[name] && !overwrite {
			report.Skipped = append(report.Skipped, "workload "+name+" already exists")
			continue
		}
		var submitErr error
		if we.Kind == "container" && we.Container != nil {
			ports := make([]*gen.PortMapping, len(we.Container.Ports))
			for i, p := range we.Container.Ports {
				ports[i] = &gen.PortMapping{ContainerPort: p.ContainerPort, Protocol: p.Protocol}
			}
			vols := make([]*gen.VolumeMount, len(we.Container.Volumes))
			for i, v := range we.Container.Volumes {
				vols[i] = &gen.VolumeMount{Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly}
			}
			_, submitErr = s.ctrl.SubmitContainer(ctx, &gen.SubmitContainerRequest{
				Spec: &gen.ContainerSpec{
					Name:       we.Container.Name,
					Image:      we.Container.Image,
					Command:    we.Container.Command,
					Env:        we.Container.Env,
					Ports:      ports,
					Volumes:    vols,
					Labels:     we.Container.Labels,
					Namespace:  we.Container.Namespace,
					SecretRefs: we.Container.SecretRefs,
					Replicas:   int32(we.Container.Replicas),
				},
			})
		} else if we.Kind == "stack" && we.Stack != nil {
			_, submitErr = s.ctrl.SubmitStack(ctx, &gen.SubmitStackRequest{
				Spec: &gen.ComposeStackSpec{
					Name:       we.Stack.Name,
					ComposeYaml: we.Stack.ComposeYAML,
					SecretRefs: we.Stack.SecretRefs,
					Replicas:   int32(we.Stack.Replicas),
				},
			})
		}
		if submitErr != nil {
			report.Errors = append(report.Errors, "workload "+name+": "+submitErr.Error())
			continue
		}
		report.Imported["workloads"]++
	}

	// ── 3. Services ───────────────────────────────────────────────────────────
	existingSvcNames := make(map[string]bool)
	for _, svc := range state.Services {
		existingSvcNames[svc.Name] = true
	}
	for _, se := range b.Services {
		if existingSvcNames[se.Name] && !overwrite {
			report.Skipped = append(report.Skipped, "service "+se.Name+" already exists")
			continue
		}
		_, err := s.ctrl.CreateService(ctx, &gen.CreateServiceRequest{
			Name:          se.Name,
			ContainerFqdn: se.ContainerFQDN,
			ContainerPort: se.ContainerPort,
		})
		if err != nil {
			report.Errors = append(report.Errors, "service "+se.Name+": "+err.Error())
			continue
		}
		report.Imported["services"]++
	}

	// ── 4. Ingress rules ──────────────────────────────────────────────────────
	for _, ie := range b.IngressRules {
		domainID, ok := importedDomainIDs[ie.DomainName]
		if !ok {
			report.Errors = append(report.Errors, "ingress for "+ie.DomainName+": domain not found")
			continue
		}
		host := ie.Host
		if host == "" {
			host = ie.DomainName
		}
		rule := types.IngressRule{
			ID:            newIngressID(),
			DomainID:      domainID,
			Host:          host,
			PathPrefix:    ie.PathPrefix,
			StripPrefix:   ie.StripPrefix,
			ContainerFQDN: ie.ContainerFQDN,
			ContainerPort: ie.ContainerPort,
			CreatedAt:     time.Now(),
		}
		if err := s.peer.ApplyIngress(rule); err != nil {
			report.Errors = append(report.Errors, "ingress "+ie.DomainName+ie.PathPrefix+": "+err.Error())
			continue
		}
		report.Imported["ingress_rules"]++
	}

	// ── 5. Secrets (ref only — values live in OpenBao) ────────────────────────
	existingSecretNames := make(map[string]bool)
	for _, sec := range state.Secrets {
		existingSecretNames[sec.Name] = true
	}
	for _, se := range b.Secrets {
		if existingSecretNames[se.Name] && !overwrite {
			report.Skipped = append(report.Skipped, "secret "+se.Name+" already exists")
			continue
		}
		sec := types.Secret{
			ID:        newSecretID(),
			Name:      se.Name,
			BaoPath:   se.BaoPath,
			CreatedAt: time.Now(),
		}
		if err := s.peer.ApplySecret(sec); err != nil {
			report.Errors = append(report.Errors, "secret "+se.Name+": "+err.Error())
			continue
		}
		report.Imported["secrets"]++
	}

	// ── 6. Registries ─────────────────────────────────────────────────────────
	existingRegNames := make(map[string]bool)
	for _, reg := range state.Registries {
		existingRegNames[reg.Name] = true
	}
	for _, re := range b.Registries {
		if existingRegNames[re.Name] && !overwrite {
			report.Skipped = append(report.Skipped, "registry "+re.Name+" already exists")
			continue
		}
		reg := types.Registry{
			ID:        newRegistryID(),
			Name:      re.Name,
			URL:       re.URL,
			Username:  re.Username,
			Password:  re.Password,
			CreatedAt: time.Now(),
		}
		if err := s.peer.ApplyRegistry(reg); err != nil {
			report.Errors = append(report.Errors, "registry "+re.Name+": "+err.Error())
			continue
		}
		report.Imported["registries"]++
	}

	// ── 7. Templates ──────────────────────────────────────────────────────────
	existingTplNames := make(map[string]bool)
	for _, t := range state.Templates {
		existingTplNames[t.Name] = true
	}
	for _, te := range b.Templates {
		if existingTplNames[te.Name] && !overwrite {
			report.Skipped = append(report.Skipped, "template "+te.Name+" already exists")
			continue
		}
		t := types.WorkloadTemplate{
			ID:          newTemplateID(),
			Name:        te.Name,
			Description: te.Description,
			CreatedAt:   time.Now(),
		}
		if te.Kind == "container" && te.Container != nil {
			t.Kind = types.KindContainer
			t.Container = te.Container
		} else if te.Kind == "stack" && te.Stack != nil {
			t.Kind = types.KindStack
			t.Stack = te.Stack
		}
		if err := s.peer.ApplyTemplate(t); err != nil {
			report.Errors = append(report.Errors, "template "+te.Name+": "+err.Error())
			continue
		}
		report.Imported["templates"]++
	}

	return report
}
