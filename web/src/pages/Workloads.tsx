import { useState } from "react";
import Button from "@cloudscape-design/components/button";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import Header from "@cloudscape-design/components/header";
import SpaceBetween from "@cloudscape-design/components/space-between";
import StatusIndicator, {
  StatusIndicatorProps,
} from "@cloudscape-design/components/status-indicator";
import Table from "@cloudscape-design/components/table";
import { api, ClusterState, formatAge, WorkloadInfo } from "../api";
import SubmitContainer from "../components/SubmitContainer";
import SubmitStack from "../components/SubmitStack";

interface Props {
  state: ClusterState | null;
  loading: boolean;
  error: string | null;
  refetch: () => void;
  onNavigate: (page: string) => void;
}

function phaseStatus(phase: string): StatusIndicatorProps.Type {
  switch (phase) {
    case "running":
      return "success";
    case "failed":
      return "error";
    case "draining":
    case "pending":
      return "pending";
    case "scheduled":
      return "in-progress";
    case "stopped":
      return "stopped";
    default:
      return "info";
  }
}

export default function Workloads({ state, loading, refetch, onNavigate }: Props) {
  const [showRun, setShowRun] = useState(false);
  const [showStack, setShowStack] = useState(false);
  const [notifications, setNotifications] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [removing, setRemoving] = useState<string | null>(null);

  function notify(type: FlashbarProps.MessageDefinition["type"], content: string) {
    const id = String(Date.now());
    setNotifications((n) => [
      { id, type, content, dismissible: true, onDismiss: () => removeNotif(id) },
      ...n,
    ]);
  }

  function removeNotif(id: string) {
    setNotifications((n) => n.filter((m) => m.id !== id));
  }

  async function handleRemove(id: string) {
    setRemoving(id);
    try {
      const res = await api.removeWorkload(id);
      if (!res.accepted) {
        notify("error", `Remove rejected: ${res.reason}`);
      } else {
        notify("success", `Workload ${id} removed`);
        refetch();
      }
    } catch (e) {
      notify("error", String(e));
    } finally {
      setRemoving(null);
    }
  }

  const items = state?.workloads ?? [];

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="Containers and Compose stacks running across the cluster. Submit new workloads or remove existing ones."
          actions={<Button iconName="refresh" onClick={refetch}>Refresh</Button>}
        >
          Workloads
        </Header>
      }
      notifications={<Flashbar items={notifications} />}
    >
      <SpaceBetween size="m">
        <Table<WorkloadInfo>
          loading={loading && !state}
          loadingText="Loading workloads…"
          items={items}
          empty={<span>No workloads. Submit one to get started.</span>}
          onRowClick={({ detail }) => onNavigate(`workload-${detail.item.id}`)}
          header={
            <Header
              counter={`(${items.length})`}
              actions={
                <SpaceBetween direction="horizontal" size="xs">
                  <Button onClick={() => setShowRun(true)}>Submit Container</Button>
                  <Button onClick={() => setShowStack(true)} variant="primary">
                    Submit Compose Stack
                  </Button>
                </SpaceBetween>
              }
            >
              Workloads
            </Header>
          }
          columnDefinitions={[
            { id: "id", header: "ID", cell: (w) => w.id },
            { id: "node", header: "Node", cell: (w) => w.node_id || "—" },
            {
              id: "phase",
              header: "Phase",
              cell: (w) => (
                <StatusIndicator type={phaseStatus(w.phase)}>{w.phase}</StatusIndicator>
              ),
            },
            { id: "kind", header: "Kind", cell: (w) => w.kind },
            { id: "name", header: "Name", cell: (w) => w.name },
            { id: "age", header: "Age", cell: (w) => formatAge(w.created_at) },
            {
              id: "actions",
              header: "",
              cell: (w) => (
                <span onClick={(e) => e.stopPropagation()}>
                  <Button
                    variant="inline-link"
                    loading={removing === w.id}
                    onClick={() => handleRemove(w.id)}
                  >
                    Remove
                  </Button>
                </span>
              ),
            },
          ]}
        />
      </SpaceBetween>

      <SubmitContainer
        visible={showRun}
        onDismiss={() => setShowRun(false)}
        onSuccess={(id) => {
          setShowRun(false);
          notify("success", `Container workload ${id} submitted`);
          refetch();
        }}
        onError={(msg) => notify("error", msg)}
      />

      <SubmitStack
        visible={showStack}
        onDismiss={() => setShowStack(false)}
        onSuccess={(id) => {
          setShowStack(false);
          notify("success", `Stack workload ${id} submitted`);
          refetch();
        }}
        onError={(msg) => notify("error", msg)}
      />
    </ContentLayout>
  );
}
