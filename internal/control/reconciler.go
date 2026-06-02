package control

import (
	"context"
	"log/slog"
	"time"

	"github.com/eghansah/orchestrator/internal/agent"
	"github.com/eghansah/orchestrator/pkg/types"
)

const reconcileInterval = 30 * time.Second

// Reconciler watches desired state in Raft and converges actual container state
// toward it. It only acts when running on the leader node.
type Reconciler struct {
	ctrl  *Server
	agent *agent.Agent
}

func NewReconciler(ctrl *Server, ag *agent.Agent) *Reconciler {
	return &Reconciler{ctrl: ctrl, agent: ag}
}

func (r *Reconciler) Run(ctx context.Context) {
	r.reconcile(ctx)
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

func (r *Reconciler) reconcile(ctx context.Context) {
	if !r.ctrl.peer.IsLeader() {
		return
	}
	state := r.ctrl.peer.State()
	actual := r.agent.AllStates()

	for _, wl := range state.Workloads {
		switch wl.Phase {
		case types.PhasePending, types.PhaseFailed:
			r.tryPlace(ctx, wl)
		case types.PhaseRunning:
			r.checkRunning(wl, actual)
		}
	}
}

func (r *Reconciler) tryPlace(ctx context.Context, wl types.Workload) {
	nodeID, addr, certDER, err := r.ctrl.pickNode()
	if err != nil {
		slog.Warn("reconciler: no healthy node", "workload", wl.ID, "err", err)
		return
	}

	wl.NodeID = nodeID
	wl.Phase = types.PhaseScheduled
	if err := r.ctrl.peer.ApplyWorkload(wl); err != nil {
		slog.Warn("reconciler: apply scheduled failed", "workload", wl.ID, "err", err)
		return
	}

	// Read back so we have FSM-assigned port allocations.
	committed, ok := r.ctrl.peer.State().Workloads[wl.ID]
	if !ok {
		slog.Warn("reconciler: workload vanished after apply", "workload", wl.ID)
		return
	}

	if err := r.ctrl.placeOnNode(ctx, addr, certDER, committed); err != nil {
		slog.Warn("reconciler: place failed", "workload", wl.ID, "node", nodeID, "err", err)
		committed.Phase = types.PhaseFailed
		_ = r.ctrl.peer.ApplyWorkload(committed)
		return
	}

	committed.Phase = types.PhaseRunning
	if err := r.ctrl.peer.ApplyWorkload(committed); err != nil {
		slog.Warn("reconciler: apply running failed", "workload", wl.ID, "err", err)
		return
	}
	slog.Info("reconciler: workload placed", "workload", wl.ID, "node", nodeID)
}

func (r *Reconciler) checkRunning(wl types.Workload, actual map[string]types.ActualWorkloadState) {
	nodeState, ok := actual[wl.NodeID]
	if !ok {
		return // no report from this node yet — assume still running
	}
	if time.Since(nodeState.ReportedAt) > 2*time.Minute {
		return // stale report — agent may be restarting; don't flip phase
	}

	found := false
	switch wl.Kind {
	case types.KindContainer:
		for _, c := range nodeState.Containers {
			if c.WorkloadID == wl.ID {
				found = true
				break
			}
		}
	case types.KindStack:
		for _, s := range nodeState.Stacks {
			if s.WorkloadID == wl.ID {
				found = true
				break
			}
		}
	}

	if !found {
		slog.Warn("reconciler: running workload absent from actual state; marking failed",
			"workload", wl.ID, "node", wl.NodeID)
		wl.Phase = types.PhaseFailed
		_ = r.ctrl.peer.ApplyWorkload(wl)
		// Next reconcile cycle will re-place it.
	}
}
