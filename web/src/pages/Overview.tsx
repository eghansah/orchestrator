import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import ColumnLayout from "@cloudscape-design/components/column-layout";
import Container from "@cloudscape-design/components/container";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Header from "@cloudscape-design/components/header";
import Spinner from "@cloudscape-design/components/spinner";
import StatusIndicator from "@cloudscape-design/components/status-indicator";
import { ClusterState } from "../api";

interface Props {
  state: ClusterState | null;
  loading: boolean;
  error: string | null;
  refetch: () => void;
}

function Metric({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div>
      <Box variant="awsui-key-label">{label}</Box>
      <Box variant="awsui-value-large">{value}</Box>
    </div>
  );
}

const overviewHeader = (refetch: () => void) => (
  <Header
    variant="h1"
    description="A snapshot of the current cluster state — leader, node count, and workload health at a glance."
    actions={<Button iconName="refresh" onClick={refetch}>Refresh</Button>}
  >
    Cluster Overview
  </Header>
);

export default function Overview({ state, loading, error, refetch }: Props) {
  if (loading && !state) {
    return (
      <ContentLayout header={overviewHeader(refetch)}>
        <Spinner size="large" />
      </ContentLayout>
    );
  }

  if (error) {
    return (
      <ContentLayout header={overviewHeader(refetch)}>
        <StatusIndicator type="error">{error}</StatusIndicator>
      </ContentLayout>
    );
  }

  const running = state?.workloads?.filter((w) => w.phase === "running").length ?? 0;
  const total = state?.workloads?.length ?? 0;
  const nodes = state?.nodes?.length ?? 0;

  return (
    <ContentLayout header={overviewHeader(refetch)}>
      <Container>
        <ColumnLayout columns={3} variant="text-grid">
          <Metric
            label="Leader"
            value={
              state?.is_leader ? (
                <StatusIndicator type="success">{state.leader_id || "—"}</StatusIndicator>
              ) : (
                <StatusIndicator type="warning">
                  {state?.leader_id || "Electing…"}
                </StatusIndicator>
              )
            }
          />
          <Metric label="Nodes" value={nodes} />
          <Metric label="Workloads" value={`${running} running / ${total} total`} />
        </ColumnLayout>
      </Container>
    </ContentLayout>
  );
}
