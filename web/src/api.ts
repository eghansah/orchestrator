import { useCallback, useEffect, useState } from "react";

// ── Types ─────────────────────────────────────────────────────────────────────

export interface NodeInfo {
  id: string;
  address: string;
  status: "healthy" | "draining" | "unreachable";
  cpu_cores: number;
}

export interface WorkloadInfo {
  id: string;
  node_id: string;
  phase: "pending" | "scheduled" | "running" | "stopped" | "failed";
  kind: "container" | "stack";
  name: string;
  created_at: number; // unix seconds
}

export interface ClusterState {
  leader_id: string;
  is_leader: boolean;
  leader_addr: string;
  nodes: NodeInfo[];
  workloads: WorkloadInfo[];
}

export interface MutationResult {
  accepted: boolean;
  workload_id?: string;
  reason?: string;
  error?: string;
}

export interface RunRequest {
  name: string;
  image: string;
  command: string[];
  env: string[];
  ports: string[];
}

export interface StackRequest {
  name: string;
  compose_yaml: string;
}

// ── REST client ───────────────────────────────────────────────────────────────

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json();
  if (!res.ok) {
    throw new Error((data as { error?: string }).error ?? `HTTP ${res.status}`);
  }
  return data as T;
}

export const api = {
  getState: () => request<ClusterState>("GET", "/api/state"),
  submitContainer: (req: RunRequest) =>
    request<MutationResult>("POST", "/api/workloads/run", req),
  submitStack: (req: StackRequest) =>
    request<MutationResult>("POST", "/api/workloads/stack", req),
  removeWorkload: (id: string) =>
    request<MutationResult>("POST", `/api/workloads/${id}/remove`),
  drainNode: (id: string) =>
    request<MutationResult>("POST", `/api/nodes/${id}/drain`),
};

// ── useClusterState hook ──────────────────────────────────────────────────────

export function useClusterState(intervalMs = 5000) {
  const [state, setState] = useState<ClusterState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const refetch = useCallback(async () => {
    try {
      const data = await api.getState();
      setState(data);
      setError(null);
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refetch();
    const id = setInterval(refetch, intervalMs);
    return () => clearInterval(id);
  }, [refetch, intervalMs]);

  return { state, error, loading, refetch };
}

// ── Utilities ─────────────────────────────────────────────────────────────────

export function formatAge(unixSec: number): string {
  if (!unixSec) return "—";
  const secs = Math.floor(Date.now() / 1000) - unixSec;
  if (secs < 60) return `${secs}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m${secs % 60}s`;
  if (secs < 86400)
    return `${Math.floor(secs / 3600)}h${Math.floor((secs % 3600) / 60)}m`;
  return `${Math.floor(secs / 86400)}d${Math.floor((secs % 86400) / 3600)}h`;
}
