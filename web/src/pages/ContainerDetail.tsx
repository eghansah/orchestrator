import { useCallback, useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  ColumnLayout,
  Container,
  ContentLayout,
  Flashbar,
  FlashbarProps,
  Header,
  Modal,
  SpaceBetween,
  Spinner,
  StatusIndicator,
  Table,
} from "@cloudscape-design/components";
import { api, ContainerInspectResult } from "../api";

interface Props {
  containerName: string;
  onNavigate: (page: string) => void;
}

export default function ContainerDetail({ containerName, onNavigate }: Props) {
  const [detail, setDetail] = useState<ContainerInspectResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [showLogs, setShowLogs] = useState(false);
  const [logs, setLogs] = useState("");
  const [logsLoading, setLogsLoading] = useState(false);
  const [confirmRestart, setConfirmRestart] = useState(false);
  const [restarting, setRestarting] = useState(false);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  const load = useCallback(async () => {
    try {
      const data = await api.inspectContainer(containerName);
      setDetail(data);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLoading(false);
    }
  }, [containerName]);

  useEffect(() => { load(); }, [load]);

  async function fetchLogs() {
    setLogsLoading(true);
    try {
      const res = await api.getContainerLogs(containerName);
      setLogs(res.logs);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLogsLoading(false);
    }
  }

  function openLogs() {
    setLogs("");
    setShowLogs(true);
    fetchLogs();
  }

  async function handleRestart() {
    setRestarting(true);
    setConfirmRestart(false);
    try {
      await api.restartContainer(containerName);
      addFlash("success", `Container ${containerName} restarted`);
      load();
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setRestarting(false);
    }
  }

  if (loading) return <Spinner />;

  if (!detail) {
    return (
      <ContentLayout header={<Header variant="h1">Container not found</Header>}>
        <Button variant="inline-link" onClick={() => onNavigate("containers")}>
          ← Back to Containers
        </Button>
      </ContentLayout>
    );
  }

  const portRows = Object.entries(detail.port_bindings ?? {}).flatMap(([proto, bindings]) =>
    bindings.map((b) => ({ proto, hostIP: b.host_ip, hostPort: b.host_port }))
  );

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          description={detail.image}
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button iconName="refresh" onClick={load}>Refresh</Button>
              <Button onClick={openLogs}>View logs</Button>
              <Button loading={restarting} onClick={() => setConfirmRestart(true)}>Restart</Button>
            </SpaceBetween>
          }
        >
          {detail.name}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Button variant="inline-link" onClick={() => onNavigate("containers")}>
          ← Back to Containers
        </Button>

        {/* ── Overview ─────────────────────────────────────────────────── */}
        <Container header={<Header variant="h2">Overview</Header>}>
          <ColumnLayout columns={3} variant="text-grid">
            <div>
              <Box variant="awsui-key-label">Status</Box>
              <Box>
                <StatusIndicator type={detail.running ? "success" : "stopped"}>
                  {detail.status}
                </StatusIndicator>
              </Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Container ID</Box>
              <Box><code>{detail.id.slice(0, 12)}</code></Box>
            </div>
            <div>
              <Box variant="awsui-key-label">PID</Box>
              <Box>{detail.pid || "—"}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Image</Box>
              <Box>{detail.image}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Started at</Box>
              <Box>{detail.started_at ? new Date(detail.started_at).toLocaleString() : "—"}</Box>
            </div>
          </ColumnLayout>
        </Container>

        {/* ── Port bindings ─────────────────────────────────────────────── */}
        <Table
          header={<Header variant="h2" counter={`(${portRows.length})`}>Port bindings</Header>}
          columnDefinitions={[
            { id: "proto", header: "Protocol", cell: (r) => r.proto },
            { id: "host_ip", header: "Host IP", cell: (r) => r.hostIP || "0.0.0.0" },
            { id: "host_port", header: "Host port", cell: (r) => r.hostPort },
          ]}
          items={portRows}
          empty="No port bindings"
        />

        {/* ── Networks ─────────────────────────────────────────────────── */}
        <Table
          header={<Header variant="h2" counter={`(${detail.networks.length})`}>Networks</Header>}
          columnDefinitions={[
            { id: "name", header: "Network", cell: (n) => n.name },
            { id: "ip", header: "IP address", cell: (n) => n.ip_address || "—" },
            { id: "gateway", header: "Gateway", cell: (n) => n.gateway || "—" },
            { id: "mac", header: "MAC address", cell: (n) => n.mac_address || "—" },
          ]}
          items={detail.networks}
          trackBy="name"
          empty="No network info"
        />

        {/* ── Mounts ───────────────────────────────────────────────────── */}
        <Table
          header={<Header variant="h2" counter={`(${detail.mounts.length})`}>Mounts</Header>}
          columnDefinitions={[
            { id: "type", header: "Type", cell: (m) => m.type },
            { id: "dest", header: "Container path", cell: (m) => <code>{m.destination}</code> },
            { id: "src", header: "Source", cell: (m) => <code>{m.source}</code> },
            { id: "mode", header: "Mode", cell: (m) => m.rw ? "rw" : "ro" },
          ]}
          items={detail.mounts}
          trackBy="destination"
          empty="No mounts"
        />

        {/* ── Environment ──────────────────────────────────────────────── */}
        <Container header={<Header variant="h2" counter={`(${detail.env?.length ?? 0})`}>Environment variables</Header>}>
          {(detail.env?.length ?? 0) === 0 ? (
            <Box color="text-body-secondary">None</Box>
          ) : (
            <div style={{ fontFamily: "monospace", fontSize: "0.85rem", lineHeight: 1.6 }}>
              {detail.env.map((e) => <div key={e}>{e}</div>)}
            </div>
          )}
        </Container>
      </SpaceBetween>

      {/* ── Logs modal ───────────────────────────────────────────────────── */}
      <Modal
        visible={showLogs}
        onDismiss={() => setShowLogs(false)}
        size="large"
        header={`Logs — ${detail.name}`}
        footer={
          <Box float="right">
            <SpaceBetween direction="horizontal" size="xs">
              <Button onClick={fetchLogs} loading={logsLoading}>Refresh</Button>
              <Button variant="primary" onClick={() => setShowLogs(false)}>Close</Button>
            </SpaceBetween>
          </Box>
        }
      >
        {logsLoading ? (
          <Box textAlign="center" padding="l"><Spinner size="large" /></Box>
        ) : (
          <pre style={{
            margin: 0, padding: "8px", fontFamily: "monospace", fontSize: "0.8rem",
            whiteSpace: "pre-wrap", wordBreak: "break-all",
            maxHeight: "60vh", overflowY: "auto",
            background: "var(--color-background-code-editor-status-bar)",
          }}>
            {logs || "(no output)"}
          </pre>
        )}
      </Modal>

      {/* ── Restart confirm ──────────────────────────────────────────────── */}
      <Modal
        visible={confirmRestart}
        onDismiss={() => setConfirmRestart(false)}
        header="Restart container"
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => setConfirmRestart(false)}>Cancel</Button>
            <Button variant="primary" onClick={handleRestart}>Restart</Button>
          </SpaceBetween>
        }
      >
        <Alert type="warning">
          Restarting <strong>{detail.name}</strong> will briefly interrupt any active connections.
          The orchestrator will not re-place it — if the container fails to come back up, use the
          workload page to re-deploy.
        </Alert>
      </Modal>
    </ContentLayout>
  );
}
