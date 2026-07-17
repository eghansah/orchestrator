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
  protocol: string;
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
  secrets: Secret[];
  config_values: ConfigValue[];
  trusted_cas: TrustedCA[];
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
  container_fqdn: string;
  container_port: number;
  system_port: number;
  created_at: number; // unix seconds
}

export interface CreateIngressRequest {
  domain_id: string;
  path_prefix: string;
  container_fqdn: string;
  container_port: number;
}

export interface Service {
  id: string;
  name: string;
  container_fqdn: string;
  container_port: number;
  system_port: number;
  created_at: number; // unix seconds
}

export interface CreateServiceRequest {
  name: string;
  container_fqdn: string;
  container_port: number;
}

export interface Domain {
  id: string;
  name: string;
  tls_cert: string;
  tls_key?: string; // only present in create/regenerate responses
  csr?: string;     // PEM CSR; empty on domains created before this feature
  enabled: boolean;
  created_at: number; // unix seconds
}

export interface CreateDomainRequest {
  name: string;
  tls_cert?: string;
  tls_key?: string;
}

export interface CSRSubject {
  organization?: string;
  organizational_unit?: string;
  country?: string;
  state?: string;
  locality?: string;
  email_address?: string;
}

export interface User {
  id: string;
  username: string;
  enabled: boolean;
  mfa_enabled: boolean;
  created_at: number; // unix seconds
}

export type LoginStep1Response =
  | { status: "mfa_setup"; pending_token: string; secret: string; qr_uri: string }
  | { status: "mfa_required"; pending_token: string }
  | { token: string }; // bootstrap mode

export interface CreateUserRequest {
  username: string; // AD username; no password — authentication is handled by LDAP
}

export interface WorkloadTemplate {
  id: string;
  name: string;
  description: string;
  kind: "container" | "stack";
  compose_yaml?: string;
  image?: string;
  insecure_registry?: boolean;
  secret_refs?: Record<string, string>;
  config_refs?: Record<string, string>;
  created_at: number; // unix seconds
}

