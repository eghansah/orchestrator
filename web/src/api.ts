import { useCallback, useEffect, useState } from "react";

// ── Types ─────────────────────────────────────────────────────────────────────

export interface NodeInfo {
  id: string;
  address: string;
  status: "healthy" | "draining" | "unreachable";
  cpu_cores: number;
  memory_bytes: number;
  disk_bytes: number;
  data_ip: string;
  last_seen_at: number; // unix seconds
  mem_total_bytes: number;
  mem_used_bytes: number;
  disk_total_bytes: number;
  disk_used_bytes: number;
}

export interface ContainerStats {
  workload_id: string;
  container_id: string;
  name: string;
  cpu_percent: number;
  mem_used_bytes: number;
  mem_limit_bytes: number;
}

export interface PortAllocation {
  container_port: number;
  allocated_port: number;
}

export interface WorkloadInfo {
  id: string;
  node_id: string;
  phase: "pending" | "scheduled" | "running" | "stopped" | "failed";
  kind: "container" | "stack";
  name: string;
  created_at: number; // unix seconds
  port_allocations: PortAllocation[];
}

export interface ActualContainer {
  workload_id: string;
  container_id: string;
  name: string;
  status: string; // raw nerdctl status, e.g. "Up 2 minutes"
  started_at: number; // unix seconds, 0 if not running
}

export interface ActualStack {
  workload_id: string;
  name: string;
  services: ActualContainer[];
}

export interface ClusterState {
  leader_id: string;
  is_leader: boolean;
  leader_addr: string;
  nodes: NodeInfo[];
  workloads: WorkloadInfo[];
  actual_containers: ActualContainer[];
  actual_stacks: ActualStack[];
  container_stats: ContainerStats[];
  registries: Registry[];
}

export interface MutationResult {
  accepted: boolean;
  workload_id?: string;
  reason?: string;
  error?: string;
}

export interface WorkloadSpec {
  id: string;
  kind: "container" | "stack";
  name: string;
  image?: string;
  command?: string[];
  env?: string[];
  ports?: { container_port: number; protocol: string }[];
  volumes?: { source: string; target: string; read_only: boolean }[];
  labels?: Record<string, string>;
  namespace?: string;
  compose_yaml?: string;
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

export interface IngressRule {
  id: string;
  domain_id: string;
  host: string;
  path_prefix: string;
  workload_id: string;
  port: number;
  created_at: number; // unix seconds
}

export interface CreateIngressRequest {
  domain_id: string;
  path_prefix: string;
  workload_id: string;
  port: number;
}

export interface Service {
  id: string;
  name: string;
  workload_id: string;
  target_port: number;
  system_port: number;
  created_at: number; // unix seconds
}

export interface CreateServiceRequest {
  name: string;
  workload_id: string;
  target_port: number;
}

export interface Domain {
  id: string;
  name: string;
  tls_cert: string;
  tls_key?: string; // only present in the create response; omitted from list/update/toggle
  enabled: boolean;
  created_at: number; // unix seconds
}

export interface CreateDomainRequest {
  name: string;
  tls_cert?: string;
  tls_key?: string;
}

export interface User {
  id: string;
  username: string;
  enabled: boolean;
  created_at: number; // unix seconds
}

export interface CreateUserRequest {
  username: string; // AD username; no password — authentication is handled by LDAP
}

export interface Registry {
  id: string;
  name: string;
  url: string;
  username: string;
  created_at: number; // unix seconds
}

export interface CreateRegistryRequest {
  name: string;
  url: string;
  username: string;
  password: string;
}

// ── Auth token ────────────────────────────────────────────────────────────────

const TOKEN_KEY = "orchestrator_token";

let _token: string = localStorage.getItem(TOKEN_KEY) ?? "";
let _onAuthError: (() => void) | null = null;

export function setToken(t: string) {
  _token = t;
  if (t) localStorage.setItem(TOKEN_KEY, t);
  else localStorage.removeItem(TOKEN_KEY);
}

export function getToken(): string { return _token; }
export function hasToken(): boolean { return !!_token; }

/** Called by App to be notified when any API call returns 401. */
export function setAuthErrorHandler(fn: () => void) { _onAuthError = fn; }

export class AuthError extends Error {}

// ── Base path ─────────────────────────────────────────────────────────────────

// Reads the prefix injected by the Go server via <meta name="base-path">.
// Returns "" in dev mode (Vite serves index.html with an empty content value).
function getBasePath(): string {
  const meta = document.querySelector<HTMLMetaElement>('meta[name="base-path"]');
  return meta?.content?.replace(/\/$/, "") ?? "";
}

// ── REST client ───────────────────────────────────────────────────────────────

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body) headers["Content-Type"] = "application/json";
  if (_token) headers["Authorization"] = `Bearer ${_token}`;

  const res = await fetch(getBasePath() + path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });

  if (res.status === 401) {
    setToken("");
    _onAuthError?.();
    throw new AuthError("Unauthorized — token invalid or expired");
  }

  const data = await res.json();
  if (!res.ok) {
    throw new Error((data as { error?: string }).error ?? `HTTP ${res.status}`);
  }
  return data as T;
}

