import { useState } from "react";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import ColumnLayout from "@cloudscape-design/components/column-layout";
import Container from "@cloudscape-design/components/container";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import Header from "@cloudscape-design/components/header";
import ProgressBar from "@cloudscape-design/components/progress-bar";
import SpaceBetween from "@cloudscape-design/components/space-between";
import StatusIndicator, {
  StatusIndicatorProps,
} from "@cloudscape-design/components/status-indicator";
import Table from "@cloudscape-design/components/table";
import { api, ClusterState, formatAge } from "../api";

interface Props {
  nodeId: string;
  state: ClusterState | null;
  loading: boolean;
  error: string | null;
  refetch: () => void;
  onNavigate: (page: string) => void;
}

function nodeStatus(status: string): StatusIndicatorProps.Type {
  switch (status) {
    case "healthy":     return "success";
    case "draining":    return "warning";
    case "unreachable": return "error";
    default:            return "info";
  }
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

function formatBytes(b: number): string {
  if (!b) return "—";
  if (b >= 1_073_741_824) return `${(b / 1_073_741_824).toFixed(1)} GiB`;
  if (b >= 1_048_576)     return `${(b / 1_048_576).toFixed(1)} MiB`;
  return `${b} B`;
}

export default function NodeDetail({ nodeId, state, loading, onNavigate, refetch }: Props) {
  const [notifications, setNotifications] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [draining, setDraining] = useState(false);

  const node = state?.nodes.find((n) => n.id === nodeId) ?? null;

  const nodeWorkloads = (state?.workloads ?? []).filter((w) => w.node_id === nodeId);
  const nodeWorkloadIds = new Set(nodeWorkloads.map((w) => w.id));
  const nodeStats = (state?.container_stats ?? []).filter((cs) =>
    nodeWorkloadIds.has(cs.workload_id)
  );

  function notify(type: FlashbarProps.MessageDefinition["type"], content: string) {
    const id = String(Date.now());
    setNotifications((n) => [
      { id, type, content, dismissible: true, onDismiss: () => setNotifications((n) => n.filter((m) => m.id !== id)) },
      ...n,
    ]);
  }

  async function handleDrain() {
    if (!node) return;
    setDraining(true);
    try {
      const res = await api.drainNode(node.id);
      if (!res.accepted) {
        notify("error", `Drain rejected: ${res.reason}`);
      } else {
        notify("success", `Node ${node.id} set to draining`);
      }
    } catch (e) {
      notify("error", String(e));
    } finally {
      setDraining(false);
    }
  }

  return (
    <ContentLayout
      notifications={<Flashbar items={notifications} />}
      header={
        <Header
          variant="h1"
          description={node ? node.address : ""}
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button iconName="refresh" onClick={refetch}>Refresh</Button>
              <Button iconName="angle-left" variant="link" onClick={() => onNavigate("nodes")}>
                Back to Nodes
              </Button>
            </SpaceBetween>
          }
        >
          {node ? node.id : nodeId}
          {node && (
            <>
              {" "}
              <StatusIndicator type={nodeStatus(node.status)}>
                {node.status}
              </StatusIndicator>
            </>
          )}
        </Header>
      }
    >
      {loading && !state ? (
        <Box>Loading…</Box>
      ) : !node ? (
        <Box>Node not found.</Box>
      ) : (
        <SpaceBetween size="l">
          {/* ── Details ──────────────────────────────────────────────── */}
          <Container header={<Header variant="h2">Details</Header>}>
            <ColumnLayout columns={3} variant="text-grid">
              <div>
                <Box variant="awsui-key-label">Address</Box>
                <Box>{node.address}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Data IP</Box>
                <Box>{node.data_ip || "—"}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Last seen</Box>
                <Box>{node.last_seen_at ? formatAge(node.last_seen_at) : "—"}</Box>
              </div>
            </ColumnLayout>
          </Container>

          {/* ── Resources ────────────────────────────────────────────── */}
          <Container header={<Header variant="h2">Resources</Header>}>
            <ColumnLayout columns={3} variant="text-grid">
              <div>
                <Box variant="awsui-key-label">CPU Cores</Box>
                <Box>{node.cpu_cores > 0 ? node.cpu_cores : "—"}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Memory</Box>
                {node.mem_total_bytes > 0 ? (
                  <ProgressBar
                    value={Math.round((node.mem_used_bytes / node.mem_total_bytes) * 100)}
                    description={`${formatBytes(node.mem_used_bytes)} used of ${formatBytes(node.mem_total_bytes)}`}
                  />
                ) : (
                  <Box>{formatBytes(node.memory_bytes)}</Box>
                )}
              </div>
              <div>
                <Box variant="awsui-key-label">Disk</Box>
                {node.disk_total_bytes > 0 ? (
                  <ProgressBar
                    value={Math.round((node.disk_used_bytes / node.disk_total_bytes) * 100)}
                    description={`${formatBytes(node.disk_used_bytes)} used of ${formatBytes(node.disk_total_bytes)}`}
                  />
                ) : (
                  <Box>{formatBytes(node.disk_bytes)}</Box>
                )}
              </div>
            </ColumnLayout>
          </Container>

          {/* ── Container Stats ───────────────────────────────────────── */}
          <Container header={<Header variant="h2">Container Stats</Header>}>
            {nodeStats.length === 0 ? (
              <Box color="text-body-secondary">
                No runtime stats — agent may not have reported yet.
              </Box>
            ) : (
              <Table
                items={nodeStats}
                columnDefinitions={[
                  { id: "name",    header: "Name",        cell: (cs) => cs.name },
                  { id: "cpu",     header: "CPU %",       cell: (cs) => `${cs.cpu_percent.toFixed(2)}%` },
                  { id: "mem",     header: "Memory Used", cell: (cs) => formatBytes(cs.mem_used_bytes) },
                  { id: "memlim", header: "Mem Limit",   cell: (cs) => formatBytes(cs.mem_limit_bytes) },
                ]}
              />
            )}
          </Container>

          {/* ── Workloads ─────────────────────────────────────────────── */}
          <Container header={<Header variant="h2">Workloads</Header>}>
            {nodeWorkloads.length === 0 ? (
              <Box color="text-body-secondary">No workloads scheduled on this node.</Box>
            ) : (
              <Table
                items={nodeWorkloads}
                onRowClick={({ detail }) => onNavigate(`workload-${detail.item.id}`)}
                columnDefinitions={[
                  { id: "name",  header: "Name",  cell: (w) => w.name },
                  { id: "kind",  header: "Kind",  cell: (w) => w.kind },
                  {
                    id: "phase",
                    header: "Phase",
                    cell: (w) => (
                      <StatusIndicator type={phaseStatus(w.phase)}>{w.phase}</StatusIndicator>
                    ),
                  },
                ]}
              />
            )}
          </Container>

          {/* ── Actions ───────────────────────────────────────────────── */}
          <Button
            variant="normal"
            disabled={node.status === "draining"}
            loading={draining}
            onClick={handleDrain}
          >
            Drain node
          </Button>
        </SpaceBetween>
      )}
    </ContentLayout>
  );
}
