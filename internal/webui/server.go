package webui

import (
	"encoding/json"
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
	peer *internraft.Peer
	ctrl *control.Server
}

func New(peer *internraft.Peer, ctrl *control.Server) *Server {
	return &Server{peer: peer, ctrl: ctrl}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// API routes.
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/workloads/run", s.handleRun)
	mux.HandleFunc("POST /api/workloads/stack", s.handleStack)
	mux.HandleFunc("POST /api/workloads/{id}/remove", s.handleRemove)
	mux.HandleFunc("POST /api/nodes/{id}/drain", s.handleDrain)

	// SPA: serve embedded dist/ with index.html fallback for client-side routing.
	sub, _ := fs.Sub(distFS, "dist")
	mux.Handle("/", spaHandler{fs: http.FS(sub)})

	return mux
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
	type workloadJSON struct {
		ID        string `json:"id"`
		NodeID    string `json:"node_id"`
		Phase     string `json:"phase"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		CreatedAt int64  `json:"created_at"`
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
		out.Workloads = append(out.Workloads, workloadJSON{
			ID:        wl.ID,
			NodeID:    wl.NodeID,
			Phase:     wl.Phase.String(),
			Kind:      kindString(wl.Kind),
			Name:      workloadName(wl),
			CreatedAt: wl.CreatedAt.Unix(),
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

// ── SPA handler ───────────────────────────────────────────────────────────────

// spaHandler serves static files from an http.FileSystem, falling back to
// index.html for any path that doesn't resolve to an existing file.
type spaHandler struct {
	fs http.FileSystem
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}
	f, err := h.fs.Open(path)
	if err != nil {
		// Unknown path — serve index.html so React's client-side navigation works.
		r.URL.Path = "/"
	} else {
		f.Close()
	}
	http.FileServer(h.fs).ServeHTTP(w, r)
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
	if len(parts) != 2 {
		return nil, &portError{s}
	}
	host, err1 := strconv.ParseUint(parts[0], 10, 32)
	cont, err2 := strconv.ParseUint(parts[1], 10, 32)
	if err1 != nil || err2 != nil {
		return nil, &portError{s}
	}
	return &gen.PortMapping{
		HostPort:      uint32(host),
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
