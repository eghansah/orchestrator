import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import ColumnLayout from "@cloudscape-design/components/column-layout";
import Container from "@cloudscape-design/components/container";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Header from "@cloudscape-design/components/header";
import SpaceBetween from "@cloudscape-design/components/space-between";
import StatusIndicator, {
  StatusIndicatorProps,
} from "@cloudscape-design/components/status-indicator";
import Table from "@cloudscape-design/components/table";
import { ClusterState, formatAge } from "../api";

interface Props {
  workloadId: string;
  state: ClusterState | null;
  loading: boolean;
  error: string | null;
  refetch: () => void;
  onNavigate: (page: string) => void;
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

export default function WorkloadDetail({ workloadId, state, loading, onNavigate }: Props) {
  const workload = state?.workloads.find((w) => w.id === workloadId) ?? null;

  const containers = (state?.actual_containers ?? []).filter(
    (c) => c.workload_id === workloadId
  );
  const stacks = (state?.actual_stacks ?? []).filter(
    (s) => s.workload_id === workloadId
  );

  const hasActual = containers.length > 0 || stacks.length > 0;

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={workload ? `${workload.kind} · ${workload.node_id || "unscheduled"}` : ""}
          actions={
            <Button iconName="angle-left" variant="link" onClick={() => onNavigate("workloads")}>
              Back to Workloads
            </Button>
          }
        >
          {workload ? workload.name : workloadId}
          {workload && (
            <>
              {" "}
              <StatusIndicator type={phaseStatus(workload.phase)}>
                {workload.phase}
              </StatusIndicator>
            </>
          )}
        </Header>
      }
    >
      {loading && !state ? (
        <Box>Loading…</Box>
      ) : !workload ? (
        <Box>Workload not found.</Box>
      ) : (
        <SpaceBetween size="l">
          {/* ── Metadata ─────────────────────────────────────────────────── */}
          <Container header={<Header variant="h2">Details</Header>}>
            <ColumnLayout columns={3} variant="text-grid">
              <div>
                <Box variant="awsui-key-label">ID</Box>
                <Box>{workload.id}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Kind</Box>
                <Box>{workload.kind}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Node</Box>
                <Box>{workload.node_id || "—"}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Phase</Box>
                <StatusIndicator type={phaseStatus(workload.phase)}>
                  {workload.phase}
                </StatusIndicator>
              </div>
              <div>
                <Box variant="awsui-key-label">Age</Box>
                <Box>{formatAge(workload.created_at)}</Box>
              </div>
            </ColumnLayout>
          </Container>

          {/* ── Port Allocations ──────────────────────────────────────────── */}
          {workload.port_allocations.length > 0 && (
            <Container header={<Header variant="h2">Port Allocations</Header>}>
              <Table
                items={workload.port_allocations}
                columnDefinitions={[
                  { id: "cport", header: "Container Port", cell: (p) => p.container_port },
                  { id: "hport", header: "Host Port", cell: (p) => p.allocated_port },
                ]}
              />
            </Container>
          )}

          {/* ── Containers (single-container workloads) ───────────────────── */}
          {workload.kind === "container" && (
            <Container header={<Header variant="h2">Containers</Header>}>
              {!hasActual ? (
                <Box color="text-body-secondary">
                  No container data — agent may not have reported yet.
                </Box>
              ) : (
                <Table
                  items={containers}
                  columnDefinitions={[
                    { id: "name", header: "Name", cell: (c) => c.name },
                    { id: "status", header: "Status", cell: (c) => c.status },
                    { id: "id", header: "Container ID", cell: (c) => c.container_id },
                  ]}
                />
              )}
            </Container>
          )}

          {/* ── Stack services ────────────────────────────────────────────── */}
          {workload.kind === "stack" && (
            <Container header={<Header variant="h2">Services</Header>}>
              {!hasActual ? (
                <Box color="text-body-secondary">
                  No container data — agent may not have reported yet.
                </Box>
              ) : (
                <SpaceBetween size="m">
                  {stacks.map((st) => (
                    <Table
                      key={st.name}
                      header={<Header variant="h3">{st.name}</Header>}
                      items={st.services}
                      columnDefinitions={[
                        { id: "name", header: "Name", cell: (c) => c.name },
                        { id: "status", header: "Status", cell: (c) => c.status },
                        { id: "id", header: "Container ID", cell: (c) => c.container_id },
                      ]}
                    />
                  ))}
                </SpaceBetween>
              )}
            </Container>
          )}
        </SpaceBetween>
      )}
    </ContentLayout>
  );
}
