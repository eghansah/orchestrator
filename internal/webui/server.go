package webui

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/eghansah/orchestrator/internal/control"
	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/pkg/types"
)

// Server serves the Cloudscape web console and JSON REST API.
type Server struct {
	peer        *internraft.Peer
	ctrl        *control.Server
	adminToken  string // required Bearer token for /api/* routes; empty = no auth
	webPassword string // password for the /api/auth/login endpoint; empty = login disabled
	prefix      string // URL path prefix, e.g. "/console" (no trailing slash, may be "")
}

// New creates a Server. prefix is an optional URL subdirectory (e.g. "/console");
// pass "" to serve at the root. A trailing slash is stripped automatically.
func New(peer *internraft.Peer, ctrl *control.Server, adminToken, webPassword, prefix string) *Server {
	p := strings.TrimRight(prefix, "/")
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return &Server{peer: peer, ctrl: ctrl, adminToken: adminToken, webPassword: webPassword, prefix: p}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Auth routes — unprotected (no Bearer token required).
	mux.Handle("POST /api/auth/login", http.HandlerFunc(s.handleLogin))
	mux.Handle("POST /api/auth/logout", http.HandlerFunc(s.handleLogout))

	// API routes — all require a valid admin token when one is configured.
	a := s.auth
	mux.Handle("GET /api/state", a(s.handleState))
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

// auth wraps a handler to require a valid Bearer token in the Authorization header.
// When no admin token is configured the handler is called without restriction.
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken != "" {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(s.adminToken)) != 1 {
				writeError(w, http.StatusUnauthorized, "invalid or missing token")
				return
			}
		}
		next(w, r)
	})
}

// ── Auth handlers ─────────────────────────────────────────────────────────────

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.webPassword == "" {
		writeError(w, http.StatusServiceUnavailable, "web console login is not configured")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte("admin"))
	passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(s.webPassword))
	if userOK != 1 || passOK != 1 {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	writeJSON(w, map[string]string{"token": s.adminToken})
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
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
		ID       string `json:"id"`
		Address  string `json:"address"`
		Status   string `json:"status"`
		CPUCores uint32 `json:"cpu_cores"`
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
	type resp struct {
		LeaderID   string         `json:"leader_id"`
		IsLeader   bool           `json:"is_leader"`
		LeaderAddr string         `json:"leader_addr,omitempty"`
		Nodes      []nodeJSON     `json:"nodes"`
		Workloads  []workloadJSON `json:"workloads"`
	}

	out := resp{
		LeaderID:   leaderID,
		IsLeader:   isLeader,
		LeaderAddr: leaderAddr,
		Nodes:      []nodeJSON{},
		Workloads:  []workloadJSON{},
	}

	for _, n := range state.Nodes {
		out.Nodes = append(out.Nodes, nodeJSON{
			ID:       n.ID,
			Address:  n.Address,
			Status:   nodeStatusString(n.Status),
			CPUCores: n.Resources.CPUCores,
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

func (s *Server) handleListIngress(w http.ResponseWriter, r *http.Request) {
	resp, err := s.ctrl.ListIngress(r.Context(), &gen.ListIngressRequest{})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	type ruleJSON struct {
		ID         string `json:"id"`
		Host       string `json:"host"`
		PathPrefix string `json:"path_prefix"`
		WorkloadID string `json:"workload_id"`
		Port       uint32 `json:"port"`
		CreatedAt  int64  `json:"created_at"`
	}
	rules := make([]ruleJSON, 0, len(resp.Rules))
	for _, r := range resp.Rules {
		rules = append(rules, ruleJSON{
			ID:         r.Id,
			Host:       r.Host,
			PathPrefix: r.PathPrefix,
			WorkloadID: r.WorkloadId,
			Port:       r.Port,
			CreatedAt:  r.CreatedAt,
		})
	}
	writeJSON(w, rules)
}

func (s *Server) handleCreateIngress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host       string `json:"host"`
		PathPrefix string `json:"path_prefix"`
		WorkloadID string `json:"workload_id"`
		Port       uint32 `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resp, err := s.ctrl.CreateIngress(r.Context(), &gen.CreateIngressRequest{
		Host:       req.Host,
		PathPrefix: req.PathPrefix,
		WorkloadId: req.WorkloadID,
		Port:       req.Port,
	})
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, grpcHTTPStatus(st.Code()), st.Message())
		return
	}
	writeJSON(w, resp)
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

func workloadName(wl types.Workload) string {
	if wl.Container != nil {
		return wl.Container.Name
	}
	if wl.Stack != nil {
		return wl.Stack.Name
	}
	return ""
}
