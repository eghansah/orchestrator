package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
	"github.com/eghansah/orchestrator/internal/nerdctl"
	"github.com/eghansah/orchestrator/pkg/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const defaultPollInterval = 15 * time.Second

// Config holds construction parameters for Agent.
type Config struct {
	NodeID       string
	PollInterval time.Duration
	// StateUpdates receives a fresh ActualWorkloadState after every poll cycle.
	// Send is non-blocking; slow consumers lose updates.
	StateUpdates chan<- types.ActualWorkloadState
}

// Agent runs on every node. It wraps the nerdctl client, implements NodeServiceServer,
// and polls actual container state on a ticker.
type Agent struct {
	gen.UnimplementedNodeServiceServer

	id           string
	nc           *nerdctl.Client
	pollInterval time.Duration

	mu        sync.RWMutex
	states    map[string]types.ActualWorkloadState // nodeID → last reported state
	workloads map[string]types.Workload            // workloadID → Workload

	stateUpdates chan<- types.ActualWorkloadState
}

func New(nc *nerdctl.Client, cfg Config) *Agent {
	interval := cfg.PollInterval
	if interval == 0 {
		interval = defaultPollInterval
	}
	return &Agent{
		id:           cfg.NodeID,
		nc:           nc,
		pollInterval: interval,
		states:       make(map[string]types.ActualWorkloadState),
		workloads:    make(map[string]types.Workload),
		stateUpdates: cfg.StateUpdates,
	}
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.poll(ctx); err != nil {
				slog.Error("agent poll failed", "node", a.id, "err", err)
			}
		}
	}
}

func (a *Agent) poll(ctx context.Context) error {
	containers, err := a.nc.ListContainers(ctx)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	a.mu.RLock()
	localWorkloads := make(map[string]types.Workload, len(a.workloads))
	for k, v := range a.workloads {
		localWorkloads[k] = v
	}
	a.mu.RUnlock()

	var stacks []types.ActualStack
	for id, wl := range localWorkloads {
		if wl.Kind != types.KindStack || wl.Stack == nil {
			continue
		}
		svcs, err := a.nc.ComposePS(ctx, wl.Stack.Name)
		if err != nil {
			slog.Warn("compose ps failed", "stack", wl.Stack.Name, "err", err)
			continue
		}
		for i := range svcs {
			svcs[i].WorkloadID = id
		}
		stacks = append(stacks, types.ActualStack{
			WorkloadID: id,
			Name:       wl.Stack.Name,
			Services:   svcs,
		})
	}

	stats, _ := a.nc.Stats(ctx)
	// Match container stats to workload IDs using the containers list.
	containerToWorkload := make(map[string]string, len(containers))
	for _, c := range containers {
		containerToWorkload[c.ContainerID] = c.WorkloadID
	}
	for i := range stats {
		stats[i].WorkloadID = containerToWorkload[stats[i].ContainerID]
	}

	newState := types.ActualWorkloadState{
		NodeID:         a.id,
		Containers:     containers,
		Stacks:         stacks,
		Metrics:        nerdctl.CollectNodeMetrics(a.nc.DataDir()),
		ContainerStats: stats,
		ReportedAt:     time.Now(),
	}

	a.mu.Lock()
	a.states[a.id] = newState
	a.mu.Unlock()

	if a.stateUpdates != nil {
		select {
		case a.stateUpdates <- newState:
		default:
		}
	}
	return nil
}

// ContainerLogs returns the last tail lines of logs for the named container.
func (a *Agent) ContainerLogs(ctx context.Context, name string, tail int) (string, error) {
	return a.nc.ContainerLogs(ctx, name, tail)
}

// State returns this node's last observed actual state.
func (a *Agent) State() types.ActualWorkloadState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.states[a.id]
}

// AllStates returns a copy of actual state for every node that has reported.
func (a *Agent) AllStates() map[string]types.ActualWorkloadState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	cp := make(map[string]types.ActualWorkloadState, len(a.states))
	for k, v := range a.states {
		cp[k] = v
	}
	return cp
}

// ── NodeServiceServer implementation ─────────────────────────────────────────

func (a *Agent) Heartbeat(_ context.Context, _ *gen.HeartbeatRequest) (*gen.HeartbeatResponse, error) {
	return &gen.HeartbeatResponse{}, nil
}

func (a *Agent) ReportState(_ context.Context, req *gen.ReportStateRequest) (*gen.ReportStateResponse, error) {
	if req.State != nil {
		s := types.ActualWorkloadStateFromProto(req.State)
		a.mu.Lock()
		a.states[s.NodeID] = s
		a.mu.Unlock()
	}
	return &gen.ReportStateResponse{}, nil
}

func (a *Agent) PlaceWorkload(ctx context.Context, req *gen.PlaceWorkloadRequest) (*gen.PlaceWorkloadResponse, error) {
	if req.Workload == nil {
		return nil, status.Error(codes.InvalidArgument, "workload is required")
	}
	wl := types.WorkloadFromProto(req.Workload)

	var runErr error
	switch wl.Kind {
	case types.KindContainer:
		if wl.Container == nil {
			return nil, status.Error(codes.InvalidArgument, "container spec is required")
		}
		runErr = a.nc.RunContainer(ctx, wl.ID, *wl.Container, wl.PortAllocations)
	case types.KindStack:
		if wl.Stack == nil {
			return nil, status.Error(codes.InvalidArgument, "stack spec is required")
		}
		runErr = a.nc.ComposeUp(ctx, wl.ID, *wl.Stack)
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown workload kind")
	}

	if runErr != nil {
		return &gen.PlaceWorkloadResponse{Accepted: false, Reason: runErr.Error()}, nil
	}

	a.mu.Lock()
	a.workloads[wl.ID] = wl
	a.mu.Unlock()

	return &gen.PlaceWorkloadResponse{Accepted: true}, nil
}

func (a *Agent) RemoveWorkload(ctx context.Context, req *gen.RemoveWorkloadRequest) (*gen.RemoveWorkloadResponse, error) {
	if req.WorkloadId == "" {
		return nil, status.Error(codes.InvalidArgument, "workload_id is required")
	}

	a.mu.Lock()
	wl, ok := a.workloads[req.WorkloadId]
	if ok {
		delete(a.workloads, req.WorkloadId)
	}
	a.mu.Unlock()

	if !ok {
		return &gen.RemoveWorkloadResponse{Accepted: true}, nil // idempotent
	}

	var removeErr error
	switch wl.Kind {
	case types.KindContainer:
		if wl.Container != nil {
			removeErr = a.nc.RemoveContainer(ctx, wl.Container.Name)
		}
	case types.KindStack:
		if wl.Stack != nil {
			removeErr = a.nc.ComposeDown(ctx, wl.Stack.Name)
		}
	}

	if removeErr != nil {
		return &gen.RemoveWorkloadResponse{Accepted: false, Reason: removeErr.Error()}, nil
	}
	return &gen.RemoveWorkloadResponse{Accepted: true}, nil
}
