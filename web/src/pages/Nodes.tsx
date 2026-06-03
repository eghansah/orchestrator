import { useState } from "react";
import Button from "@cloudscape-design/components/button";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import Header from "@cloudscape-design/components/header";
import StatusIndicator, {
  StatusIndicatorProps,
} from "@cloudscape-design/components/status-indicator";
import Table from "@cloudscape-design/components/table";
import { api, ClusterState, NodeInfo } from "../api";

interface Props {
  state: ClusterState | null;
  loading: boolean;
  error: string | null;
  refetch: () => void;
  onNavigate: (page: string) => void;
}

function nodeStatus(status: string): StatusIndicatorProps.Type {
  switch (status) {
    case "healthy":
      return "success";
    case "draining":
      return "warning";
    case "unreachable":
      return "error";
    default:
      return "info";
  }
}

export default function Nodes({ state, loading, refetch, onNavigate }: Props) {
  const [notifications, setNotifications] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [draining, setDraining] = useState<string | null>(null);

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

  async function handleDrain(id: string) {
    setDraining(id);
    try {
      const res = await api.drainNode(id);
      if (!res.accepted) {
        notify("error", `Drain rejected: ${res.reason}`);
      } else {
        notify("success", `Node ${id} set to draining`);
        refetch();
      }
    } catch (e) {
      notify("error", String(e));
    } finally {
      setDraining(null);
    }
  }

  const items = state?.nodes ?? [];

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="All cluster members and their health status. Drain a node to stop new workloads from being placed on it."
          actions={<Button iconName="refresh" onClick={refetch}>Refresh</Button>}
        >
          Nodes
        </Header>
      }
      notifications={<Flashbar items={notifications} />}
    >
      <Table<NodeInfo>
        loading={loading && !state}
        loadingText="Loading nodes…"
        items={items}
        empty={<span>No nodes registered.</span>}
        header={<Header counter={`(${items.length})`}>Nodes</Header>}
        onRowClick={({ detail }) => onNavigate(`node-${detail.item.id}`)}
        columnDefinitions={[
          { id: "id", header: "ID", cell: (n) => n.id },
          { id: "address", header: "Address", cell: (n) => n.address },
          {
            id: "status",
            header: "Status",
            cell: (n) => (
              <StatusIndicator type={nodeStatus(n.status)}>{n.status}</StatusIndicator>
            ),
          },
          {
            id: "cpus",
            header: "CPUs",
            cell: (n) => (n.cpu_cores > 0 ? n.cpu_cores : "—"),
          },
          {
            id: "actions",
            header: "",
            cell: (n) => (
              <span onClick={(e) => e.stopPropagation()}>
                <Button
                  variant="inline-link"
                  disabled={n.status === "draining"}
                  loading={draining === n.id}
                  onClick={() => handleDrain(n.id)}
                >
                  Drain
                </Button>
              </span>
            ),
          },
        ]}
      />
    </ContentLayout>
  );
}
