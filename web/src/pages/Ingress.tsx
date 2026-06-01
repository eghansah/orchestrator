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
import { api, IngressRule, Domain } from "../api";
import { formatAge } from "../api";

export default function Ingress() {
  const [rules, setRules] = useState<IngressRule[]>([]);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [creating, setCreating] = useState(false);
  const [createForm, setCreateForm] = useState({
    domain_id: "",
    path_prefix: "/",
    workload_id: "",
    port: "",
  });
  const [selected, setSelected] = useState<IngressRule[]>([]);

  const load = useCallback(async () => {
    try {
      const [rulesData, domainsData] = await Promise.all([
        api.listIngress(),
        api.listDomains(),
      ]);
      setRules(rulesData ?? []);
      setDomains(domainsData ?? []);
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
    if (!createForm.domain_id) {
      addFlash("error", "A domain is required");
      return;
    }
    if (!createForm.workload_id || isNaN(port) || port <= 0) {
      addFlash("error", "Workload ID and a valid port are required");
      return;
    }
    try {
      const resp = await api.createIngress({
        domain_id: createForm.domain_id,
        path_prefix: createForm.path_prefix || "/",
        workload_id: createForm.workload_id,
        port,
      });
      if (!resp.accepted) {
        addFlash("error", "rejected");
      } else {
        addFlash("success", `Rule ${resp.rule_id} created`);
        setCreating(false);
        setCreateForm({ domain_id: "", path_prefix: "/", workload_id: "", port: "" });
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

  const domainOptions: SelectProps.Option[] = domains.map((d) => ({
    value: d.id,
    label: d.name,
    disabled: !d.enabled,
    description: d.enabled ? undefined : "disabled",
  }));

  const selectedDomainOption =
    domainOptions.find((o) => o.value === createForm.domain_id) ?? null;

  const domainNameById = (id: string) =>
    domains.find((d) => d.id === id)?.name ?? id;

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          description="HTTP routing rules that direct inbound traffic to containers by domain and URL path prefix. Longest prefix wins."
        >
          Ingress
        </Header>
      }
    >
      <Table
        loading={loading}
        loadingText="Loading ingress rules"
        header={
          <Header
            actions={
              <SpaceBetween direction="horizontal" size="xs">
                <Button disabled={selected.length === 0} onClick={handleDelete}>
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
          { id: "domain", header: "Domain", cell: (r) => domainNameById(r.domain_id) || r.host || "—" },
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
            <FormField
              label="Domain"
              description="The registered domain this rule will match on"
              constraintText="Required"
            >
              <Select
                options={domainOptions}
                selectedOption={selectedDomainOption}
                onChange={(e) =>
                  setCreateForm((f) => ({ ...f, domain_id: e.detail.selectedOption.value ?? "" }))
                }
                placeholder="Select a domain"
                empty="No domains registered — create one in the Domains page first"
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
            <FormField
              label="Container port"
              description="Port the container listens on (e.g. 80 for nginx)"
              constraintText="Required"
            >
              <Input
                type="number"
                value={createForm.port}
                onChange={(e) => setCreateForm((f) => ({ ...f, port: e.detail.value }))}
                placeholder="8080"
              />
            </FormField>
          </SpaceBetween>
        </Form>
      </Modal>
    </ContentLayout>
  );
}
