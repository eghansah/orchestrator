import { useCallback, useEffect, useState } from "react";
import {
  Box,
  Button,
  ColumnLayout,
  Container,
  ContentLayout,
  Flashbar,
  FlashbarProps,
  Header,
  SpaceBetween,
  Spinner,
} from "@cloudscape-design/components";
import { api, VolumeInspectResult } from "../api";

interface Props {
  volumeName: string;
  onNavigate: (page: string) => void;
}

export default function VolumeDetail({ volumeName, onNavigate }: Props) {
  const [volume, setVolume] = useState<VolumeInspectResult | null>(null);
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
      const data = await api.inspectVolume(volumeName);
      setVolume(data);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLoading(false);
    }
  }, [volumeName]);

  useEffect(() => { load(); }, [load]);

  if (loading) return <Spinner />;

  if (!volume) {
    return (
      <ContentLayout header={<Header variant="h1">Volume not found</Header>}>
        <Button variant="inline-link" onClick={() => onNavigate("volumes")}>
          ← Back to Volumes
        </Button>
      </ContentLayout>
    );
  }

  const labelEntries = Object.entries(volume.labels ?? {});

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          actions={<Button iconName="refresh" onClick={load}>Refresh</Button>}
        >
          {volume.name}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Button variant="inline-link" onClick={() => onNavigate("volumes")}>
          ← Back to Volumes
        </Button>

        <Container header={<Header variant="h2">Volume details</Header>}>
          <ColumnLayout columns={3} variant="text-grid">
            <div>
              <Box variant="awsui-key-label">Name</Box>
              <Box>{volume.name}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Driver</Box>
              <Box>{volume.driver}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Scope</Box>
              <Box>{volume.scope || "—"}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Mountpoint</Box>
              <Box><code>{volume.mountpoint}</code></Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Labels</Box>
              <Box>
                {labelEntries.length === 0
                  ? "—"
                  : labelEntries.map(([k, v]) => (
                      <div key={k}><code>{k}={v}</code></div>
                    ))}
              </Box>
            </div>
          </ColumnLayout>
        </Container>
      </SpaceBetween>
    </ContentLayout>
  );
}
