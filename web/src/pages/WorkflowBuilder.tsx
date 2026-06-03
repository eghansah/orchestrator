import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Container from "@cloudscape-design/components/container";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import Multiselect from "@cloudscape-design/components/multiselect";
import Select, { SelectProps } from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { api, Registry } from "../api";

// ── Types ─────────────────────────────────────────────────────────────────────

interface PortEntry   { host: string; container: string; protocol: string }
interface EnvEntry    { key: string; value: string }
interface VolumeEntry { source: string; target: string }

interface ServiceDef {
  id: string;
  name: string;
  registryId: string;
  repoSearch: string;
  repos: string[];
  reposLoading: boolean;
  repo: string;
  tags: string[];
  tagsLoading: boolean;
  tag: string;
  image: string;
  ports: PortEntry[];
  env: EnvEntry[];
  volumes: VolumeEntry[];
  dependsOn: string[];
}

interface Props {
  onNavigate: (page: string) => void;
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function newServiceId(): string {
  return Math.random().toString(36).slice(2);
}

function emptyService(): ServiceDef {
  return {
    id: newServiceId(),
    name: "",
    registryId: "",
    repoSearch: "",
    repos: [],
    reposLoading: false,
    repo: "",
    tags: [],
    tagsLoading: false,
    tag: "",
    image: "",
    ports: [],
    env: [],
    volumes: [],
    dependsOn: [],
  };
}

function imageRef(registries: Registry[], registryId: string, repo: string, tag: string): string {
  const reg = registries.find((r) => r.id === registryId);
  const host = reg ? reg.url.replace(/^https?:\/\//, "").replace(/\/$/, "") : "";
  return host ? `${host}/${repo}:${tag}` : `${repo}:${tag}`;
}

function buildComposeYaml(stackName: string, services: ServiceDef[]): string {
  const lines: string[] = [`# ${stackName || "my-stack"}`, `version: "3.8"`, `services:`];
  for (const svc of services) {
    if (!svc.name) continue;
    lines.push(`  ${svc.name}:`);
    if (svc.image) lines.push(`    image: ${svc.image}`);
    const ports = svc.ports.filter((p) => p.container);
    if (ports.length) {
      lines.push(`    ports:`);
      ports.forEach((p) => {
        const host = p.host ? `127.0.0.1:${p.host}:` : "";
        lines.push(`      - "${host}${p.container}/${p.protocol}"`);
      });
    }
    const env = svc.env.filter((e) => e.key);
    if (env.length) {
      lines.push(`    environment:`);
      env.forEach((e) => lines.push(`      - ${e.key}=${e.value}`));
    }
    const vols = svc.volumes.filter((v) => v.source && v.target);
    if (vols.length) {
      lines.push(`    volumes:`);
      vols.forEach((v) => lines.push(`      - ${v.source}:${v.target}`));
    }
    if (svc.dependsOn.length) {
      lines.push(`    depends_on:`);
      svc.dependsOn.forEach((d) => lines.push(`      - ${d}`));
    }
  }
  return lines.join("\n") + "\n";
}

// ── Component ─────────────────────────────────────────────────────────────────

export default function WorkflowBuilder({ onNavigate }: Props) {
  const [stackName, setStackName] = useState("");
  const [services, setServices] = useState<ServiceDef[]>([emptyService()]);
  const [registries, setRegistries] = useState<Registry[]>([]);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const debounceTimers = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());

  const yaml = useMemo(() => buildComposeYaml(stackName, services), [stackName, services]);

  const loadRegistries = useCallback(async () => {
    try {
      const data = await api.listRegistries();
      setRegistries(data ?? []);
    } catch (e) {
      addFlash("error", "Load registries: " + String(e));
    }
  }, []);

  useEffect(() => { loadRegistries(); }, [loadRegistries]);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  function updateService(id: string, updates: Partial<ServiceDef>) {
    setServices((svcs) => svcs.map((s) => (s.id === id ? { ...s, ...updates } : s)));
  }

  function addService() {
    setServices((svcs) => [...svcs, emptyService()]);
  }

  function removeService(id: string) {
    const removedName = services.find((s) => s.id === id)?.name ?? "";
    setServices((svcs) =>
      svcs
        .filter((s) => s.id !== id)
        .map((s) => ({
          ...s,
          dependsOn: s.dependsOn.filter((d) => d !== removedName),
        }))
    );
  }

  // ── Image picker handlers ─────────────────────────────────────────────────

  function handleRepoFilter(svcId: string, registryId: string, query: string) {
    updateService(svcId, { repoSearch: query, repos: [], repo: "", tag: "", image: "" });
    const existing = debounceTimers.current.get(svcId);
    if (existing) clearTimeout(existing);
    if (!registryId || !query) return;
    updateService(svcId, { reposLoading: true });
    const timer = setTimeout(async () => {
      try {
        const data = await api.getRegistryCatalog(registryId, query);
        setServices((svcs) =>
          svcs.map((s) => (s.id === svcId ? { ...s, repos: data.repos ?? [], reposLoading: false } : s))
        );
      } catch {
        setServices((svcs) => svcs.map((s) => (s.id === svcId ? { ...s, reposLoading: false } : s)));
      }
    }, 300);
    debounceTimers.current.set(svcId, timer);
  }

  async function handleRepoSelect(svcId: string, registryId: string, repo: string) {
    updateService(svcId, { repo, repoSearch: repo, tags: [], tag: "", image: "", tagsLoading: true });
    try {
      const data = await api.getRepoTags(registryId, repo);
      setServices((svcs) =>
        svcs.map((s) => (s.id === svcId ? { ...s, tags: data.tags ?? [], tagsLoading: false } : s))
      );
    } catch {
      setServices((svcs) => svcs.map((s) => (s.id === svcId ? { ...s, tagsLoading: false } : s)));
    }
  }

  async function handleTagSelect(svcId: string, registryId: string, repo: string, tag: string) {
    const img = imageRef(registries, registryId, repo, tag);
    updateService(svcId, { tag, image: img });
    try {
      const data = await api.getImageEnv(registryId, repo, tag);
      const loaded = (data.env ?? []).map((kv) => {
        const eq = kv.indexOf("=");
        return eq >= 0 ? { key: kv.slice(0, eq), value: kv.slice(eq + 1) } : { key: kv, value: "" };
      });
      setServices((svcs) =>
        svcs.map((s) => {
          if (s.id !== svcId) return s;
          const existingKeys = new Set(s.env.map((e) => e.key));
          const toAdd = loaded.filter((e) => !existingKeys.has(e.key));
          return { ...s, env: [...s.env, ...toAdd] };
        })
      );
    } catch { /* env pre-population is best-effort */ }
  }

  // ── Submit ────────────────────────────────────────────────────────────────

  async function handleSubmit() {
    if (!stackName.trim()) { addFlash("error", "Stack name is required"); return; }
    const named = services.filter((s) => s.name && s.image);
    if (named.length === 0) { addFlash("error", "At least one service with an image is required"); return; }

    setSubmitting(true);
    try {
      const resp = await api.submitStack({ name: stackName.trim(), compose_yaml: yaml });
      if (!resp.accepted) { addFlash("error", resp.reason ?? "Rejected"); return; }
      addFlash("success", `Stack "${stackName}" submitted (${resp.workload_id})`);
      setTimeout(() => onNavigate("workloads"), 1200);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setSubmitting(false);
    }
  }

  // ── Render ────────────────────────────────────────────────────────────────

  const registryOptions: SelectProps.Option[] = registries.map((r) => ({
    value: r.id,
    label: r.name,
    description: r.url,
  }));

  const serviceNames = services.map((s) => s.name).filter(Boolean);

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="Build a multi-service compose stack by selecting images from configured registries."
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button variant="link" onClick={() => onNavigate("workloads")}>Cancel</Button>
              <Button variant="primary" loading={submitting} onClick={handleSubmit}>
                Submit as stack
              </Button>
            </SpaceBetween>
          }
        >
          Workflow Builder
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Flashbar items={flash} />

