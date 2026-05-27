package control

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/internal/tlsutil"
	"github.com/eghansah/orchestrator/pkg/types"
)

// Server implements ControlServiceServer. All mutating RPCs require the node
// to be the current Raft leader; reads are served from local FSM state.
type Server struct {
	gen.UnimplementedControlServiceServer
	peer     *internraft.Peer
	nodeID   string
	grpcAddr string // this node's own gRPC address
	ownCert  tls.Certificate
}

func New(peer *internraft.Peer, nodeID, grpcAddr string, ownCert tls.Certificate) *Server {
	return &Server{peer: peer, nodeID: nodeID, grpcAddr: grpcAddr, ownCert: ownCert}
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return fmt.Sprintf("%x", b)
}

func (s *Server) requireLeader() error {
	if !s.peer.IsLeader() {
		return status.Error(codes.FailedPrecondition, "not the leader")
	}
	return nil
}

// ── Submit ────────────────────────────────────────────────────────────────────

func (s *Server) SubmitContainer(ctx context.Context, req *gen.SubmitContainerRequest) (*gen.SubmitResponse, error) {
	if req.Spec == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	spec := types.ContainerSpecFromProto(req.Spec)
	if spec.Namespace == "" {
		spec.Namespace = "orchestrator"
	}
	wl := types.Workload{
		ID:        newID(),
		Kind:      types.KindContainer,
		Container: &spec,
		Phase:     types.PhasePending,
		CreatedAt: time.Now(),
	}
	return s.scheduleAndPlace(ctx, wl)
}

func (s *Server) SubmitStack(ctx context.Context, req *gen.SubmitStackRequest) (*gen.SubmitResponse, error) {
	if req.Spec == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	spec := types.ComposeStackSpecFromProto(req.Spec)
	wl := types.Workload{
		ID:        newID(),
		Kind:      types.KindStack,
		Stack:     &spec,
		Phase:     types.PhasePending,
		CreatedAt: time.Now(),
	}
	return s.scheduleAndPlace(ctx, wl)
}

// scheduleAndPlace picks a node, commits to Raft, then calls PlaceWorkload on the agent.
func (s *Server) scheduleAndPlace(ctx context.Context, wl types.Workload) (*gen.SubmitResponse, error) {
	nodeID, nodeAddr, nodeCert, err := s.pickNode()
	if err != nil {
		return &gen.SubmitResponse{Accepted: false, Reason: err.Error()}, nil
	}

	wl.NodeID = nodeID
	wl.Phase = types.PhaseScheduled
	if err := s.peer.ApplyWorkload(wl); err != nil {
		return nil, status.Errorf(codes.Internal, "apply workload: %v", err)
	}

	// Read back from state so we have the FSM-assigned port allocations.
	if committed, ok := s.peer.State().Workloads[wl.ID]; ok {
		wl = committed
	}

	if err := s.placeOnNode(ctx, nodeAddr, nodeCert, wl); err != nil {
		// Mark failed in Raft — the agent never ran it.
		wl.Phase = types.PhaseFailed
		_ = s.peer.ApplyWorkload(wl)
		return &gen.SubmitResponse{Accepted: false, Reason: err.Error()}, nil
	}

	wl.Phase = types.PhaseRunning
	_ = s.peer.ApplyWorkload(wl)
	return &gen.SubmitResponse{WorkloadId: wl.ID, Accepted: true}, nil
}

// ── Remove ────────────────────────────────────────────────────────────────────