export interface CreateTemplateRequest {
  name: string;
  description?: string;
  kind: "container" | "stack";
  compose_yaml?: string;
  image?: string;
  command?: string[];
  env?: string[];
  ports?: { container_port: number; protocol: string }[];
  volumes?: { source: string; target: string; read_only: boolean }[];
  labels?: Record<string, string>;
  namespace?: string;
  insecure_registry?: boolean;
  secret_refs?: Record<string, string>;
  config_refs?: Record<string, string>;
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

export interface Secret {
  id: string;
  name: string;
  created_at: number; // unix seconds
}

export interface ConfigValue {
  id: string;
  name: string;
  value: string;
  created_at: number; // unix seconds
  updated_at: number; // unix seconds
}

export interface TrustedCA {
  id: string;
  label: string;
  pem: string;
  appliesToOpenBao: boolean;
  appliesToRegistries: boolean;
  notAfter?: number; // unix seconds
  expired: boolean;
  createdAt: number; // unix seconds
}

export interface CreateTrustedCARequest {
  label: string;
  pem: string;
  appliesToOpenBao: boolean;
  appliesToRegistries: boolean;
}

export interface UpdateTrustedCARequest {
  label: string;
  pem: string; // blank keeps the existing certificate
  appliesToOpenBao: boolean;
  appliesToRegistries: boolean;
}

export interface CreateSecretRequest {
  name: string;
  value: string;
}

export interface UpdateSecretRequest {
  value: string;
}

export interface CreateConfigValueRequest {
  name: string;
  value: string;
}

export interface UpdateConfigValueRequest {
  value: string;
}

export interface VersionInfo {
  version: string;
}

export interface ImportReport {
  imported: Record<string, number>;
  skipped?: string[];
  errors?: string[];
}

export interface OpenBaoStatus {
  configured: boolean;
  address?: string;
  mount?: string;
  insecureSkipVerify: boolean;
  roleId?: string;
  authMount?: string;
  connected: boolean;
  error?: string;
}

export interface SetOpenBaoConfigRequest {
  address: string;
  token: string;
  mount: string;
  insecureSkipVerify: boolean;
  roleId: string;
  secretId: string;
  authMount: string;
}

export interface BaoSealStatus {
  configured: boolean;
  reachable: boolean;
  error?: string;
  initialized: boolean;
  sealed: boolean;
  progress: number;
  threshold: number;
  shares: number;
}

export interface ContainerInspectResult {
  id: string;
  name: string;
  status: string;
  running: boolean;
  pid: number;
  started_at: string;
  image: string;
  env: string[];
  port_bindings: Record<string, { host_ip: string; host_port: string }[]>;
  networks: { name: string; ip_address: string; gateway: string; mac_address: string }[];
  mounts: { type: string; name: string; source: string; destination: string; mode: string; rw: boolean }[];
}

export interface NetworkListEntry {
  network_id: string;
  name: string;
  driver: string;
  ipv4: string;
  labels: string;
}

export interface NetworkInspectResult {
  name: string;
  id: string;
  driver: string;
  subnet: string;
  gateway: string;
  containers: { name: string; ipv4_address: string }[];
  labels: Record<string, string>;
}

export interface VolumeListEntry {
  name: string;
  driver: string;
  mountpoint: string;
  labels: string;
}

export interface VolumeInspectResult {
  name: string;
  driver: string;
  mountpoint: string;
  labels: Record<string, string>;
  scope: string;
}

export interface ChangelogSection {
  title: string;
  items: string[];
}

export interface ChangelogRelease {
  version: string;
  date: string;
  summary: string;
  sections: ChangelogSection[];
}

export interface SystemServiceInfo {
  name: string;
  role: string;
  kind: "container" | "process";
  status: "running" | "stopped" | "not found" | "unknown";
  controllable: boolean;
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

interface RequestOptions {
  body?: string;
  contentType?: string;
}

async function request<T>(method: string, path: string, jsonBody?: unknown, opts?: RequestOptions): Promise<T> {
  const headers: Record<string, string> = {};
  if (_token) headers["Authorization"] = `Bearer ${_token}`;

  let rawBody: string | undefined;
  if (opts?.body !== undefined) {
    rawBody = opts.body;
    headers["Content-Type"] = opts.contentType ?? "text/plain";
  } else if (jsonBody !== undefined) {
    rawBody = JSON.stringify(jsonBody);
    headers["Content-Type"] = "application/json";
  }

  const res = await fetch(getBasePath() + path, {
    method,
    headers,
    body: rawBody,
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
  login: async (username: string, password: string): Promise<LoginStep1Response> => {
    const res = await fetch(getBasePath() + "/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password }),
    });
    const data = await res.json();
    if (!res.ok) throw new Error((data as { error?: string }).error ?? `HTTP ${res.status}`);
    return data as LoginStep1Response;
  },
  verifyMFA: async (pendingToken: string, code: string): Promise<{ token: string }> => {
    const res = await fetch(getBasePath() + "/api/auth/mfa", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ pending_token: pendingToken, code }),
    });
    const data = await res.json();
    if (!res.ok) throw new Error((data as { error?: string }).error ?? `HTTP ${res.status}`);
    return data as { token: string };
  },
  logout: () => request<{ ok: boolean }>("POST", "/api/auth/logout"),
  getVersion: () => request<VersionInfo>("GET", "/api/version"),
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
    request<{ rule_id: string; system_port: number; accepted: boolean }>(
      "POST",
      "/api/ingress",
      req
    ),
  updateIngress: (id: string, req: CreateIngressRequest) =>
    request<IngressRule>("POST", `/api/ingress/${id}/update`, req),
  deleteIngress: (id: string) =>
    request<MutationResult>("POST", `/api/ingress/${id}/delete`),
  listServices: () => request<Service[]>("GET", "/api/services"),
  createService: (req: CreateServiceRequest) =>
    request<{ service_id: string; system_port: number; accepted: boolean; reason?: string }>(
      "POST",
      "/api/services",
      req
    ),
  updateService: (id: string, req: CreateServiceRequest) =>
    request<Service>("POST", `/api/services/${id}/update`, req),
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
  regenerateDomainKeys: (id: string, subj?: CSRSubject) =>
    request<Domain>("POST", `/api/domains/${id}/regenerate`, subj),
  importDomainCert: (id: string, cert: string) =>
    request<Domain>("POST", `/api/domains/${id}/import-cert`, { tls_cert: cert }),
  listUsers: () => request<User[]>("GET", "/api/users"),
  createUser: (req: CreateUserRequest) => request<User>("POST", "/api/users", req),
  toggleUser: (id: string) => request<User>("POST", `/api/users/${id}/toggle`),
  deleteUser: (id: string) =>
    request<{ accepted: boolean }>("POST", `/api/users/${id}/delete`),
  resetUserMFA: (id: string) =>
    request<{ ok: boolean }>("POST", `/api/users/${id}/reset-mfa`),
  listRegistries: () => request<Registry[]>("GET", "/api/registries"),
  createRegistry: (req: CreateRegistryRequest) =>
    request<Registry>("POST", "/api/registries", req),
  updateRegistry: (id: string, req: CreateRegistryRequest) =>
    request<Registry>("POST", `/api/registries/${id}/update`, req),
  deleteRegistry: (id: string) =>
    request<{ accepted: boolean }>("POST", `/api/registries/${id}/delete`),
  listTemplates: () => request<WorkloadTemplate[]>("GET", "/api/templates"),
  createTemplate: (req: CreateTemplateRequest) =>
    request<WorkloadTemplate>("POST", "/api/templates", req),
  updateTemplate: (id: string, req: CreateTemplateRequest) =>
    request<WorkloadTemplate>("POST", `/api/templates/${id}/update`, req),
  deleteTemplate: (id: string) =>
    request<{ accepted: boolean }>("POST", `/api/templates/${id}/delete`),
  deployTemplate: (id: string) =>
    request<{ accepted: boolean; workload_id?: string; reason?: string }>("POST", `/api/templates/${id}/deploy`),
  getContainerLogs: (name: string, tail = 200) =>
    request<{ logs: string }>("GET", `/api/containers/${encodeURIComponent(name)}/logs?tail=${tail}`),
  inspectContainer: (name: string) =>
    request<ContainerInspectResult>("GET", `/api/containers/${encodeURIComponent(name)}/inspect`),
  restartContainer: (name: string) =>
    request<{ ok: boolean }>("POST", `/api/containers/${encodeURIComponent(name)}/restart`),
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
  listSecrets: () => request<Secret[]>("GET", "/api/secrets"),
  createSecret: (req: CreateSecretRequest) =>
    request<Secret>("POST", "/api/secrets", req),
  deleteSecret: (id: string) =>
    request<{ accepted: boolean }>("POST", `/api/secrets/${id}/delete`),
  updateSecret: (id: string, req: UpdateSecretRequest) =>
    request<Secret>("POST", `/api/secrets/${id}/update`, req),
  listConfigValues: () => request<ConfigValue[]>("GET", "/api/config-values"),
  createConfigValue: (req: CreateConfigValueRequest) =>
    request<ConfigValue>("POST", "/api/config-values", req),
  updateConfigValue: (id: string, req: UpdateConfigValueRequest) =>
    request<ConfigValue>("POST", `/api/config-values/${id}/update`, req),
  deleteConfigValue: (id: string) =>
    request<{ accepted: boolean }>("POST", `/api/config-values/${id}/delete`),
  getOpenBaoStatus: () => request<OpenBaoStatus>("GET", "/api/openbao/status"),
  setOpenBaoConfig: (req: SetOpenBaoConfigRequest) =>
    request<{ accepted: boolean }>("POST", "/api/openbao/config", req),
  getBaoSealStatus: () => request<BaoSealStatus>("GET", "/api/openbao/seal-status"),
  unsealBao: (key: string) =>
    request<BaoSealStatus>("POST", "/api/openbao/unseal", { key }),
  listTrustedCAs: () => request<TrustedCA[]>("GET", "/api/trusted-cas"),
  createTrustedCA: (req: CreateTrustedCARequest) =>
    request<TrustedCA>("POST", "/api/trusted-cas", req),
  updateTrustedCA: (id: string, req: UpdateTrustedCARequest) =>
    request<TrustedCA>("POST", `/api/trusted-cas/${id}/update`, req),
  deleteTrustedCA: (id: string) =>
    request<{ accepted: boolean }>("POST", `/api/trusted-cas/${id}/delete`),
  adminCompact: () => request<{ accepted: boolean }>("POST", "/api/admin/compact"),
  listNetworks: () => request<NetworkListEntry[]>("GET", "/api/networks"),
  inspectNetwork: (name: string) =>
    request<NetworkInspectResult>("GET", `/api/networks/${encodeURIComponent(name)}/inspect`),
  listVolumes: () => request<VolumeListEntry[]>("GET", "/api/volumes"),
  inspectVolume: (name: string) =>
    request<VolumeInspectResult>("GET", `/api/volumes/${encodeURIComponent(name)}/inspect`),
  getChangelog: () => request<ChangelogRelease[]>("GET", "/api/system/changelog"),
  listSystemServices: () => request<SystemServiceInfo[]>("GET", "/api/system/services"),
  startSystemService: (name: string) =>
    request<{ ok: boolean }>("POST", `/api/system/services/${encodeURIComponent(name)}/start`),
  stopSystemService: (name: string) =>
    request<{ ok: boolean }>("POST", `/api/system/services/${encodeURIComponent(name)}/stop`),
  exportCluster: async (): Promise<Blob> => {
    const resp = await fetch(getBasePath() + "/api/export", {
      headers: { Authorization: `Bearer ${_token}` },
    });
    if (!resp.ok) {
      const text = await resp.text();
      throw new Error(`HTTP ${resp.status}: ${text}`);
    }
    return resp.blob();
  },
  importCluster: (yamlText: string, overwrite: boolean) =>
    request<ImportReport>("POST", `/api/import${overwrite ? "?overwrite=true" : ""}`, undefined, {
      body: yamlText,
      contentType: "application/yaml",
    }),
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
