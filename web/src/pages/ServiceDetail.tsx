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
  SpaceBetween,
  Spinner,
} from "@cloudscape-design/components";
import { api, Service } from "../api";
import { formatAge } from "../api";

interface Props {
  serviceId: string;
  onNavigate: (page: string) => void;
}

type ModalMode = "edit" | "delete-confirm" | null;

export default function ServiceDetail({ serviceId, onNavigate }: Props) {
  const [service, setService] = useState<Service | null>(null);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [mode, setMode] = useState<ModalMode>(null);
  const [editForm, setEditForm] = useState({ name: "", container_fqdn: "", container_port: "" });

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  const load = useCallback(async () => {
    try {
      const allServices = await api.listServices();
      const svc = (allServices ?? []).find((s) => s.id === serviceId) ?? null;
      setService(svc);
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
      container_fqdn: service.container_fqdn,
      container_port: String(service.container_port),
    });
    setMode("edit");
  }

  async function handleUpdate() {
    if (!service) return;
    const port = parseInt(editForm.container_port, 10);
    if (!editForm.name || !editForm.container_fqdn || isNaN(port) || port <= 0) {
      addFlash("error", "Name, container FQDN, and a valid container port are required");
      return;
    }
    try {
      const resp = await api.updateService(service.id, {
        name: editForm.name,
        container_fqdn: editForm.container_fqdn,
        container_port: port,
      });
      addFlash("success", "TCP service updated");
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
      onNavigate("tcp-services");
    } catch (e) {
      addFlash("error", String(e));
      setMode(null);
    }
  }

  if (loading) return <Spinner />;
  if (!service) {
    return (
      <ContentLayout header={<Header variant="h1">TCP service not found</Header>}>
        <Button variant="inline-link" onClick={() => onNavigate("tcp-services")}>
          ← Back to TCP Services
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
        <Button variant="inline-link" onClick={() => onNavigate("tcp-services")}>
          ← Back to TCP Services
        </Button>

        <Container header={<Header variant="h2">TCP service details</Header>}>
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
              <Box variant="awsui-key-label">Container FQDN</Box>
              <Box>{service.container_fqdn}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Container port</Box>
              <Box>{service.container_port}</Box>
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
        header="Edit TCP service"
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
            <FormField label="Container FQDN" description='e.g. "ecouniversal" or "backend.myapp"' constraintText="Required">
              <Input
                value={editForm.container_fqdn}
                onChange={(e) => setEditForm((f) => ({ ...f, container_fqdn: e.detail.value }))}
                placeholder="ecouniversal"
              />
            </FormField>
            <FormField
              label="Container port"
              description="Port the container listens on"
              constraintText="Required — system port is preserved"
            >
              <Input
                type="number"
                value={editForm.container_port}
                onChange={(e) => setEditForm((f) => ({ ...f, container_port: e.detail.value }))}
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
        header="Delete TCP service"
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => setMode(null)}>Cancel</Button>
            <Button variant="primary" onClick={handleDelete}>Delete</Button>
          </SpaceBetween>
        }
      >
        Are you sure you want to delete TCP service <strong>{service.name}</strong>? This cannot be undone.
      </Modal>
    </ContentLayout>
  );
}
