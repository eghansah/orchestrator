import { useCallback, useEffect, useState } from "react";
import {
  Button,
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
import { api, IngressRule } from "../api";
import { formatAge } from "../api";

export default function Ingress() {
  const [rules, setRules] = useState<IngressRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [creating, setCreating] = useState(false);
  const [createForm, setCreateForm] = useState({
    host: "",
    path_prefix: "/",
    workload_id: "",
    port: "",
  });
  const [selected, setSelected] = useState<IngressRule[]>([]);

  const load = useCallback(async () => {
    try {
      const data = await api.listIngress();
      setRules(data ?? []);
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
    const port = parseInt(createForm.port, 10);
    if (!createForm.workload_id || isNaN(port) || port <= 0) {
      addFlash("error", "Workload ID and a valid port are required");
      return;
    }
    try {
      const resp = await api.createIngress({
        host: createForm.host,
        path_prefix: createForm.path_prefix || "/",
        workload_id: createForm.workload_id,
        port,
      });
      if (!resp.accepted) {
        addFlash("error", resp.reason ?? "rejected");
      } else {
        addFlash("success", `Rule ${resp.rule_id} created`);
        setCreating(false);
        setCreateForm({ host: "", path_prefix: "/", workload_id: "", port: "" });
        load();
      }
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleDelete() {
    for (const rule of selected) {
      try {
        await api.deleteIngress(rule.id);
        addFlash("success", `Deleted ${rule.id}`);
      } catch (e) {
        addFlash("error", String(e));
      }
    }
    setSelected([]);
    load();
  }

  return (
    <SpaceBetween size="m">
      <Flashbar items={flash} />
      <Table
        loading={loading}
        loadingText="Loading ingress rules"
        header={
          <Header
            variant="h1"
            actions={
              <SpaceBetween direction="horizontal" size="xs">
                <Button
                  disabled={selected.length === 0}
                  onClick={handleDelete}
                >
                  Delete
                </Button>
                <Button variant="primary" onClick={() => setCreating(true)}>
                  Create rule
                </Button>
              </SpaceBetween>
            }
          >
            Ingress rules
          </Header>
        }
        selectionType="multi"
        selectedItems={selected}
        onSelectionChange={(e) => setSelected(e.detail.selectedItems)}
        trackBy="id"
        columnDefinitions={[
          { id: "id", header: "ID", cell: (r) => r.id },
          { id: "host", header: "Host", cell: (r) => r.host || "—" },
          { id: "path", header: "Path", cell: (r) => r.path_prefix || "/" },
          { id: "workload", header: "Workload", cell: (r) => r.workload_id },
          { id: "port", header: "Port", cell: (r) => r.port },
          { id: "age", header: "Age", cell: (r) => formatAge(r.created_at) },
        ]}
        items={rules}
        empty="No ingress rules"
      />

      <Modal
        visible={creating}
        onDismiss={() => setCreating(false)}
        header="Create ingress rule"
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
            <FormField label="Host" description="Host header to match (leave empty to match all)">
              <Input
                value={createForm.host}
                onChange={(e) => setCreateForm((f) => ({ ...f, host: e.detail.value }))}
                placeholder="example.com"
              />
            </FormField>
            <FormField label="Path prefix" description="URL path prefix to match">
              <Input
                value={createForm.path_prefix}
                onChange={(e) => setCreateForm((f) => ({ ...f, path_prefix: e.detail.value }))}
                placeholder="/"
              />
            </FormField>
            <FormField label="Workload ID" constraintText="Required">
              <Input
                value={createForm.workload_id}
                onChange={(e) => setCreateForm((f) => ({ ...f, workload_id: e.detail.value }))}
              />
            </FormField>
            <FormField label="Port" description="Host port on the target node" constraintText="Required">
              <Input
                type="number"
                value={createForm.port}
                onChange={(e) => setCreateForm((f) => ({ ...f, port: e.detail.value }))}
                placeholder="8888"
              />
            </FormField>
          </SpaceBetween>
        </Form>
      </Modal>
    </SpaceBetween>
  );
}
