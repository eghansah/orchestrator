package webui

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/eghansah/orchestrator/internal/agent"
	"github.com/eghansah/orchestrator/internal/control"
	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/internal/registry"
	"github.com/eghansah/orchestrator/pkg/types"
)

// Server serves the Cloudscape web console and JSON REST API.
type Server struct {
	peer        *internraft.Peer
	ctrl        *control.Server
	agent       *agent.Agent
	adminToken  string // required Bearer token for /api/* routes; empty = no auth
	webPassword string // password for the /api/auth/login endpoint; empty = login disabled
	prefix      string // URL path prefix, e.g. "/console" (no trailing slash, may be "")
	ldap        LDAPConfig

	sessionsMu sync.RWMutex
	sessions   map[string]time.Time // per-login token → expiry
}

const sessionTTL = 8 * time.Hour

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

// New creates a Server. prefix is an optional URL subdirectory (e.g. "/console");
// pass "" to serve at the root. A trailing slash is stripped automatically.
func New(peer *internraft.Peer, ctrl *control.Server, ag *agent.Agent, adminToken, webPassword, prefix string, ldapCfg LDAPConfig) *Server {
	p := strings.TrimRight(prefix, "/")
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if ldapCfg.UserFilter == "" {
		ldapCfg.UserFilter = "(sAMAccountName=%s)"
	}
	return &Server{
		peer:        peer,
		ctrl:        ctrl,
		agent:       ag,
		adminToken:  adminToken,
		webPassword: webPassword,
		prefix:      p,
		ldap:        ldapCfg,
		sessions:    make(map[string]time.Time),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Auth routes — unprotected (no Bearer token required).
	mux.Handle("POST /api/auth/login", http.HandlerFunc(s.handleLogin))
	mux.Handle("POST /api/auth/logout", http.HandlerFunc(s.handleLogout))

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
	mux.Handle("POST /api/ingress/{id}/delete", a(s.handleDeleteIngress))
	mux.Handle("GET /api/services", a(s.handleListServices))
	mux.Handle("POST /api/services", a(s.handleCreateService))
	mux.Handle("POST /api/services/{id}/delete", a(s.handleDeleteService))
	mux.Handle("GET /api/domains", a(s.handleListDomains))
	mux.Handle("POST /api/domains", a(s.handleCreateDomain))
	mux.Handle("POST /api/domains/{id}/update", a(s.handleUpdateDomain))
	mux.Handle("POST /api/domains/{id}/toggle", a(s.handleToggleDomain))
	mux.Handle("POST /api/domains/{id}/delete", a(s.handleDeleteDomain))
	mux.Handle("GET /api/users", a(s.handleListUsers))
	mux.Handle("POST /api/users", a(s.handleCreateUser))
	mux.Handle("POST /api/users/{id}/toggle", a(s.handleToggleUser))
	mux.Handle("POST /api/users/{id}/delete", a(s.handleDeleteUser))
	mux.Handle("GET /api/registries", a(s.handleListRegistries))
	mux.Handle("POST /api/registries", a(s.handleCreateRegistry))
	mux.Handle("POST /api/registries/{id}/update", a(s.handleUpdateRegistry))
	mux.Handle("POST /api/registries/{id}/delete", a(s.handleDeleteRegistry))
	mux.Handle("GET /api/registries/{id}/catalog", a(s.handleRegistryCatalog))
	mux.Handle("GET /api/registries/{id}/tags", a(s.handleRegistryTags))
	mux.Handle("GET /api/registries/{id}/env", a(s.handleRegistryEnv))

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
		writeJSON(w, map[string]string{"token": s.newSession()})
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
	ID         string `json:"id"`
	DomainID   string `json:"domain_id"`
	Host       string `json:"host"`
	PathPrefix string `json:"path_prefix"`
	WorkloadID string `json:"workload_id"`
	Port       uint32 `json:"port"`
	CreatedAt  int64  `json:"created_at"`
}

func (s *Server) handleListIngress(w http.ResponseWriter, _ *http.Request) {
	state := s.peer.State()
	rules := make([]ingressRuleJSON, 0, len(state.IngressRules))
	for _, r := range state.IngressRules {
		rules = append(rules, ingressRuleJSON{
			ID:         r.ID,
			DomainID:   r.DomainID,
			Host:       r.Host,
			PathPrefix: r.PathPrefix,
			WorkloadID: r.WorkloadID,
			Port:       r.Port,
			CreatedAt:  r.CreatedAt.Unix(),
		})
	}
	writeJSON(w, rules)
}

func (s *Server) handleCreateIngress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainID   string `json:"domain_id"`
		PathPrefix string `json:"path_prefix"`
		WorkloadID string `json:"workload_id"`
		Port       uint32 `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DomainID == "" {
		writeError(w, http.StatusBadRequest, "domain_id is required")
		return
	}
	if req.WorkloadID == "" {
		writeError(w, http.StatusBadRequest, "workload_id is required")
		return
	}
	if req.Port == 0 {
		writeError(w, http.StatusBadRequest, "port is required")
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
		ID:         newIngressID(),
		DomainID:   domain.ID,
		Host:       domain.Name,
		PathPrefix: req.PathPrefix,
		WorkloadID: req.WorkloadID,
		Port:       req.Port,
		CreatedAt:  time.Now(),
	}
	if err := s.peer.ApplyIngress(rule); err != nil {
		writeError(w, http.StatusInternalServerError, "apply ingress: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"rule_id": rule.ID, "accepted": true})
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

// ── Services API handlers ─────────────────────────────────────────────────────

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	resp, err := s.ctrl.ListService(r.Context(), &gen.ListServiceRequest{})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	type svcJSON struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		WorkloadID string `json:"workload_id"`
		TargetPort uint32 `json:"target_port"`
		SystemPort uint32 `json:"system_port"`
		CreatedAt  int64  `json:"created_at"`
	}
	svcs := make([]svcJSON, 0, len(resp.Services))
	for _, s := range resp.Services {
		svcs = append(svcs, svcJSON{
			ID:         s.Id,
			Name:       s.Name,
			WorkloadID: s.WorkloadId,
			TargetPort: s.TargetPort,
			SystemPort: s.SystemPort,
			CreatedAt:  s.CreatedAt,
		})
	}
	writeJSON(w, svcs)
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		WorkloadID string `json:"workload_id"`
		TargetPort uint32 `json:"target_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resp, err := s.ctrl.CreateService(r.Context(), &gen.CreateServiceRequest{
		Name:       req.Name,
		WorkloadId: req.WorkloadID,
		TargetPort: req.TargetPort,
	})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, resp)
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
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// domainWithKeyJSON is returned only once, at creation, so the operator can
// copy the private key. Subsequent reads never include the key.
type domainWithKeyJSON struct {
	domainJSON
	TLSKey string `json:"tls_key"`
}

