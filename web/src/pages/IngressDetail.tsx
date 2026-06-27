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
import { api, IngressRule, Domain } from "../api";
import { formatAge } from "../api";

interface Props {
  ruleId: string;
  onNavigate: (page: string) => void;
}

type ModalMode = "edit" | "delete-confirm" | null;

export default function IngressDetail({ ruleId, onNavigate }: Props) {
  const [rule, setRule] = useState<IngressRule | null>(null);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [mode, setMode] = useState<ModalMode>(null);
  const [editForm, setEditForm] = useState({
    domain_id: "",
    path_prefix: "/",
    container_fqdn: "",
    container_port: "",
  });

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  const load = useCallback(async () => {
    try {
      const [rules, domainsData] = await Promise.all([api.listIngress(), api.listDomains()]);
      setDomains(domainsData ?? []);
      setRule((rules ?? []).find((r) => r.id === ruleId) ?? null);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLoading(false);
    }
  }, [ruleId]);

  useEffect(() => { load(); }, [load]);

  function openEdit() {
    if (!rule) return;
    setEditForm({
      domain_id: rule.domain_id,
      path_prefix: rule.path_prefix || "/",
      container_fqdn: rule.container_fqdn,
      container_port: String(rule.container_port),
    });
    setMode("edit");
  }

  async function handleUpdate() {
    if (!rule) return;
    const port = parseInt(editForm.container_port, 10);
    if (!editForm.domain_id || !editForm.container_fqdn || isNaN(port) || port <= 0) {
      addFlash("error", "Domain, container FQDN, and a valid container port are required");
      return;
    }
    try {
      await api.updateIngress(rule.id, {
        domain_id: editForm.domain_id,
        path_prefix: editForm.path_prefix || "/",
        container_fqdn: editForm.container_fqdn,
        container_port: port,
      });
      addFlash("success", "Web service updated");
      setMode(null);
      load();
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleDelete() {
    if (!rule) return;
    try {
      await api.deleteIngress(rule.id);
      onNavigate("web-services");
    } catch (e) {
      addFlash("error", String(e));
      setMode(null);
    }
  }

  const domainNameById = (id: string) => domains.find((d) => d.id === id)?.name ?? id;

  const domainOptions: SelectProps.Option[] = domains.map((d) => ({
    value: d.id,
    label: d.name,
    disabled: !d.enabled,
    description: d.enabled ? undefined : "disabled",
  }));

  const selectedDomainOption =
    domainOptions.find((o) => o.value === editForm.domain_id) ?? null;

  if (loading) return <Spinner />;
  if (!rule) {
    return (
      <ContentLayout header={<Header variant="h1">Web service not found</Header>}>
        <Button variant="inline-link" onClick={() => onNavigate("web-services")}>
          ← Back to Web Services
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
          {domainNameById(rule.domain_id)}{rule.path_prefix && rule.path_prefix !== "/" ? rule.path_prefix : ""}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Button variant="inline-link" onClick={() => onNavigate("web-services")}>
          ← Back to Web Services
        </Button>

        <Container header={<Header variant="h2">Web service details</Header>}>
          <ColumnLayout columns={3} variant="text-grid">
            <div>
              <Box variant="awsui-key-label">Domain</Box>
              <Box>{domainNameById(rule.domain_id)}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Path prefix</Box>
              <Box>{rule.path_prefix || "/"}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">ID</Box>
              <Box>{rule.id}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Container FQDN</Box>
              <Box>{rule.container_fqdn}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Container port</Box>
              <Box>{rule.container_port}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">System port</Box>
              <Box>{rule.system_port}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Age</Box>
              <Box>{formatAge(rule.created_at)}</Box>
            </div>
          </ColumnLayout>
        </Container>
      </SpaceBetween>

      {/* ── Edit modal ─────────────────────────────────────────────────── */}
      <Modal
        visible={mode === "edit"}
        onDismiss={() => setMode(null)}
        header="Edit web service"
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => setMode(null)}>Cancel</Button>
            <Button variant="primary" onClick={handleUpdate}>Save</Button>
          </SpaceBetween>
        }
      >
        <Form>
          <SpaceBetween size="m">
            <FormField label="Domain" constraintText="Required">
              <Select
                filteringType="auto"
                options={domainOptions}
                selectedOption={selectedDomainOption}
                onChange={(e) =>
                  setEditForm((f) => ({ ...f, domain_id: e.detail.selectedOption.value ?? "" }))
                }
                placeholder="Select a domain"
              />
            </FormField>
            <FormField label="Path prefix" description="URL path prefix to match">
              <Input
                value={editForm.path_prefix}
                onChange={(e) => setEditForm((f) => ({ ...f, path_prefix: e.detail.value }))}
                placeholder="/"
              />
            </FormField>
            <FormField label="Container FQDN" constraintText="Required">
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
                placeholder="8080"
              />
            </FormField>
          </SpaceBetween>
        </Form>
      </Modal>

      {/* ── Delete confirm modal ───────────────────────────────────────── */}
      <Modal
        visible={mode === "delete-confirm"}
        onDismiss={() => setMode(null)}
        header="Delete web service"
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => setMode(null)}>Cancel</Button>
            <Button variant="primary" onClick={handleDelete}>Delete</Button>
          </SpaceBetween>
        }
      >
        Are you sure you want to delete the web service for <strong>{domainNameById(rule.domain_id)}{rule.path_prefix}</strong>? This cannot be undone.
      </Modal>
    </ContentLayout>
  );
}
