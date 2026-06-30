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

	"github.com/eghansah/orchestrator/internal/baoclient"
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

// baoClient returns a baoclient.Client configured from the current Raft state,
// or an error when OpenBao has not been configured yet.
func (s *Server) baoClient() (*baoclient.Client, error) {
	cfg := s.peer.State().OpenBaoConfig
	if cfg == nil || cfg.Address == "" {
		return nil, fmt.Errorf("OpenBao is not configured — set an address via the Secrets page or ctl")
	}
	return baoclient.New(cfg.Address, cfg.Token, cfg.Mount), nil
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

	// Build a placement copy with secret refs resolved into env vars. The
	// original workload in Raft retains only the refs, never the values.
	placed, err := s.resolveSecrets(ctx, wl)
	if err != nil {
		wl.Phase = types.PhaseFailed
		_ = s.peer.ApplyWorkload(wl)
		return &gen.SubmitResponse{Accepted: false, Reason: "resolve secrets: " + err.Error()}, nil
	}

	if err := s.placeOnNode(ctx, nodeAddr, nodeCert, placed); err != nil {
		// Mark failed in Raft — the agent never ran it.
		wl.Phase = types.PhaseFailed
		_ = s.peer.ApplyWorkload(wl)
		return &gen.SubmitResponse{Accepted: false, Reason: err.Error()}, nil
	}

	wl.Phase = types.PhaseRunning
	_ = s.peer.ApplyWorkload(wl)
	return &gen.SubmitResponse{WorkloadId: wl.ID, Accepted: true}, nil
}

// resolveSecrets returns a shallow copy of wl with SecretRefs resolved into
// Env entries by fetching plaintext values from OpenBao.
func (s *Server) resolveSecrets(ctx context.Context, wl types.Workload) (types.Workload, error) {
	state := s.peer.State()

	bao, err := s.baoClient()
	if err != nil {
		// No secret refs = no problem; the error only matters if refs exist.
		if wl.Container != nil && len(wl.Container.SecretRefs) > 0 {
			return wl, err
		}
		if wl.Stack != nil && len(wl.Stack.SecretRefs) > 0 {
			return wl, err
		}
		return wl, nil
	}

	resolveRefs := func(refs map[string]string, env []string) ([]string, error) {
		if len(refs) == 0 {
			return env, nil
		}
		out := make([]string, len(env))
		copy(out, env)
		for envVar, secretName := range refs {
			var found *types.Secret
			for _, sec := range state.Secrets {
				if sec.Name == secretName {
					found = &sec
					break
				}
			}
			if found == nil {
				return nil, fmt.Errorf("secret %q not found", secretName)
			}
			plaintext, err := bao.Read(ctx, found.BaoPath)
			if err != nil {
				return nil, fmt.Errorf("fetch secret %q from OpenBao: %w", secretName, err)
			}
			out = append(out, envVar+"="+plaintext)
		}
		return out, nil
	}

	if wl.Container != nil {
		specCopy := *wl.Container
		resolved, err := resolveRefs(specCopy.SecretRefs, specCopy.Env)
		if err != nil {
			return wl, err
		}
		specCopy.Env = resolved
		wl.Container = &specCopy
	}
	if wl.Stack != nil {
		specCopy := *wl.Stack
		resolved, err := resolveRefs(specCopy.SecretRefs, nil)
		if err != nil {
			return wl, err
		}
		specCopy.ResolvedEnv = resolved
		wl.Stack = &specCopy
	}
	return wl, nil
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
	return s.pickNodeExcluding(nil)
}