        {/* ── Stack name ──────────────────────────────────────────────────── */}
        <Container header={<Header variant="h2">Stack</Header>}>
          <FormField label="Stack name" description="Unique name for this workload">
            <Input
              value={stackName}
              onChange={(e) => setStackName(e.detail.value)}
              placeholder="my-app"
            />
          </FormField>
        </Container>

        {/* ── Services ────────────────────────────────────────────────────── */}
        {services.map((svc) => (
          <Container
            key={svc.id}
            header={
              <Header
                variant="h2"
                actions={
                  <Button
                    iconName="close"
                    variant="icon"
                    onClick={() => removeService(svc.id)}
                    disabled={services.length === 1}
                  />
                }
              >
                {svc.name || "Unnamed service"}
              </Header>
            }
          >
            <SpaceBetween size="m">
              {/* Service name */}
              <FormField label="Service name" description="Used as the compose service key">
                <Input
                  value={svc.name}
                  onChange={(e) => updateService(svc.id, { name: e.detail.value })}
                  placeholder="web"
                />
              </FormField>

              {/* Image picker */}
              <SpaceBetween size="s">
                <FormField label="Registry">
                  <Select
                    options={registryOptions}
                    selectedOption={registryOptions.find((o) => o.value === svc.registryId) ?? null}
                    onChange={(e) => {
                      updateService(svc.id, {
                        registryId: e.detail.selectedOption.value ?? "",
                        repoSearch: "", repos: [], repo: "", tags: [], tag: "", image: "",
                      });
                    }}
                    placeholder="Select registry"
                    empty="No registries configured"
                  />
                </FormField>

                <FormField label="Repository" description="Type to search images in the registry">
                  <Select
                    options={svc.repos.map((r) => ({ value: r, label: r }))}
                    selectedOption={svc.repo ? { value: svc.repo, label: svc.repo } : null}
                    onChange={(e) =>
                      handleRepoSelect(svc.id, svc.registryId, e.detail.selectedOption.value ?? "")
                    }
                    onLoadItems={(e) => handleRepoFilter(svc.id, svc.registryId, e.detail.filteringText)}
                    filteringType="manual"
                    filteringPlaceholder="Search repositories…"
                    statusType={svc.reposLoading ? "loading" : "finished"}
                    loadingText="Searching…"
                    disabled={!svc.registryId}
                    placeholder="Search and select a repository"
                    empty="No repositories found"
                  />
                </FormField>

                <FormField label="Tag">
                  <Select
                    options={svc.tags.map((t) => ({ value: t, label: t }))}
                    selectedOption={svc.tag ? { value: svc.tag, label: svc.tag } : null}
                    onChange={(e) =>
                      handleTagSelect(svc.id, svc.registryId, svc.repo, e.detail.selectedOption.value ?? "")
                    }
                    statusType={svc.tagsLoading ? "loading" : "finished"}
                    loadingText="Loading tags…"
                    disabled={!svc.repo}
                    placeholder={svc.repo ? "Select tag" : "Select a repository first"}
                    empty="No tags found"
                  />
                </FormField>

                {svc.image && (
                  <Box color="text-body-secondary" fontSize="body-s">
                    Image: <code>{svc.image}</code>
                  </Box>
                )}
              </SpaceBetween>

              {/* Ports */}
              <FormField
                label="Ports"
                description="HOST:CONTAINER/protocol — leave host blank for auto-assignment"
              >
                <SpaceBetween size="xs">
                  {svc.ports.map((p, i) => (
                    <SpaceBetween key={i} direction="horizontal" size="xs">
                      <Input
                        value={p.host}
                        onChange={(e) => {
                          const ports = [...svc.ports];
                          ports[i] = { ...p, host: e.detail.value };
                          updateService(svc.id, { ports });
                        }}
                        placeholder="Host port"
                      />
                      <Input
                        value={p.container}
                        onChange={(e) => {
                          const ports = [...svc.ports];
                          ports[i] = { ...p, container: e.detail.value };
                          updateService(svc.id, { ports });
                        }}
                        placeholder="Container port"
                      />
                      <Select
                        options={[{ value: "tcp", label: "tcp" }, { value: "udp", label: "udp" }]}
                        selectedOption={{ value: p.protocol, label: p.protocol }}
                        onChange={(e) => {
                          const ports = [...svc.ports];
                          ports[i] = { ...p, protocol: e.detail.selectedOption.value ?? "tcp" };
                          updateService(svc.id, { ports });
                        }}
                      />
                      <Button
                        iconName="close"
                        variant="icon"
                        onClick={() => updateService(svc.id, { ports: svc.ports.filter((_, j) => j !== i) })}
                      />
                    </SpaceBetween>
                  ))}
                  <Button
                    iconName="add-plus"
                    onClick={() => updateService(svc.id, { ports: [...svc.ports, { host: "", container: "", protocol: "tcp" }] })}
                  >
                    Add port
                  </Button>
                </SpaceBetween>
              </FormField>

              {/* Environment variables */}
              <FormField
                label="Environment variables"
                description="KEY=VALUE pairs — pre-populated from the image defaults"
              >
                <SpaceBetween size="xs">
                  {svc.env.map((e, i) => (
                    <SpaceBetween key={i} direction="horizontal" size="xs">
                      <Input
                        value={e.key}
                        onChange={(ev) => {
                          const env = [...svc.env];
                          env[i] = { ...e, key: ev.detail.value };
                          updateService(svc.id, { env });
                        }}
                        placeholder="KEY"
                      />
                      <Input
                        value={e.value}
                        onChange={(ev) => {
                          const env = [...svc.env];
                          env[i] = { ...e, value: ev.detail.value };
                          updateService(svc.id, { env });
                        }}
                        placeholder="value"
                      />
                      <Button
                        iconName="close"
                        variant="icon"
                        onClick={() => updateService(svc.id, { env: svc.env.filter((_, j) => j !== i) })}
                      />
                    </SpaceBetween>
                  ))}
                  <Button
                    iconName="add-plus"
                    onClick={() => updateService(svc.id, { env: [...svc.env, { key: "", value: "" }] })}
                  >
                    Add variable
                  </Button>
                </SpaceBetween>
              </FormField>

              {/* Volumes */}
              <FormField label="Volumes" description="SOURCE:TARGET mount paths">
                <SpaceBetween size="xs">
                  {svc.volumes.map((v, i) => (
                    <SpaceBetween key={i} direction="horizontal" size="xs">
                      <Input
                        value={v.source}
                        onChange={(e) => {
                          const volumes = [...svc.volumes];
                          volumes[i] = { ...v, source: e.detail.value };
                          updateService(svc.id, { volumes });
                        }}
                        placeholder="./host-path"
                      />
                      <Input
                        value={v.target}
                        onChange={(e) => {
                          const volumes = [...svc.volumes];
                          volumes[i] = { ...v, target: e.detail.value };
                          updateService(svc.id, { volumes });
                        }}
                        placeholder="/container/path"
                      />
                      <Button
                        iconName="close"
                        variant="icon"
                        onClick={() => updateService(svc.id, { volumes: svc.volumes.filter((_, j) => j !== i) })}
                      />
                    </SpaceBetween>
                  ))}
                  <Button
                    iconName="add-plus"
                    onClick={() => updateService(svc.id, { volumes: [...svc.volumes, { source: "", target: "" }] })}
                  >
                    Add volume
                  </Button>
                </SpaceBetween>
              </FormField>

              {/* Depends on */}
              <FormField label="Depends on" description="Services that must start before this one">
                <Multiselect
                  options={serviceNames
                    .filter((n) => n !== svc.name)
                    .map((n) => ({ value: n, label: n }))}
                  selectedOptions={svc.dependsOn.map((n) => ({ value: n, label: n }))}
                  onChange={(e) =>
                    updateService(svc.id, { dependsOn: e.detail.selectedOptions.map((o) => o.value ?? "") })
                  }
                  placeholder="Select services"
                  empty="No other services defined"
                />
              </FormField>
            </SpaceBetween>
          </Container>
        ))}

        <Button iconName="add-plus" onClick={addService}>
          Add service
        </Button>

        {/* ── YAML preview ────────────────────────────────────────────────── */}
        <Container
          header={
            <Header
              variant="h2"
              actions={
                <Button
                  iconName="copy"
                  onClick={() => navigator.clipboard.writeText(yaml)}
                >
                  Copy
                </Button>
              }
            >
              YAML Preview
            </Header>
          }
        >
          <pre style={{ margin: 0, fontFamily: "monospace", fontSize: "0.85rem", whiteSpace: "pre-wrap" }}>
            {yaml}
          </pre>
        </Container>

        {/* ── Footer actions ──────────────────────────────────────────────── */}
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => onNavigate("workloads")}>Cancel</Button>
            <Button variant="primary" loading={submitting} onClick={handleSubmit}>
              Submit as stack
            </Button>
          </SpaceBetween>
        </Box>
      </SpaceBetween>
    </ContentLayout>
  );
}
