import { useCallback, useEffect, useState } from "react";
import {
  Alert,
  Badge,
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
  Textarea,
} from "@cloudscape-design/components";
import { api, Domain, CreateDomainRequest } from "../api";
import { formatAge } from "../api";

interface Props {
  onNavigate: (page: string) => void;
}

export default function Domains({ onNavigate }: Props) {
  const [domains, setDomains] = useState<Domain[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [showCreate, setShowCreate] = useState(false);
  const [showCredentials, setShowCredentials] = useState(false);
  const [form, setForm] = useState<CreateDomainRequest>({ name: "", tls_cert: "", tls_key: "" });
  const [generated, setGenerated] = useState<Domain | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await api.listDomains();
      setDomains(data ?? []);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  function openCreate() {
    setForm({ name: "", tls_cert: "", tls_key: "" });
    setShowCreate(true);
  }

  function closeCreate() {
    setShowCreate(false);
    setForm({ name: "", tls_cert: "", tls_key: "" });
  }

  function downloadCSR(domain: Domain) {
    if (!domain.csr) return;
    const blob = new Blob([domain.csr], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${domain.name}.csr.pem`;
    a.click();
    URL.revokeObjectURL(url);
  }

  async function handleCreate() {
    if (!form.name) { addFlash("error", "Domain name is required"); return; }
    try {
      const resp = await api.createDomain(form);
      const userProvidedCert = !!(form.tls_cert && form.tls_key);
      closeCreate();
      load();
      if (!userProvidedCert) {
        setGenerated(resp);
        setShowCredentials(true);
      } else {
        addFlash("success", `Domain ${resp.name} created`);
      }
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleToggle(domain: Domain) {
    try {
      const resp = await api.toggleDomain(domain.id);
      addFlash("success", `${resp.name} ${resp.enabled ? "enabled" : "disabled"}`);
      load();
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          description="Named TLS certificates for HTTPS ingress termination. Domains are matched by SNI hostname."
          actions={<Button iconName="refresh" onClick={load}>Refresh</Button>}
        >
          Domains
        </Header>
      }
    >
      <Table
        loading={loading}
        loadingText="Loading domains"
        header={
          <Header
            actions={
              <Button variant="primary" onClick={openCreate}>
                Create domain
              </Button>
            }
          >
            Domains
          </Header>
        }
        columnDefinitions={[
          {
            id: "name",
            header: "Name",
            cell: (d) => (
              <Button variant="inline-link" onClick={() => onNavigate("domain-" + d.id)}>
                {d.name}
              </Button>
            ),
          },
          {
            id: "status",
            header: "Status",
            cell: (d) =>
              d.enabled ? (
                <Badge color="green">Enabled</Badge>
              ) : (
                <Badge color="grey">Disabled</Badge>
              ),
          },
          { id: "age", header: "Age", cell: (d) => formatAge(d.created_at) },
          {
            id: "actions",
            header: "",
            cell: (d) => (
              <Button variant="inline-link" onClick={() => handleToggle(d)}>
                {d.enabled ? "Disable" : "Enable"}
              </Button>
            ),
          },
        ]}
        items={domains}
        empty="No domains"
      />

      {/* Create domain modal */}
      <Modal
        visible={showCreate}
        onDismiss={closeCreate}
        header="Create domain"
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={closeCreate}>Cancel</Button>
            <Button variant="primary" onClick={handleCreate}>Create</Button>
          </SpaceBetween>
        }
      >
        <Form>
          <SpaceBetween size="m">
            <FormField
              label="Domain name"
              description="Hostname matched by SNI, e.g. api.example.com"
              constraintText="Required — must be unique"
            >
              <Input
                value={form.name ?? ""}
                onChange={(e) => setForm((f) => ({ ...f, name: e.detail.value }))}
                placeholder="api.example.com"
              />
            </FormField>
            <FormField
              label="TLS Certificate (PEM)"
              description="Leave empty to auto-generate a self-signed certificate"
            >
              <Textarea
                value={form.tls_cert ?? ""}
                onChange={(e) => setForm((f) => ({ ...f, tls_cert: e.detail.value }))}
                placeholder="-----BEGIN CERTIFICATE-----"
                rows={6}
              />
            </FormField>
            <FormField
              label="TLS Private Key (PEM)"
              description="Leave empty to auto-generate alongside the certificate"
            >
              <Textarea
                value={form.tls_key ?? ""}
                onChange={(e) => setForm((f) => ({ ...f, tls_key: e.detail.value }))}
                placeholder="-----BEGIN EC PRIVATE KEY-----"
                rows={6}
              />
            </FormField>
          </SpaceBetween>
        </Form>
      </Modal>

      {/* Credentials modal — shown after auto-generate on create */}
      <Modal
        visible={showCredentials}
        onDismiss={() => setShowCredentials(false)}
        header="Credentials generated"
        footer={
          <Button
            variant="primary"
            onClick={() => {
              addFlash("success", `Domain ${generated?.name} created`);
              setShowCredentials(false);
              setGenerated(null);
            }}
          >
            Done
          </Button>
        }
      >
        <SpaceBetween size="m">
          <Alert type="warning">
            A self-signed certificate was generated for <strong>{generated?.name}</strong>.
            Copy and save the private key now — it will not be shown again after you close this dialog.
          </Alert>
          <FormField label="TLS Certificate (PEM)">
            <Textarea readOnly value={generated?.tls_cert ?? ""} rows={8} />
          </FormField>
          <FormField label="TLS Private Key (PEM)">
            <Textarea readOnly value={generated?.tls_key ?? ""} rows={8} />
          </FormField>
          {generated?.csr && (
            <FormField
              label="Certificate Signing Request (PEM)"
              description="Submit this to your CA to get a signed certificate. Use 'Import signed cert' on the domain detail page once you receive it."
            >
              <Textarea readOnly value={generated.csr} rows={8} />
            </FormField>
          )}
          {generated?.csr && (
            <Button iconName="download" onClick={() => generated && downloadCSR(generated)}>
              Download CSR
            </Button>
          )}
        </SpaceBetween>
      </Modal>
    </ContentLayout>
  );
}
