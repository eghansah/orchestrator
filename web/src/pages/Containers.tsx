import { useEffect, useState } from "react";
import Alert from "@cloudscape-design/components/alert";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Header from "@cloudscape-design/components/header";
import Modal from "@cloudscape-design/components/modal";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Spinner from "@cloudscape-design/components/spinner";
import StatusIndicator from "@cloudscape-design/components/status-indicator";
import Table from "@cloudscape-design/components/table";
import { ActualContainer, ClusterState, api } from "../api";

interface Props {
  state: ClusterState | null;
  loading: boolean;
  error: string | null;
  refetch: () => void;
}

function containerStatus(status: string) {
  const up = /^up/i.test(status);
  return (
    <StatusIndicator type={up ? "success" : "stopped"}>
      {status || "unknown"}
    </StatusIndicator>
  );
}

export default function Containers({ state, loading, error, refetch }: Props) {
  const containers: ActualContainer[] = state?.actual_containers ?? [];

  const [selected, setSelected] = useState<ActualContainer | null>(null);
  const [logs, setLogs] = useState<string>("");
  const [logsLoading, setLogsLoading] = useState(false);
  const [logsError, setLogsError] = useState<string | null>(null);

  async function fetchLogs(name: string) {
    setLogsLoading(true);
    setLogsError(null);
    try {
      const res = await api.getContainerLogs(name);
      setLogs(res.logs);
    } catch (e) {
      setLogsError(String(e));
    } finally {
      setLogsLoading(false);
    }
  }

  useEffect(() => {
    if (selected) fetchLogs(selected.name);
  }, [selected?.name]);

  function openLogs(c: ActualContainer) {
    setLogs("");
    setLogsError(null);
    setSelected(c);
  }

  function closeLogs() {
    setSelected(null);
    setLogs("");
    setLogsError(null);
  }

  return (
    <ContentLayout header={<Header variant="h1" actions={<Button iconName="refresh" onClick={refetch}>Refresh</Button>}>Containers</Header>}>
      <SpaceBetween size="m">
        {error && <Alert type="error">{error}</Alert>}

        <Table
          loading={loading}
          loadingText="Loading containers…"
          empty={
            <Box textAlign="center" color="inherit">
              No containers found
            </Box>
          }
          columnDefinitions={[
            {
              id: "name",
              header: "Name",
              cell: (c) => c.name,
              sortingField: "name",
            },
            {
              id: "id",
              header: "Container ID",
              cell: (c) => c.container_id ? c.container_id.slice(0, 12) : "—",
            },
            {
              id: "status",
              header: "Status",
              cell: (c) => containerStatus(c.status),
            },
            {
              id: "workload",
              header: "Workload ID",
              cell: (c) => c.workload_id || "—",
            },
            {
              id: "actions",
              header: "",
              cell: (c) => (
                <Button variant="inline-link" onClick={() => openLogs(c)}>
                  View logs
                </Button>
              ),
            },
          ]}
          items={containers}
          header={
            <Header counter={`(${containers.length})`}>
              Running containers
            </Header>
          }
        />
      </SpaceBetween>

      <Modal
        visible={!!selected}
        onDismiss={closeLogs}
        size="large"
        header={selected ? `Logs — ${selected.name}` : "Logs"}
        footer={
          <Box float="right">
            <SpaceBetween direction="horizontal" size="xs">
              <Button
                onClick={() => selected && fetchLogs(selected.name)}
                loading={logsLoading}
                disabled={logsLoading}
              >
                Refresh
              </Button>
              <Button variant="primary" onClick={closeLogs}>
                Close
              </Button>
            </SpaceBetween>
          </Box>
        }
      >
        {logsError && <Alert type="error">{logsError}</Alert>}
        {logsLoading ? (
          <Box textAlign="center" padding="l">
            <Spinner size="large" />
          </Box>
        ) : (
          <pre
            style={{
              margin: 0,
              padding: "8px",
              fontFamily: "monospace",
              fontSize: "0.8rem",
              whiteSpace: "pre-wrap",
              wordBreak: "break-all",
              maxHeight: "60vh",
              overflowY: "auto",
              background: "var(--color-background-code-editor-status-bar)",
            }}
          >
            {logs || "(no output)"}
          </pre>
        )}
      </Modal>
    </ContentLayout>
  );
}
