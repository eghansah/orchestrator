import { useCallback, useEffect, useState } from "react";
import {
  Button,
  ContentLayout,
  Flashbar,
  FlashbarProps,
  Form,
  FormField,
  Header,
  Input,
  Modal,
  SpaceBetween,
  Table,
} from "@cloudscape-design/components";
import { api, Service } from "../api";
import { formatAge } from "../api";

interface Props {
  onNavigate: (page: string) => void;
}

export default function Services({ onNavigate }: Props) {
  const [services, setServices] = useState<Service[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [creating, setCreating] = useState(false);
  const [createForm, setCreateForm] = useState({
    name: "",
    container_fqdn: "",
    container_port: "",
  });
  const [selected, setSelected] = useState<Service[]>([]);

  const load = useCallback(async () => {
    try {
      const data = await api.listServices();
      setServices(data ?? []);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  }, [load]);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  async function handleCreate() {
    const port = parseInt(createForm.container_port, 10);
    if (!createForm.name || !createForm.container_fqdn || isNaN(port) || port <= 0) {
      addFlash("error", "Name, container FQDN, and a valid container port are required");
      return;
    }
    try {
      const resp = await api.createService({
        name: createForm.name,
        container_fqdn: createForm.container_fqdn,
        container_port: port,
      });
      if (!resp.accepted) {
        addFlash("error", resp.reason ?? "rejected");
      } else {
        addFlash("success", `TCP service ${resp.service_id} created (system port ${resp.system_port})`);
        setCreating(false);
        setCreateForm({ name: "", container_fqdn: "", container_port: "" });
        load();
      }
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleDelete() {
    for (const svc of selected) {
      try {
        await api.deleteService(svc.id);
        addFlash("success", `Deleted ${svc.id}`);
      } catch (e) {
        addFlash("error", String(e));
      }
    }
    setSelected([]);
    load();
  }

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          description="Named TCP endpoints for workloads. Internally, each TCP service allocates a system port on the node running the workload; proxyd listens on that port and forwards traffic to the container. The service is reachable both within the cluster (via DNS at <name>.<workload>.svc.local) and from outside the cluster (directly at <node-data-ip>:<system-port>)."
          actions={<Button iconName="refresh" onClick={load}>Refresh</Button>}
        >
          TCP Services
        </Header>
      }
    >
      <Table
        loading={loading}
        loadingText="Loading TCP services"
        header={
          <Header
            actions={
              <SpaceBetween direction="horizontal" size="xs">
                <Button disabled={selected.length === 0} onClick={handleDelete}>
                  Delete
                </Button>
                <Button variant="primary" onClick={() => setCreating(true)}>
                  Create TCP service
                </Button>
              </SpaceBetween>
            }
          >
            TCP Services
          </Header>
        }
        selectionType="multi"
        selectedItems={selected}
        onSelectionChange={(e) => setSelected(e.detail.selectedItems)}
        trackBy="id"
        columnDefinitions={[
          { id: "id", header: "ID", cell: (s) => s.id },
          {
            id: "name",
            header: "Name",
            cell: (s) => (
              <Button variant="inline-link" onClick={() => onNavigate("service-" + s.id)}>
                {s.name}
              </Button>
            ),
          },
          { id: "system_port", header: "System port", cell: (s) => s.system_port },
          { id: "container_fqdn", header: "Container FQDN", cell: (s) => s.container_fqdn },
          { id: "container_port", header: "Container port", cell: (s) => s.container_port },
          { id: "dns", header: "DNS", cell: (s) => `${s.name}.svc.local` },
          { id: "age", header: "Age", cell: (s) => formatAge(s.created_at) },
        ]}
        items={services}
        empty="No TCP services"
      />

      {/* ── Create modal ─────────────────────────────────────────────── */}
      <Modal
        visible={creating}
        onDismiss={() => setCreating(false)}
        header="Create TCP service"
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => setCreating(false)}>
              Cancel
            </Button>
            <Button variant="primary" onClick={handleCreate}>
              Create
            </Button>
          </SpaceBetween>
        }
      >
        <Form>
          <SpaceBetween size="m">
            <FormField label="Name" description="Short DNS label (e.g. api, db)" constraintText="Required">
              <Input
                value={createForm.name}
                onChange={(e) => setCreateForm((f) => ({ ...f, name: e.detail.value }))}
                placeholder="api"
              />
            </FormField>
            <FormField label="Container FQDN" description='Container name, e.g. "ecouniversal" or "backend.myapp" for compose' constraintText="Required">
              <Input
                value={createForm.container_fqdn}
                onChange={(e) => setCreateForm((f) => ({ ...f, container_fqdn: e.detail.value }))}
                placeholder="ecouniversal"
              />
            </FormField>
            <FormField label="Container port" description="Port the container listens on" constraintText="Required">
              <Input
                type="number"
                value={createForm.container_port}
                onChange={(e) => setCreateForm((f) => ({ ...f, container_port: e.detail.value }))}
                placeholder="3000"
              />
            </FormField>
          </SpaceBetween>
        </Form>
      </Modal>
    </ContentLayout>
  );
}