func (s *Server) handleListDomains(w http.ResponseWriter, _ *http.Request) {
	state := s.peer.State()
	out := make([]domainJSON, 0, len(state.Domains))
	for _, d := range state.Domains {
		out = append(out, domainJSON{
			ID:        d.ID,
			Name:      d.Name,
			TLSCert:   d.TLSCert,
			Enabled:   d.Enabled,
			CreatedAt: d.CreatedAt.Unix(),
		})
	}
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

	d := types.Domain{
		ID:        newDomainID(),
		Name:      req.Name,
		TLSCert:   req.TLSCert,
		TLSKey:    req.TLSKey,
		Enabled:   true,
		CreatedAt: time.Now(),
	}
	if err := s.peer.ApplyDomain(d); err != nil {
		writeError(w, http.StatusInternalServerError, "apply domain: "+err.Error())
		return
	}
	writeJSON(w, domainWithKeyJSON{
		domainJSON: domainJSON{
			ID:        d.ID,
			Name:      d.Name,
			TLSCert:   d.TLSCert,
			Enabled:   d.Enabled,
			CreatedAt: d.CreatedAt.Unix(),
		},
		TLSKey: d.TLSKey,
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

	// If both cert and key are left empty, regenerate.
	if newCert == "" && newKey == "" {
		cert, key, err := generateSelfSignedCert(newName)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "generate cert: "+err.Error())
			return
		}
		newCert = cert
		newKey = key
	} else if newCert == "" {
		newCert = existing.TLSCert
	} else if newKey == "" {
		newKey = existing.TLSKey
	}

	updated := types.Domain{
		ID:        existing.ID,
		Name:      newName,
		TLSCert:   newCert,
		TLSKey:    newKey,
		Enabled:   existing.Enabled,
		CreatedAt: existing.CreatedAt,
	}
	if err := s.peer.ApplyDomain(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "apply domain: "+err.Error())
		return
	}
	writeJSON(w, domainJSON{
		ID:        updated.ID,
		Name:      updated.Name,
		TLSCert:   updated.TLSCert,
		Enabled:   updated.Enabled,
		CreatedAt: updated.CreatedAt.Unix(),
	})
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
	writeJSON(w, domainJSON{
		ID:        existing.ID,
		Name:      existing.Name,
		TLSCert:   existing.TLSCert,
		Enabled:   existing.Enabled,
		CreatedAt: existing.CreatedAt.Unix(),
	})
}

func (s *Server) handleDeleteDomain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.peer.RemoveDomain(id); err != nil {
		writeError(w, http.StatusInternalServerError, "remove domain: "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

// ── Users API handlers ────────────────────────────────────────────────────────

type userJSON struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, _ *http.Request) {
	state := s.peer.State()
	out := make([]userJSON, 0, len(state.Users))
	for _, u := range state.Users {
		out = append(out, userJSON{
			ID:        u.ID,
			Username:  u.Username,
			Enabled:   u.Enabled,
			CreatedAt: u.CreatedAt.Unix(),
		})
	}
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
		ID:        u.ID,
		Username:  u.Username,
		Enabled:   u.Enabled,
		CreatedAt: u.CreatedAt.Unix(),
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
		ID:        existing.ID,
		Username:  existing.Username,
		Enabled:   existing.Enabled,
		CreatedAt: existing.CreatedAt.Unix(),
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

func newRegistryID() string {
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
