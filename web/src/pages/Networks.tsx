import { useCallback, useEffect, useState } from "react";
import {
  Button,
  ContentLayout,
  Flashbar,
  FlashbarProps,
  Header,
  Table,
} from "@cloudscape-design/components";
import { api, NetworkListEntry } from "../api";

interface Props {
  onNavigate: (page: string) => void;
}

export default function Networks({ onNavigate }: Props) {
  const [networks, setNetworks] = useState<NetworkListEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  const load = useCallback(async () => {
    try {
      const data = await api.listNetworks();
      setNetworks(data ?? []);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const id = setInterval(load, 10000);
    return () => clearInterval(id);
  }, [load]);

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          description="Container networks managed by nerdctl on this node."
          actions={<Button iconName="refresh" onClick={load}>Refresh</Button>}
        >
          Networks
        </Header>
      }
    >
      <Table
        loading={loading}
        loadingText="Loading networks"
        columnDefinitions={[
          {
            id: "name",
            header: "Name",
            cell: (n) => (
              <Button variant="inline-link" onClick={() => onNavigate("network-" + n.name)}>
                {n.name}
              </Button>
            ),
          },
          { id: "driver", header: "Driver", cell: (n) => n.driver },
          { id: "ipv4", header: "Subnet", cell: (n) => n.ipv4 || "—" },
          { id: "labels", header: "Labels", cell: (n) => n.labels || "—" },
        ]}
        items={networks}
        trackBy="network_id"
        empty="No networks"
      />
    </ContentLayout>
  );
}