export const api = {
  login: async (username: string, password: string): Promise<{ token: string }> => {
    const res = await fetch(getBasePath() + "/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password }),
    });
    const data = await res.json();
    if (!res.ok) throw new Error((data as { error?: string }).error ?? `HTTP ${res.status}`);
    return data as { token: string };
  },
  logout: () => request<{ ok: boolean }>("POST", "/api/auth/logout"),
  getState: () => request<ClusterState>("GET", "/api/state"),
  getWorkload: (id: string) => request<WorkloadSpec>("GET", `/api/workloads/${id}`),
  submitContainer: (req: RunRequest) =>
    request<MutationResult>("POST", "/api/workloads/run", req),
  submitStack: (req: StackRequest) =>
    request<MutationResult>("POST", "/api/workloads/stack", req),
  removeWorkload: (id: string) =>
    request<MutationResult>("POST", `/api/workloads/${id}/remove`),
  drainNode: (id: string) =>
    request<MutationResult>("POST", `/api/nodes/${id}/drain`),
  listIngress: () => request<IngressRule[]>("GET", "/api/ingress"),
  createIngress: (req: CreateIngressRequest) =>
    request<{ rule_id: string; accepted: boolean }>(
      "POST",
      "/api/ingress",
      req
    ),
  deleteIngress: (id: string) =>
    request<MutationResult>("POST", `/api/ingress/${id}/delete`),
  listServices: () => request<Service[]>("GET", "/api/services"),
  createService: (req: CreateServiceRequest) =>
    request<{ service_id: string; system_port: number; accepted: boolean; reason?: string }>(
      "POST",
      "/api/services",
      req
    ),
  deleteService: (id: string) =>
    request<MutationResult>("POST", `/api/services/${id}/delete`),
  listDomains: () => request<Domain[]>("GET", "/api/domains"),
  createDomain: (req: CreateDomainRequest) =>
    request<Domain>("POST", "/api/domains", req),
  updateDomain: (id: string, req: CreateDomainRequest) =>
    request<Domain>("POST", `/api/domains/${id}/update`, req),
  toggleDomain: (id: string) =>
    request<Domain>("POST", `/api/domains/${id}/toggle`),
  deleteDomain: (id: string) =>
    request<{ accepted: boolean }>("POST", `/api/domains/${id}/delete`),
  listUsers: () => request<User[]>("GET", "/api/users"),
  createUser: (req: CreateUserRequest) => request<User>("POST", "/api/users", req),
  toggleUser: (id: string) => request<User>("POST", `/api/users/${id}/toggle`),
  deleteUser: (id: string) =>
    request<{ accepted: boolean }>("POST", `/api/users/${id}/delete`),
  listRegistries: () => request<Registry[]>("GET", "/api/registries"),
  createRegistry: (req: CreateRegistryRequest) =>
    request<Registry>("POST", "/api/registries", req),
  deleteRegistry: (id: string) =>
    request<{ accepted: boolean }>("POST", `/api/registries/${id}/delete`),
  getRegistryCatalog: (id: string, search?: string) =>
    request<{ repos: string[] }>(
      "GET",
      `/api/registries/${id}/catalog${search ? `?search=${encodeURIComponent(search)}` : ""}`
    ),
  getRepoTags: (id: string, repo: string) =>
    request<{ tags: string[] }>(
      "GET",
      `/api/registries/${id}/tags?repo=${encodeURIComponent(repo)}`
    ),
  getImageEnv: (id: string, repo: string, tag: string) =>
    request<{ env: string[] }>(
      "GET",
      `/api/registries/${id}/env?repo=${encodeURIComponent(repo)}&tag=${encodeURIComponent(tag)}`
    ),
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
