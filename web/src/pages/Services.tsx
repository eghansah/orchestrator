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
  Select,
  SelectProps,
  SpaceBetween,
  Table,
} from "@cloudscape-design/components";
import { api, Service, WorkloadInfo } from "../api";
import { formatAge } from "../api";

export default function Services() {
  const [services, setServices] = useState<Service[]>([]);
  const [workloads, setWorkloads] = useState<WorkloadInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [creating, setCreating] = useState(false);
  const [createForm, setCreateForm] = useState({
    name: "",
    workload_name: "",
    target_port: "",
  });
  const [selected, setSelected] = useState<Service[]>([]);
  const [editing, setEditing] = useState<Service | null>(null);
  const [editForm, setEditForm] = useState({
    name: "",
    workload_name: "",
    target_port: "",
  });

  const load = useCallback(async () => {
    try {
      const [data, state] = await Promise.all([api.listServices(), api.getState()]);
      setServices(data ?? []);
      setWorkloads(state?.workloads ?? []);
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
    const port = parseInt(createForm.target_port, 10);
    if (!createForm.name || !createForm.workload_name || isNaN(port) || port <= 0) {
      addFlash("error", "Name, workload, and a valid container port are required");
      return;
    }
    try {
      const resp = await api.createService({
        name: createForm.name,
        workload_name: createForm.workload_name,
        target_port: port,
      });
      if (!resp.accepted) {
        addFlash("error", resp.reason ?? "rejected");
      } else {
        addFlash("success", `Service ${resp.service_id} created (port ${resp.system_port})`);
        if (resp.warning) addFlash("warning", resp.warning);
        setCreating(false);
        setCreateForm({ name: "", workload_name: "", target_port: "" });
        load();
      }
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  function openEdit(svc: Service) {
    setEditForm({
      name: svc.name,
      workload_name: svc.workload_name,
      target_port: String(svc.target_port),
    });
    setEditing(svc);
  }

  async function handleUpdate() {
    if (!editing) return;
    const port = parseInt(editForm.target_port, 10);
    if (!editForm.name || !editForm.workload_name || isNaN(port) || port <= 0) {
      addFlash("error", "Name, workload, and a valid container port are required");
      return;
    }
    try {
      const resp = await api.updateService(editing.id, {
        name: editForm.name,
        workload_name: editForm.workload_name,
        target_port: port,
      });
      addFlash("success", `Service ${editing.id} updated`);
      if (resp.warning) addFlash("warning", resp.warning);
      setEditing(null);
      load();
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

  const workloadOptions: SelectProps.Option[] = workloads.map((w) => ({
    value: w.name,
    label: w.name,
    description: w.phase,
  }));

  const selectedWorkloadOption =
    workloadOptions.find((o) => o.value === createForm.workload_name) ?? null;

  const editWorkloadOption =
    workloadOptions.find((o) => o.value === editForm.workload_name) ?? null;

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          description="Named TCP endpoints for container-to-container communication. Each service gets a stable port and is reachable via DNS at <name>.svc.local."
          actions={<Button iconName="refresh" onClick={load}>Refresh</Button>}
        >
          Services
        </Header>
      }
    >
      <Table
        loading={loading}
        loadingText="Loading services"
        header={
          <Header
            actions={
              <SpaceBetween direction="horizontal" size="xs">
                <Button disabled={selected.length === 0} onClick={handleDelete}>
                  Delete
                </Button>
                <Button variant="primary" onClick={() => setCreating(true)}>
                  Create service
                </Button>
              </SpaceBetween>
            }
          >
            Services
          </Header>
        }
        selectionType="multi"
        selectedItems={selected}
        onSelectionChange={(e) => setSelected(e.detail.selectedItems)}
        trackBy="id"
        columnDefinitions={[
          { id: "id", header: "ID", cell: (s) => s.id },
          { id: "name", header: "Name", cell: (s) => s.name },
          { id: "system_port", header: "System port", cell: (s) => s.system_port },
          { id: "workload", header: "Workload", cell: (s) => s.workload_name },
          { id: "target_port", header: "Target port", cell: (s) => s.target_port },
          { id: "dns", header: "DNS", cell: (s) => `${s.name}.svc.local` },
          { id: "age", header: "Age", cell: (s) => formatAge(s.created_at) },
          {
            id: "actions",
            header: "",
            cell: (s) => (
              <Button variant="inline-link" onClick={() => openEdit(s)}>
                Edit
              </Button>
            ),
          },
        ]}
        items={services}
        empty="No services"
      />

      {/* ── Create modal ─────────────────────────────────────────────── */}
      <Modal
        visible={creating}
        onDismiss={() => setCreating(false)}
        header="Create service"
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
            <FormField label="Workload" constraintText="Required">
              <Select
                filteringType="auto"
                options={workloadOptions}
                selectedOption={selectedWorkloadOption}
                onChange={(e) =>
                  setCreateForm((f) => ({ ...f, workload_name: e.detail.selectedOption.value ?? "" }))
                }
                placeholder="Select a workload"
                empty="No workloads available"
              />
            </FormField>
            <FormField label="Container port" description="Port the container listens on" constraintText="Required">
              <Input
                type="number"
                value={createForm.target_port}
                onChange={(e) => setCreateForm((f) => ({ ...f, target_port: e.detail.value }))}
                placeholder="3000"
              />
            </FormField>
          </SpaceBetween>
        </Form>
      </Modal>

      {/* ── Edit modal ───────────────────────────────────────────────── */}
      <Modal
        visible={editing !== null}
        onDismiss={() => setEditing(null)}
        header={`Edit service — ${editing?.id}`}
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => setEditing(null)}>
              Cancel
            </Button>
            <Button variant="primary" onClick={handleUpdate}>
              Save
            </Button>
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
    </ContentLayout>
  );
}
