package control

import (
	"context"
	"fmt"
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
			if wl.GroupName != "" {
				// replica instance — place normally
				r.tryPlace(ctx, wl, nil)
			} else if replicas(wl) > 1 {
				// parent workload with replicas — fan out
				r.tryFanOut(ctx, wl)
			} else {
				r.tryPlace(ctx, wl, nil)
			}
		case types.PhaseRunning:
			r.checkRunning(wl, actual)
		}
	}
}

// tryFanOut creates N replica child workloads for a parent with Replicas > 1,
// spreading them across different nodes. The parent transitions to PhaseRunning
// as a template marker once all replicas are submitted (individual replica
// phases are tracked independently).
func (r *Reconciler) tryFanOut(ctx context.Context, parent types.Workload) {
	n := replicas(parent)
	groupName := parent.Name()
	if groupName == "" {
		groupName = parent.ID
	}

	used := map[string]bool{}
	for i := range n {
		nodeID, _, _, err := r.ctrl.pickNodeExcluding(used)
		if err != nil {
			slog.Warn("reconciler: fan-out ran out of healthy nodes",
				"workload", parent.ID, "replica", i, "err", err)
			break
		}
		used[nodeID] = true

		child := replicaChild(parent, i, groupName)
		child.NodeID = nodeID
		child.Phase = types.PhasePending
		if err := r.ctrl.peer.ApplyWorkload(child); err != nil {
			slog.Warn("reconciler: apply replica child failed",
				"workload", parent.ID, "replica", i, "err", err)
		}
	}

	// Mark parent as Running so it is not re-processed on the next cycle.
	parent.Phase = types.PhaseRunning
	if err := r.ctrl.peer.ApplyWorkload(parent); err != nil {
		slog.Warn("reconciler: apply parent running failed", "workload", parent.ID, "err", err)
	}
}

func (r *Reconciler) tryPlace(ctx context.Context, wl types.Workload, excluded map[string]bool) {
	nodeID, addr, certDER, err := r.ctrl.pickNodeExcluding(excluded)
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

	// Resolve config and secret refs into env vars/files before placing, the
	// same way scheduleAndPlace does for the initial submit — otherwise a
	// reconciler-driven re-placement (after a crash, reboot, or manual
	// removal) would run with the refs unresolved.
	placed, err := r.ctrl.resolveConfigRefs(committed)
	if err != nil {
		slog.Warn("reconciler: resolve config failed", "workload", wl.ID, "err", err)
		committed.Phase = types.PhaseFailed
		_ = r.ctrl.peer.ApplyWorkload(committed)
		return
	}
	placed, err = r.ctrl.resolveSecrets(ctx, placed)
	if err != nil {
		slog.Warn("reconciler: resolve secrets failed", "workload", wl.ID, "err", err)
		committed.Phase = types.PhaseFailed
		_ = r.ctrl.peer.ApplyWorkload(committed)
		return
	}

	if err := r.ctrl.placeOnNode(ctx, addr, certDER, placed); err != nil {
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

// replicas returns the desired replica count for a workload (minimum 1).
func replicas(wl types.Workload) int {
	if wl.Container != nil && wl.Container.Replicas > 1 {
		return wl.Container.Replicas
	}
	if wl.Stack != nil && wl.Stack.Replicas > 1 {
		return wl.Stack.Replicas
	}
	return 1
}

// replicaChild clones parent into a new leaf Workload with a disambiguated name
// and the given group name linking it back to the parent.
func replicaChild(parent types.Workload, index int, groupName string) types.Workload {
	child := parent
	child.ID = fmt.Sprintf("%s-r%d", parent.ID, index)
	child.GroupName = groupName
	if parent.Container != nil {
		spec := *parent.Container
		spec.Name = fmt.Sprintf("%s-%d", parent.Container.Name, index)
		spec.Replicas = 1
		child.Container = &spec
	}
	if parent.Stack != nil {
		spec := *parent.Stack
		spec.Name = fmt.Sprintf("%s-%d", parent.Stack.Name, index)
		spec.Replicas = 1
		child.Stack = &spec
	}
	return child
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
