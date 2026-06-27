import { useCallback, useEffect, useState } from "react";
import {
  Button,
  ContentLayout,
  Flashbar,
  FlashbarProps,
  Header,
  Table,
} from "@cloudscape-design/components";
import { api, VolumeListEntry } from "../api";

interface Props {
  onNavigate: (page: string) => void;
}

export default function Volumes({ onNavigate }: Props) {
  const [volumes, setVolumes] = useState<VolumeListEntry[]>([]);
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
      const data = await api.listVolumes();
      setVolumes(data ?? []);
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
          description="Named volumes managed by nerdctl on this node."
          actions={<Button iconName="refresh" onClick={load}>Refresh</Button>}
        >
          Volumes
        </Header>
      }
    >
      <Table
        loading={loading}
        loadingText="Loading volumes"
        columnDefinitions={[
          {
            id: "name",
            header: "Name",
            cell: (v) => (
              <Button variant="inline-link" onClick={() => onNavigate("volume-" + v.name)}>
                {v.name}
              </Button>
            ),
          },
          { id: "driver", header: "Driver", cell: (v) => v.driver },
          { id: "mountpoint", header: "Mountpoint", cell: (v) => <code>{v.mountpoint}</code> },
          { id: "labels", header: "Labels", cell: (v) => v.labels || "—" },
        ]}
        items={volumes}
        trackBy="name"
        empty="No volumes"
      />
    </ContentLayout>
  );
}
