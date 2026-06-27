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
  Table,
} from "@cloudscape-design/components";
import { api, NetworkInspectResult } from "../api";

interface Props {
  networkName: string;
  onNavigate: (page: string) => void;
}

export default function NetworkDetail({ networkName, onNavigate }: Props) {
  const [network, setNetwork] = useState<NetworkInspectResult | null>(null);
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
      const data = await api.inspectNetwork(networkName);
      setNetwork(data);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLoading(false);
    }
  }, [networkName]);

  useEffect(() => { load(); }, [load]);

  if (loading) return <Spinner />;

  if (!network) {
    return (
      <ContentLayout header={<Header variant="h1">Network not found</Header>}>
        <Button variant="inline-link" onClick={() => onNavigate("networks")}>
          ← Back to Networks
        </Button>
      </ContentLayout>
    );
  }

  const labelEntries = Object.entries(network.labels ?? {});

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          actions={<Button iconName="refresh" onClick={load}>Refresh</Button>}
        >
          {network.name}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Button variant="inline-link" onClick={() => onNavigate("networks")}>
          ← Back to Networks
        </Button>

        <Container header={<Header variant="h2">Network details</Header>}>
          <ColumnLayout columns={3} variant="text-grid">
            <div>
              <Box variant="awsui-key-label">Name</Box>
              <Box>{network.name}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">ID</Box>
              <Box><code>{network.id}</code></Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Driver</Box>
              <Box>{network.driver}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Subnet</Box>
              <Box>{network.subnet || "—"}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Gateway</Box>
              <Box>{network.gateway || "—"}</Box>
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

        <Table
          header={<Header variant="h2" counter={`(${network.containers.length})`}>Containers</Header>}
          columnDefinitions={[
            { id: "name", header: "Container", cell: (c) => c.name },
            { id: "ipv4", header: "IPv4 Address", cell: (c) => c.ipv4_address || "—" },
          ]}
          items={network.containers}
          trackBy="name"
          empty="No containers connected to this network"
        />
      </SpaceBetween>
    </ContentLayout>
  );
}