func (s *Server) RemoveWorkload(ctx context.Context, req *gen.RemoveWorkloadRequest) (*gen.RemoveWorkloadResponse, error) {
	if req.WorkloadId == "" {
		return nil, status.Error(codes.InvalidArgument, "workload_id is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}

	state := s.peer.State()
	wl, ok := state.Workloads[req.WorkloadId]
	if !ok {
		return &gen.RemoveWorkloadResponse{Accepted: true}, nil // idempotent
	}

	if wl.NodeID != "" {
		if n, nOK := state.Nodes[wl.NodeID]; nOK {
			if err := s.removeFromNode(ctx, n.Address, n.TLSCert, req.WorkloadId); err != nil {
				return &gen.RemoveWorkloadResponse{Accepted: false, Reason: err.Error()}, nil
			}
		}
	}

	if err := s.peer.RemoveWorkload(req.WorkloadId); err != nil {
		return nil, status.Errorf(codes.Internal, "remove from raft: %v", err)
	}
	return &gen.RemoveWorkloadResponse{Accepted: true}, nil
}

// ── Queries ───────────────────────────────────────────────────────────────────

func (s *Server) ListWorkloads(_ context.Context, req *gen.ListWorkloadsRequest) (*gen.ListWorkloadsResponse, error) {
	state := s.peer.State()
	var out []*gen.Workload
	for _, wl := range state.Workloads {
		if len(req.Phases) > 0 && !phaseIn(wl.Phase, req.Phases) {
			continue
		}
		out = append(out, types.WorkloadToProto(wl))
	}
	return &gen.ListWorkloadsResponse{Workloads: out}, nil
}

func (s *Server) ListNodes(_ context.Context, _ *gen.ListNodesRequest) (*gen.ListNodesResponse, error) {
	state := s.peer.State()
	var out []*gen.Node
	for _, n := range state.Nodes {
		out = append(out, types.NodeToProto(n))
	}
	return &gen.ListNodesResponse{Nodes: out}, nil
}

func (s *Server) DrainNode(_ context.Context, req *gen.DrainNodeRequest) (*gen.DrainNodeResponse, error) {
	if req.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	state := s.peer.State()
	n, ok := state.Nodes[req.NodeId]
	if !ok {
		return &gen.DrainNodeResponse{Accepted: false, Reason: "node not found"}, nil
	}
	n.Status = types.NodeDraining
	if err := s.peer.RegisterNode(n); err != nil {
		return nil, status.Errorf(codes.Internal, "update node: %v", err)
	}
	return &gen.DrainNodeResponse{Accepted: true}, nil
}

func (s *Server) GetClusterState(_ context.Context, _ *gen.GetClusterStateRequest) (*gen.GetClusterStateResponse, error) {
	state := s.peer.State()
	var nodes []*gen.Node
	for _, n := range state.Nodes {
		nodes = append(nodes, types.NodeToProto(n))
	}
	var workloads []*gen.Workload
	for _, wl := range state.Workloads {
		workloads = append(workloads, types.WorkloadToProto(wl))
	}
	return &gen.GetClusterStateResponse{
		Nodes:     nodes,
		Workloads: workloads,
		LeaderId:  s.peer.LeaderID(),
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// pickNode selects the first healthy node from cluster state.
func (s *Server) pickNode() (nodeID, addr string, certDER []byte, err error) {
	for _, n := range s.peer.State().Nodes {
		if n.Status == types.NodeHealthy {
			return n.ID, n.Address, n.TLSCert, nil
		}
	}
	return "", "", nil, fmt.Errorf("no healthy nodes available")
}

func (s *Server) dialNode(addr string, serverCertDER []byte) (*grpc.ClientConn, error) {
	tlsCfg := tlsutil.ClientTLSConfig(s.ownCert, serverCertDER)
	return grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
}

func (s *Server) placeOnNode(ctx context.Context, addr string, certDER []byte, wl types.Workload) error {
	conn, err := s.dialNode(addr, certDER)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()
	resp, err := gen.NewNodeServiceClient(conn).PlaceWorkload(ctx, &gen.PlaceWorkloadRequest{
		Workload: types.WorkloadToProto(wl),
	})
	if err != nil {
		return err
	}
	if !resp.Accepted {
		return fmt.Errorf("agent rejected: %s", resp.Reason)
	}
	return nil
}

func (s *Server) removeFromNode(ctx context.Context, addr string, certDER []byte, workloadID string) error {
	conn, err := s.dialNode(addr, certDER)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()
	resp, err := gen.NewNodeServiceClient(conn).RemoveWorkload(ctx, &gen.RemoveWorkloadRequest{
		WorkloadId: workloadID,
	})
	if err != nil {
		return err
	}
	if !resp.Accepted {
		return fmt.Errorf("agent rejected: %s", resp.Reason)
	}
	return nil
}

// ── Ingress ───────────────────────────────────────────────────────────────────

func (s *Server) CreateIngress(_ context.Context, req *gen.CreateIngressRequest) (*gen.CreateIngressResponse, error) {
	if req.WorkloadId == "" {
		return nil, status.Error(codes.InvalidArgument, "workload_id is required")
	}
	if req.Port == 0 {
		return nil, status.Error(codes.InvalidArgument, "port is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	rule := types.IngressRule{
		ID:         newID(),
		Host:       req.Host,
		PathPrefix: req.PathPrefix,
		WorkloadID: req.WorkloadId,
		Port:       req.Port,
		CreatedAt:  time.Now(),
	}
	if err := s.peer.ApplyIngress(rule); err != nil {
		return nil, status.Errorf(codes.Internal, "apply ingress: %v", err)
	}
	return &gen.CreateIngressResponse{RuleId: rule.ID, Accepted: true}, nil
}

func (s *Server) DeleteIngress(_ context.Context, req *gen.DeleteIngressRequest) (*gen.DeleteIngressResponse, error) {
	if req.RuleId == "" {
		return nil, status.Error(codes.InvalidArgument, "rule_id is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	if err := s.peer.RemoveIngress(req.RuleId); err != nil {
		return nil, status.Errorf(codes.Internal, "remove ingress: %v", err)
	}
	return &gen.DeleteIngressResponse{Accepted: true}, nil
}

func (s *Server) ListIngress(_ context.Context, _ *gen.ListIngressRequest) (*gen.ListIngressResponse, error) {
	state := s.peer.State()
	rules := make([]*gen.IngressRule, 0, len(state.IngressRules))
	for _, r := range state.IngressRules {
		rules = append(rules, types.IngressRuleToProto(r))
	}
	return &gen.ListIngressResponse{Rules: rules}, nil
}

// ── Services ──────────────────────────────────────────────────────────────────

func (s *Server) CreateService(_ context.Context, req *gen.CreateServiceRequest) (*gen.CreateServiceResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.WorkloadId == "" {
		return nil, status.Error(codes.InvalidArgument, "workload_id is required")
	}
	if req.TargetPort == 0 {
		return nil, status.Error(codes.InvalidArgument, "target_port is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	svc := types.Service{
		ID:         newID(),
		Name:       req.Name,
		WorkloadID: req.WorkloadId,
		TargetPort: req.TargetPort,
		CreatedAt:  time.Now(),
	}
	if err := s.peer.ApplyService(svc); err != nil {
		return nil, status.Errorf(codes.Internal, "apply service: %v", err)
	}
	// Read back to get FSM-assigned system port.
	committed := s.peer.State().Services[svc.ID]
	return &gen.CreateServiceResponse{ServiceId: svc.ID, SystemPort: committed.SystemPort, Accepted: true}, nil
}

func (s *Server) DeleteService(_ context.Context, req *gen.DeleteServiceRequest) (*gen.DeleteServiceResponse, error) {
	if req.ServiceId == "" {
		return nil, status.Error(codes.InvalidArgument, "service_id is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	if err := s.peer.RemoveService(req.ServiceId); err != nil {
		return nil, status.Errorf(codes.Internal, "remove service: %v", err)
	}
	return &gen.DeleteServiceResponse{Accepted: true}, nil
}

func (s *Server) ListService(_ context.Context, _ *gen.ListServiceRequest) (*gen.ListServiceResponse, error) {
	state := s.peer.State()
	svcs := make([]*gen.Service, 0, len(state.Services))
	for _, svc := range state.Services {
		svcs = append(svcs, types.ServiceToProto(svc))
	}
	return &gen.ListServiceResponse{Services: svcs}, nil
}

func phaseIn(phase types.WorkloadPhase, phases []gen.WorkloadPhase) bool {
	for _, p := range phases {
		if types.WorkloadPhase(p) == phase {
			return true
		}
	}
	return false
}
