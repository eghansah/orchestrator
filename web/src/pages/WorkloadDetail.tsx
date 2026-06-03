import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import ColumnLayout from "@cloudscape-design/components/column-layout";
import Container from "@cloudscape-design/components/container";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import Modal from "@cloudscape-design/components/modal";
import SpaceBetween from "@cloudscape-design/components/space-between";
import StatusIndicator, {
  StatusIndicatorProps,
} from "@cloudscape-design/components/status-indicator";
import Table from "@cloudscape-design/components/table";
import { useEffect, useState } from "react";
import { api, ClusterState, ContainerStats, formatAge, WorkloadSpec } from "../api";

function formatBytes(b: number): string {
  if (!b) return "—";
  if (b >= 1_073_741_824) return `${(b / 1_073_741_824).toFixed(1)} GiB`;
  if (b >= 1_048_576)     return `${(b / 1_048_576).toFixed(1)} MiB`;
  return `${b} B`;
}

interface Props {
  workloadId: string;
  state: ClusterState | null;
  loading: boolean;
  error: string | null;
  refetch: () => void;
  onNavigate: (page: string) => void;
}

function phaseStatus(phase: string): StatusIndicatorProps.Type {
  switch (phase) {
    case "running":   return "success";
    case "failed":    return "error";
    case "pending":   return "pending";
    case "scheduled": return "in-progress";
    case "stopped":   return "stopped";
    default:          return "info";
  }
}

function specToYaml(spec: WorkloadSpec): string {
  if (spec.kind === "stack") {
    const indented = (spec.compose_yaml ?? "")
      .split("\n")
      .map((l) => `  ${l}`)
      .join("\n");
    return `kind: stack\nname: ${spec.name}\ncompose_yaml: |\n${indented}\n`;
  }

  const lines: string[] = [`kind: container`, `name: ${spec.name}`];
  if (spec.image) lines.push(`image: ${spec.image}`);
  if (spec.command?.length) {
    lines.push(`command:`);
    spec.command.forEach((c) => lines.push(`  - ${c}`));
  }
  if (spec.env?.length) {
    lines.push(`env:`);
    spec.env.forEach((e) => lines.push(`  - ${e}`));
  }
  if (spec.ports?.length) {
    lines.push(`ports:`);
    spec.ports.forEach((p) =>
      lines.push(`  - container_port: ${p.container_port}\n    protocol: ${p.protocol}`)
    );
  }
  if (spec.volumes?.length) {
    lines.push(`volumes:`);
    spec.volumes.forEach((v) =>
      lines.push(
        `  - source: ${v.source}\n    target: ${v.target}\n    read_only: ${v.read_only}`
      )
    );
  }
  if (spec.labels && Object.keys(spec.labels).length) {
    lines.push(`labels:`);
    Object.entries(spec.labels).forEach(([k, v]) => lines.push(`  ${k}: ${v}`));
  }
  if (spec.namespace) lines.push(`namespace: ${spec.namespace}`);
  return lines.join("\n") + "\n";
}