// pickNodeExcluding selects the first healthy node that is not in the excluded
// set. Used by the replica fan-out to avoid placing two replicas on the same node.
func (s *Server) pickNodeExcluding(excluded map[string]bool) (nodeID, addr string, certDER []byte, err error) {
	for _, n := range s.peer.State().Nodes {
		if n.Status == types.NodeHealthy && !excluded[n.ID] {
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
	if req.ContainerFqdn == "" {
		return nil, status.Error(codes.InvalidArgument, "container_fqdn is required")
	}
	if req.ContainerPort == 0 {
		return nil, status.Error(codes.InvalidArgument, "container_port is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	rule := types.IngressRule{
		ID:            newID(),
		Host:          req.Host,
		PathPrefix:    req.PathPrefix,
		ContainerFQDN: req.ContainerFqdn,
		ContainerPort: req.ContainerPort,
		CreatedAt:     time.Now(),
	}
	if err := s.peer.ApplyIngress(rule); err != nil {
		return nil, status.Errorf(codes.Internal, "apply ingress: %v", err)
	}
	committed := s.peer.State().IngressRules[rule.ID]
	return &gen.CreateIngressResponse{RuleId: rule.ID, Accepted: true, SystemPort: committed.SystemPort}, nil
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
	if req.ContainerFqdn == "" {
		return nil, status.Error(codes.InvalidArgument, "container_fqdn is required")
	}
	if req.ContainerPort == 0 {
		return nil, status.Error(codes.InvalidArgument, "container_port is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	svc := types.Service{
		ID:            newID(),
		Name:          req.Name,
		ContainerFQDN: req.ContainerFqdn,
		ContainerPort: req.ContainerPort,
		CreatedAt:     time.Now(),
	}
	if err := s.peer.ApplyService(svc); err != nil {
		return nil, status.Errorf(codes.Internal, "apply service: %v", err)
	}
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

// ── Secrets ───────────────────────────────────────────────────────────────────

func (s *Server) CreateSecret(ctx context.Context, req *gen.CreateSecretRequest) (*gen.CreateSecretResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.Value == "" {
		return nil, status.Error(codes.InvalidArgument, "value is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	bao, err := s.baoClient()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "openbao not configured: %v", err)
	}
	if err := bao.Health(ctx); err != nil {
		return nil, status.Errorf(codes.Unavailable, "openbao unreachable: %v", err)
	}
	sec := types.Secret{
		ID:        newID(),
		Name:      req.Name,
		BaoPath:   "orchestrator/" + req.Name,
		CreatedAt: time.Now(),
	}
	if err := bao.Write(ctx, sec.BaoPath, req.Value); err != nil {
		return nil, status.Errorf(codes.Internal, "write to openbao: %v", err)
	}
	if err := s.peer.ApplySecret(sec); err != nil {
		// Best-effort: try to delete the value we just wrote so we don't leave orphans.
		_ = bao.Delete(ctx, sec.BaoPath)
		return nil, status.Errorf(codes.Internal, "apply secret to raft: %v", err)
	}
	return &gen.CreateSecretResponse{SecretId: sec.ID, Accepted: true}, nil
}

func (s *Server) DeleteSecret(ctx context.Context, req *gen.DeleteSecretRequest) (*gen.DeleteSecretResponse, error) {
	if req.SecretId == "" {
		return nil, status.Error(codes.InvalidArgument, "secret_id is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	state := s.peer.State()
	sec, ok := state.Secrets[req.SecretId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "secret %q not found", req.SecretId)
	}
	if bao, err := s.baoClient(); err == nil {
		_ = bao.Delete(ctx, sec.BaoPath) // best-effort; don't block Raft removal on OpenBao errors
	}
	if err := s.peer.RemoveSecret(req.SecretId); err != nil {
		return nil, status.Errorf(codes.Internal, "remove secret: %v", err)
	}
	return &gen.DeleteSecretResponse{Accepted: true}, nil
}

func (s *Server) ListSecrets(_ context.Context, _ *gen.ListSecretsRequest) (*gen.ListSecretsResponse, error) {
	state := s.peer.State()
	secrets := make([]*gen.Secret, 0, len(state.Secrets))
	for _, sec := range state.Secrets {
		secrets = append(secrets, types.SecretToProto(sec))
	}
	return &gen.ListSecretsResponse{Secrets: secrets}, nil
}

// ── OpenBao configuration ─────────────────────────────────────────────────────

func (s *Server) SetOpenBaoConfig(ctx context.Context, req *gen.SetOpenBaoConfigRequest) (*gen.SetOpenBaoConfigResponse, error) {
	if req.Address == "" {
		return nil, status.Error(codes.InvalidArgument, "address is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	cfg := types.OpenBaoConfig{
		Address: req.Address,
		Token:   req.Token,
		Mount:   req.Mount,
	}
	// Validate the connection before persisting.
	bao := baoclient.New(cfg.Address, cfg.Token, cfg.Mount)
	if err := bao.Health(ctx); err != nil {
		return &gen.SetOpenBaoConfigResponse{Accepted: false, Reason: "health check failed: " + err.Error()}, nil
	}
	if err := s.peer.SetOpenBaoConfig(cfg); err != nil {
		return nil, status.Errorf(codes.Internal, "store openbao config: %v", err)
	}
	return &gen.SetOpenBaoConfigResponse{Accepted: true}, nil
}

func (s *Server) GetOpenBaoStatus(ctx context.Context, _ *gen.GetOpenBaoStatusRequest) (*gen.GetOpenBaoStatusResponse, error) {
	cfg := s.peer.State().OpenBaoConfig
	if cfg == nil || cfg.Address == "" {
		return &gen.GetOpenBaoStatusResponse{Configured: false}, nil
	}
	resp := &gen.GetOpenBaoStatusResponse{
		Configured: true,
		Address:    cfg.Address,
		Mount:      cfg.Mount,
	}
	bao := baoclient.New(cfg.Address, cfg.Token, cfg.Mount)
	if err := bao.Health(ctx); err != nil {
		resp.Error = err.Error()
	} else {
		resp.Connected = true
	}
	return resp, nil
}

func phaseIn(phase types.WorkloadPhase, phases []gen.WorkloadPhase) bool {
	for _, p := range phases {
		if types.WorkloadPhase(p) == phase {
			return true
		}
	}
	return false
}
