import { useCallback, useEffect, useState } from "react";
import {
  Box,
  Button,
  ColumnLayout,
  Container,
  ContentLayout,
  Flashbar,
  FlashbarProps,
  Form,
  FormField,
  Header,
  Input,
  Modal,
  Select,
  SelectProps,
  SpaceBetween,
  Spinner,
} from "@cloudscape-design/components";
import { api, Service, WorkloadInfo } from "../api";
import { formatAge } from "../api";

interface Props {
  serviceId: string;
  onNavigate: (page: string) => void;
}

type ModalMode = "edit" | "delete-confirm" | null;

export default function ServiceDetail({ serviceId, onNavigate }: Props) {
  const [service, setService] = useState<Service | null>(null);
  const [workloads, setWorkloads] = useState<WorkloadInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [mode, setMode] = useState<ModalMode>(null);
  const [editForm, setEditForm] = useState({ name: "", workload_name: "", target_port: "" });

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  const load = useCallback(async () => {
    try {
      const [allServices, state] = await Promise.all([api.listServices(), api.getState()]);
      const svc = (allServices ?? []).find((s) => s.id === serviceId) ?? null;
      setService(svc);
      setWorkloads(state?.workloads ?? []);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLoading(false);
    }
  }, [serviceId]);

  useEffect(() => { load(); }, [load]);

  function openEdit() {
    if (!service) return;
    setEditForm({
      name: service.name,
      workload_name: service.workload_name,
      target_port: String(service.target_port),
    });
    setMode("edit");
  }

  async function handleUpdate() {
    if (!service) return;
    const port = parseInt(editForm.target_port, 10);
    if (!editForm.name || !editForm.workload_name || isNaN(port) || port <= 0) {
      addFlash("error", "Name, workload, and a valid container port are required");
      return;
    }
    try {
      const resp = await api.updateService(service.id, {
        name: editForm.name,
        workload_name: editForm.workload_name,
        target_port: port,
      });
      addFlash("success", "Service updated");
      if (resp.warning) addFlash("warning", resp.warning);
      setMode(null);
      load();
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleDelete() {
    if (!service) return;
    try {
      await api.deleteService(service.id);
      onNavigate("services");
    } catch (e) {
      addFlash("error", String(e));
      setMode(null);
    }
  }

  const workloadOptions: SelectProps.Option[] = workloads.map((w) => ({
    value: w.name,
    label: w.name,
    description: w.phase,
  }));

  const editWorkloadOption = workloadOptions.find((o) => o.value === editForm.workload_name) ?? null;

  if (loading) return <Spinner />;
  if (!service) {
    return (
      <ContentLayout header={<Header variant="h1">Service not found</Header>}>
        <Button variant="inline-link" onClick={() => onNavigate("services")}>
          ← Back to Services
        </Button>
      </ContentLayout>
    );
  }

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button onClick={openEdit}>Edit</Button>
              <Button variant="normal" onClick={() => setMode("delete-confirm")}>
                Delete
              </Button>
            </SpaceBetween>
          }
        >
          {service.name}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Button variant="inline-link" onClick={() => onNavigate("services")}>
          ← Back to Services
        </Button>

        <Container header={<Header variant="h2">Service details</Header>}>
          <ColumnLayout columns={3} variant="text-grid">
            <div>
              <Box variant="awsui-key-label">Name</Box>
              <Box>{service.name}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">ID</Box>
              <Box>{service.id}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Age</Box>
              <Box>{formatAge(service.created_at)}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Workload</Box>
              <Box>{service.workload_name}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Container port</Box>
              <Box>{service.target_port}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">System port</Box>
              <Box>{service.system_port}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">DNS name</Box>
              <Box>
                <code>{service.name}.svc.local</code>
              </Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Connect</Box>
              <Box>
                <code>{service.name}.svc.local:{service.system_port}</code>
              </Box>
            </div>
          </ColumnLayout>
        </Container>
      </SpaceBetween>

      {/* ── Edit modal ─────────────────────────────────────────────────── */}
      <Modal
        visible={mode === "edit"}
        onDismiss={() => setMode(null)}
        header="Edit service"
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => setMode(null)}>Cancel</Button>
            <Button variant="primary" onClick={handleUpdate}>Save</Button>
          </SpaceBetween>
        }
      >
        <Form>
          <SpaceBetween size="m">
            <FormField label="Name" description="Short DNS label (e.g. api, db)" constraintText="Required">
              <Input
                value={editForm.name}
                onChange={(e) => setEditForm((f) => ({ ...f, name: e.detail.value }))}
                placeholder="api"
              />
            </FormField>
            <FormField label="Workload" constraintText="Required">
              <Select
                filteringType="auto"
                options={workloadOptions}
                selectedOption={editWorkloadOption}
                onChange={(e) =>
                  setEditForm((f) => ({ ...f, workload_name: e.detail.selectedOption.value ?? "" }))
                }
                placeholder="Select a workload"
                empty="No workloads available"
              />
            </FormField>
            <FormField
              label="Container port"
              description="Port the container listens on"
              constraintText="Required — system port is preserved"
            >
              <Input
                type="number"
                value={editForm.target_port}
                onChange={(e) => setEditForm((f) => ({ ...f, target_port: e.detail.value }))}
                placeholder="3000"
              />
            </FormField>
          </SpaceBetween>
        </Form>
      </Modal>

      {/* ── Delete confirm modal ───────────────────────────────────────── */}
      <Modal
        visible={mode === "delete-confirm"}
        onDismiss={() => setMode(null)}
        header="Delete service"
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => setMode(null)}>Cancel</Button>
            <Button variant="primary" onClick={handleDelete}>Delete</Button>
          </SpaceBetween>
        }
      >
        Are you sure you want to delete service <strong>{service.name}</strong>? This cannot be undone.
      </Modal>
    </ContentLayout>
  );
}
