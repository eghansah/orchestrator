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
    container_fqdn: "",
    container_port: "",
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
    const port = parseInt(createForm.container_port, 10);
    if (!createForm.domain_id) {
      addFlash("error", "A domain is required");
      return;
    }
    if (!createForm.container_fqdn) {
      addFlash("error", "A container FQDN is required");
      return;
    }
    if (isNaN(port) || port <= 0) {
      addFlash("error", "A valid container port is required");
      return;
    }
    try {
      const resp = await api.createIngress({
        domain_id: createForm.domain_id,
        path_prefix: createForm.path_prefix || "/",
        container_fqdn: createForm.container_fqdn,
        container_port: port,
      });
      if (!resp.accepted) {
        addFlash("error", "rejected");
      } else {
        addFlash("success", `Web service ${resp.rule_id} created (system port ${resp.system_port})`);
        setCreating(false);
        setCreateForm({ domain_id: "", path_prefix: "/", container_fqdn: "", container_port: "" });
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
          description="HTTP/HTTPS routing rules that direct inbound web traffic to workloads by domain and URL path prefix. Internally, ingressd drives an HAProxy frontend that matches the Host header and path, then forwards to the TCP service backing the workload. Longest path prefix wins."
          actions={<Button iconName="refresh" onClick={load}>Refresh</Button>}
        >
          Web Services
        </Header>
      }
    >
      <Table
        loading={loading}
        loadingText="Loading web services"
        header={
          <Header
            actions={
              <SpaceBetween direction="horizontal" size="xs">
                <Button disabled={selected.length === 0} onClick={handleDelete}>
                  Delete
                </Button>
                <Button variant="primary" onClick={() => setCreating(true)}>
                  Create web service
                </Button>
              </SpaceBetween>
            }
          >
            Web Services
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
          { id: "container_fqdn", header: "Container FQDN", cell: (r) => r.container_fqdn },
          { id: "container_port", header: "Container port", cell: (r) => r.container_port },
          { id: "system_port", header: "System port", cell: (r) => r.system_port },
          { id: "age", header: "Age", cell: (r) => formatAge(r.created_at) },
        ]}
        items={rules}
        empty="No web services"
      />

      <Modal
        visible={creating}
        onDismiss={() => setCreating(false)}
        header="Create web service"
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
                filteringType="auto"
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
            <FormField
              label="Container FQDN"
              description='Container name, e.g. "ecouniversal" or "backend.myapp" for compose'
              constraintText="Required"
            >
              <Input
                value={createForm.container_fqdn}
                onChange={(e) => setCreateForm((f) => ({ ...f, container_fqdn: e.detail.value }))}
                placeholder="ecouniversal"
              />
            </FormField>
            <FormField
              label="Container port"
              description="Port the container listens on"
              constraintText="Required"
            >
              <Input
                type="number"
                value={createForm.container_port}
                onChange={(e) => setCreateForm((f) => ({ ...f, container_port: e.detail.value }))}
                placeholder="8080"
              />
            </FormField>
          </SpaceBetween>
        </Form>
      </Modal>
    </ContentLayout>
  );
}