function downloadYaml(name: string, content: string) {
  const blob = new Blob([content], { type: "text/yaml" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${name}.yaml`;
  a.click();
  URL.revokeObjectURL(url);
}

export default function WorkloadDetail({ workloadId, state, loading, onNavigate, refetch }: Props) {
  const workload = state?.workloads.find((w) => w.id === workloadId) ?? null;
  const [spec, setSpec] = useState<WorkloadSpec | null>(null);
  const [copied, setCopied] = useState(false);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [showSaveTemplate, setShowSaveTemplate] = useState(false);
  const [templateName, setTemplateName] = useState("");
  const [templateDesc, setTemplateDesc] = useState("");
  const [savingTemplate, setSavingTemplate] = useState(false);

  useEffect(() => {
    setSpec(null);
    api.getWorkload(workloadId).then(setSpec).catch(() => {});
  }, [workloadId]);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [...f, { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) }]);
  }

  async function handleSaveTemplate() {
    if (!spec) return;
    setSavingTemplate(true);
    try {
      await api.createTemplate({
        name: templateName || spec.name,
        description: templateDesc,
        kind: spec.kind,
        compose_yaml: spec.compose_yaml,
        image: spec.image,
        command: spec.command,
        env: spec.env,
        ports: spec.ports,
        volumes: spec.volumes,
        labels: spec.labels,
        namespace: spec.namespace,
      });
      addFlash("success", `Saved as template "${templateName || spec.name}"`);
      setShowSaveTemplate(false);
      setTemplateName("");
      setTemplateDesc("");
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setSavingTemplate(false);
    }
  }

  const containers = (state?.actual_containers ?? []).filter(
    (c) => c.workload_id === workloadId
  );

  const statsByContainerId = new Map<string, ContainerStats>(
    (state?.container_stats ?? []).map((cs) => [cs.container_id, cs])
  );
  const stacks = (state?.actual_stacks ?? []).filter(
    (s) => s.workload_id === workloadId
  );

  const hasActual = containers.length > 0 || stacks.length > 0;

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={workload ? `${workload.kind} · ${workload.node_id || "unscheduled"}` : ""}
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button iconName="refresh" onClick={refetch}>Refresh</Button>
              {spec && (
                <Button iconName="upload" onClick={() => { setTemplateName(spec.name); setShowSaveTemplate(true); }}>
                  Save as template
                </Button>
              )}
              <Button iconName="angle-left" variant="link" onClick={() => onNavigate("workloads")}>
                Back to Workloads
              </Button>
            </SpaceBetween>
          }
        >
          {workload ? workload.name : workloadId}
          {workload && (
            <>
              {" "}
              <StatusIndicator type={phaseStatus(workload.phase)}>
                {workload.phase}
              </StatusIndicator>
            </>
          )}
        </Header>
      }
    >
      <Flashbar items={flash} />

      {/* ── Save as template modal ────────────────────────────────────────── */}
      <Modal
        visible={showSaveTemplate}
        onDismiss={() => setShowSaveTemplate(false)}
        header="Save as template"
        footer={
          <Box float="right">
            <SpaceBetween direction="horizontal" size="xs">
              <Button variant="link" onClick={() => setShowSaveTemplate(false)}>Cancel</Button>
              <Button variant="primary" loading={savingTemplate} onClick={handleSaveTemplate}>Save</Button>
            </SpaceBetween>
          </Box>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Template name">
            <Input value={templateName} onChange={(e) => setTemplateName(e.detail.value)} />
          </FormField>
          <FormField label="Description" constraintText="Optional">
            <Input value={templateDesc} onChange={(e) => setTemplateDesc(e.detail.value)} />
          </FormField>
        </SpaceBetween>
      </Modal>

      {loading && !state ? (
        <Box>Loading…</Box>
      ) : !workload ? (
        <Box>Workload not found.</Box>
      ) : (
        <SpaceBetween size="l">
          {/* ── Metadata ─────────────────────────────────────────────────── */}
          <Container header={<Header variant="h2">Details</Header>}>
            <ColumnLayout columns={3} variant="text-grid">
              <div>
                <Box variant="awsui-key-label">ID</Box>
                <Box>{workload.id}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Kind</Box>
                <Box>{workload.kind}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Node</Box>
                <Box>{workload.node_id || "—"}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Phase</Box>
                <StatusIndicator type={phaseStatus(workload.phase)}>
                  {workload.phase}
                </StatusIndicator>
              </div>
              <div>
                <Box variant="awsui-key-label">Age</Box>
                <Box>{formatAge(workload.created_at)}</Box>
              </div>
            </ColumnLayout>
          </Container>

          {/* ── Published Ports (container workloads only) ────────────────── */}
          {workload.kind === "container" && (() => {
            // Merge declared ports (spec) with allocated host ports (state).
            const allocMap = new Map(
              workload.port_allocations.map((pa) => [pa.container_port, pa])
            );
            const declaredPorts = spec?.ports ?? [];
            // Include any allocation that isn't in the declared list (shouldn't
            // normally happen but is safe to show).
            const extraAllocs = workload.port_allocations.filter(
              (pa) => !declaredPorts.some((p) => p.container_port === pa.container_port)
            );
            type PortRow = { container_port: number; protocol: string; allocated_port: number | null };
            const rows: PortRow[] = [
              ...declaredPorts.map((p) => ({
                container_port: p.container_port,
                protocol: p.protocol || "tcp",
                allocated_port: allocMap.get(p.container_port)?.allocated_port ?? null,
              })),
              ...extraAllocs.map((pa) => ({
                container_port: pa.container_port,
                protocol: pa.protocol || "tcp",
                allocated_port: pa.allocated_port,
              })),
            ];
            return (
              <Container header={<Header variant="h2">Published Ports</Header>}>
                {rows.length === 0 ? (
                  <Box color="text-body-secondary">No ports declared in the workload spec.</Box>
                ) : (
                  <Table
                    items={rows}
                    columnDefinitions={[
                      { id: "cport",    header: "Container port", cell: (p) => p.container_port },
                      { id: "protocol", header: "Protocol",       cell: (p) => p.protocol.toUpperCase() },
                      { id: "hport",    header: "Host port",      cell: (p) => p.allocated_port ?? "—" },
                    ]}
                  />
                )}
              </Container>
            );
          })()}

          {/* ── Containers (single-container workloads) ───────────────────── */}
          {workload.kind === "container" && (
            <Container header={<Header variant="h2">Containers</Header>}>
              {!hasActual ? (
                <Box color="text-body-secondary">
                  No container data — agent may not have reported yet.
                </Box>
              ) : (
                <Table
                  items={containers}
                  columnDefinitions={[
                    { id: "name",   header: "Name",         cell: (c) => c.name },
                    { id: "status", header: "Status",       cell: (c) => c.status },
                    { id: "cpu",    header: "CPU %",        cell: (c) => { const s = statsByContainerId.get(c.container_id); return s ? `${s.cpu_percent.toFixed(2)}%` : "—"; } },
                    { id: "mem",    header: "Memory Used",  cell: (c) => { const s = statsByContainerId.get(c.container_id); return s ? formatBytes(s.mem_used_bytes) : "—"; } },
                    { id: "id",     header: "Container ID", cell: (c) => c.container_id },
                  ]}
                />
              )}
            </Container>
          )}

          {/* ── Stack services ────────────────────────────────────────────── */}
          {workload.kind === "stack" && (
            <Container header={<Header variant="h2">Services</Header>}>
              {!hasActual ? (
                <Box color="text-body-secondary">
                  No container data — agent may not have reported yet.
                </Box>
              ) : (
                <SpaceBetween size="m">
                  {stacks.map((st) => (
                    <Table
                      key={st.name}
                      header={<Header variant="h3">{st.name}</Header>}
                      items={st.services}
                      columnDefinitions={[
                        { id: "name", header: "Name", cell: (c) => c.name },
                        { id: "status", header: "Status", cell: (c) => c.status },
                        { id: "id", header: "Container ID", cell: (c) => c.container_id },
                      ]}
                    />
                  ))}
                </SpaceBetween>
              )}
            </Container>
          )}

          {/* ── Definition (YAML) ─────────────────────────────────────────── */}
          {spec && (() => {
            const yaml = specToYaml(spec);
            return (
              <Container
                header={
                  <Header
                    variant="h2"
                    actions={
                      <SpaceBetween direction="horizontal" size="xs">
                        <Button
                          iconName="copy"
                          onClick={() => {
                            navigator.clipboard.writeText(yaml);
                            setCopied(true);
                            setTimeout(() => setCopied(false), 2000);
                          }}
                        >
                          {copied ? "Copied!" : "Copy"}
                        </Button>
                        <Button
                          iconName="download"
                          onClick={() => downloadYaml(spec.name, yaml)}
                        >
                          Download
                        </Button>
                      </SpaceBetween>
                    }
                  >
                    Definition
                    <Box variant="small" color="text-body-secondary" display="inline">
                      {" "}— reference only; to resubmit a stack use the raw Compose YAML
                    </Box>
                  </Header>
                }
              >
                <pre style={{ margin: 0, fontFamily: "monospace", fontSize: "0.85rem", whiteSpace: "pre-wrap", wordBreak: "break-all" }}>
                  {yaml}
                </pre>
              </Container>
            );
          })()}
        </SpaceBetween>
      )}
    </ContentLayout>
  );
}
