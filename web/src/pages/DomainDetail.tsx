import { useCallback, useEffect, useState } from "react";
import {
  Alert,
  Badge,
  Box,
  Button,
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
  Table,
  Textarea,
} from "@cloudscape-design/components";
import { api, Domain, IngressRule, Service, CreateDomainRequest } from "../api";
import { formatAge } from "../api";

type ModalMode = "edit" | "credentials" | "import" | "add-route" | "delete-confirm";

interface Props {
  domainId: string;
  onNavigate: (page: string) => void;
}

function certPreview(cert: string): string {
  if (!cert) return "—";
  const lines = cert.trim().split("\n");
  return lines[0] + (lines.length > 1 ? " …" : "");
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

export default function DomainDetail({ domainId, onNavigate }: Props) {
  const [domain, setDomain] = useState<Domain | null>(null);
  const [services, setServices] = useState<Service[]>([]);
  const [rules, setRules] = useState<IngressRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [mode, setMode] = useState<ModalMode | null>(null);
  const [generated, setGenerated] = useState<Domain | null>(null);
  const [editForm, setEditForm] = useState<CreateDomainRequest>({ name: "", tls_cert: "", tls_key: "" });
  const [importCert, setImportCert] = useState("");
  const [routePathPrefix, setRoutePathPrefix] = useState("");
  const [routeServiceOption, setRouteServiceOption] = useState<SelectProps.Option | null>(null);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  const load = useCallback(async () => {
    try {
      const [allDomains, allRules, allServices] = await Promise.all([
        api.listDomains(),
        api.listIngress(),
        api.listServices(),
      ]);
      const d = (allDomains ?? []).find((x) => x.id === domainId) ?? null;
      setDomain(d);
      setRules((allRules ?? []).filter((r) => r.domain_id === domainId));
      setServices(allServices ?? []);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setLoading(false);
    }
  }, [domainId]);

  useEffect(() => { load(); }, [load]);

  function closeModal() {
    setMode(null);
    setGenerated(null);
    setImportCert("");
    setRoutePathPrefix("");
    setRouteServiceOption(null);
  }

  function openEdit() {
    if (!domain) return;
    setEditForm({ name: domain.name, tls_cert: domain.tls_cert, tls_key: "" });
    setMode("edit");
  }

  async function handleUpdate() {
    if (!domain) return;
    try {
      const resp = await api.updateDomain(domain.id, editForm);
      const userProvidedCert = !!(editForm.tls_cert && editForm.tls_key);
      const certChanged = resp.tls_cert !== domain.tls_cert;
      if (!userProvidedCert && certChanged) {
        setGenerated(resp);
        setMode("credentials");
      } else {
        addFlash("success", `Domain ${resp.name} updated`);
        closeModal();
      }
      load();
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleToggle() {
    if (!domain) return;
    try {
      const resp = await api.toggleDomain(domain.id);
      setDomain(resp);
      addFlash("success", `${resp.name} ${resp.enabled ? "enabled" : "disabled"}`);
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleDelete() {
    if (!domain) return;
    try {
      await api.deleteDomain(domain.id);
      onNavigate("domains");
    } catch (e) {
      addFlash("error", String(e));
      closeModal();
    }
  }

  async function handleRegenerateKeys() {
    if (!domain) return;
    try {
      const resp = await api.regenerateDomainKeys(domain.id);
      setGenerated(resp);
      setMode("credentials");
      load();
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleImportCert() {
    if (!domain || !importCert.trim()) {
      addFlash("error", "Certificate is required");
      return;
    }
    try {
      await api.importDomainCert(domain.id, importCert.trim());
      addFlash("success", `Certificate updated for ${domain.name}`);
      closeModal();
      load();
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleAddRoute() {
    if (!domain || !routeServiceOption?.value) {
      addFlash("error", "Service is required");
      return;
    }
    try {
      await api.createIngress({
        domain_id: domain.id,
        path_prefix: routePathPrefix.trim() || "/",
        service_name: routeServiceOption.value,
      });
      addFlash("success", "Route added");
      closeModal();
      load();
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleRemoveRoute(ruleId: string) {
    try {
      await api.deleteIngress(ruleId);
      load();
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  const serviceOptions: SelectProps.Option[] = services.map((s) => ({
    value: s.name,
    label: s.name,
    description: `→ ${s.workload_name}:${s.target_port}`,
  }));

  const modalTitle =
    mode === "credentials" ? "Credentials generated" :
    mode === "import" ? "Import signed certificate" :
    mode === "edit" ? `Edit — ${domain?.name}` :
    mode === "add-route" ? "Add route" :
    mode === "delete-confirm" ? "Delete domain" :
    "";

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          description={domain ? `Created ${formatAge(domain.created_at)} ago` : ""}
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button iconName="refresh" onClick={load}>Refresh</Button>
              <Button variant="link" iconName="angle-left" onClick={() => onNavigate("domains")}>
                Domains
              </Button>
            </SpaceBetween>
          }
        >
          {loading ? domainId : (domain?.name ?? domainId)}
          {domain && (
            <>
              {" "}
              {domain.enabled
                ? <Badge color="green">Enabled</Badge>
                : <Badge color="grey">Disabled</Badge>}
            </>
          )}
        </Header>
      }
    >
      {loading ? (
        <Box>Loading…</Box>
      ) : !domain ? (
        <Box>Domain not found.</Box>
      ) : (
        <SpaceBetween size="l">

          {/* ── Identity ──────────────────────────────────────────────── */}
          <Container
            header={
              <Header
                variant="h2"
                actions={
                  <SpaceBetween direction="horizontal" size="xs">
                    <Button onClick={openEdit}>Edit</Button>
                    <Button onClick={handleToggle}>
                      {domain.enabled ? "Disable" : "Enable"}
                    </Button>
                  </SpaceBetween>
                }
              >
                Domain
              </Header>
            }
          >
            <SpaceBetween size="s">
              <div>
                <Box variant="awsui-key-label">Hostname</Box>
                <Box>{domain.name}</Box>
              </div>
              <div>
                <Box variant="awsui-key-label">Status</Box>
                <Box>
                  {domain.enabled
                    ? <Badge color="green">Enabled</Badge>
                    : <Badge color="grey">Disabled</Badge>}
                </Box>
              </div>
            </SpaceBetween>
          </Container>

          {/* ── Certificate ───────────────────────────────────────────── */}
          <Container
            header={
              <Header
                variant="h2"
                actions={
                  <SpaceBetween direction="horizontal" size="xs">
                    <Button
                      disabled={!domain.csr}
                      onClick={() => downloadCSR(domain)}
                    >
                      Download CSR
                    </Button>
                    <Button
                      disabled={!domain.csr}
                      onClick={() => setMode("import")}
                    >
                      Import signed cert
                    </Button>
                    <Button onClick={handleRegenerateKeys}>Regenerate keys</Button>
                  </SpaceBetween>
                }
              >
                Certificate
              </Header>
            }
          >
            <SpaceBetween size="s">
              <div>
                <Box variant="awsui-key-label">Certificate</Box>
                <Box variant="code">{certPreview(domain.tls_cert)}</Box>
              </div>
              {domain.csr && (
                <div>
                  <Box variant="awsui-key-label">CSR</Box>
                  <Box variant="code">{certPreview(domain.csr)}</Box>
                </div>
              )}
            </SpaceBetween>
          </Container>

          {/* ── Routes ────────────────────────────────────────────────── */}
          <Table
            header={
              <Header
                variant="h2"
                description="HTTP/S routes that use this domain's certificate. Each route maps a path prefix to a service."
                actions={
                  <Button
                    variant="primary"
                    disabled={services.length === 0}
                    onClick={() => setMode("add-route")}
                  >
                    Add route
                  </Button>
                }
              >
                Routes
              </Header>
            }
            columnDefinitions={[
              { id: "path", header: "Path prefix", cell: (r) => r.path_prefix || "/" },
              { id: "service", header: "Service", cell: (r) => r.service_name },
              {
                id: "actions",
                header: "",
                cell: (r) => (
                  <Button variant="inline-link" onClick={() => handleRemoveRoute(r.id)}>
                    Remove
                  </Button>
                ),
              },
            ]}
            items={rules}
            empty={
              services.length === 0
                ? "No services defined — create a service first, then add a route."
                : "No routes — click Add route to connect a service to this domain."
            }
          />

          {/* ── Danger zone ───────────────────────────────────────────── */}
          <Container header={<Header variant="h2">Danger zone</Header>}>
            <Button
              variant="normal"
              onClick={() => setMode("delete-confirm")}
            >
              Delete domain
            </Button>
          </Container>

        </SpaceBetween>
      )}

      {/* ── Modals ────────────────────────────────────────────────────── */}
      <Modal
        visible={mode !== null}
        onDismiss={closeModal}
        header={modalTitle}
        footer={
          mode === "credentials" ? (
            <Button
              variant="primary"
              onClick={() => {
                addFlash("success", `Domain ${generated?.name} updated`);
                closeModal();
              }}
            >
              Done
            </Button>
          ) : mode === "delete-confirm" ? (
            <SpaceBetween direction="horizontal" size="xs">
              <Button variant="link" onClick={closeModal}>Cancel</Button>
              <Button variant="primary" onClick={handleDelete}>Delete</Button>
            </SpaceBetween>
          ) : (
            <SpaceBetween direction="horizontal" size="xs">
              <Button variant="link" onClick={closeModal}>Cancel</Button>
              <Button
                variant="primary"
                onClick={
                  mode === "import" ? handleImportCert :
                  mode === "add-route" ? handleAddRoute :
                  handleUpdate
                }
              >
                {mode === "import" ? "Import" : mode === "add-route" ? "Add" : "Save"}
              </Button>
            </SpaceBetween>
          )
        }
      >
        {mode === "credentials" && (
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
                description="Submit this to your CA to get a signed certificate."
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
        )}

        {mode === "import" && (
          <Form>
            <SpaceBetween size="m">
              <Alert type="info">
                Paste the CA-signed certificate below. The existing private key and CSR will not change.
              </Alert>
              <FormField label="Signed Certificate (PEM)" constraintText="Required">
                <Textarea
                  value={importCert}
                  onChange={(e) => setImportCert(e.detail.value)}
                  placeholder="-----BEGIN CERTIFICATE-----"
                  rows={10}
                />
              </FormField>
            </SpaceBetween>
          </Form>
        )}

        {mode === "edit" && (
          <Form>
            <SpaceBetween size="m">
              <FormField label="Domain name" description="Hostname matched by SNI, e.g. api.example.com">
                <Input
                  value={editForm.name ?? ""}
                  onChange={(e) => setEditForm((f) => ({ ...f, name: e.detail.value }))}
                  placeholder="api.example.com"
                />
              </FormField>
              <FormField
                label="TLS Certificate (PEM)"
                description="Leave both cert and key empty to regenerate a self-signed certificate"
              >
                <Textarea
                  value={editForm.tls_cert ?? ""}
                  onChange={(e) => setEditForm((f) => ({ ...f, tls_cert: e.detail.value }))}
                  placeholder="-----BEGIN CERTIFICATE-----"
                  rows={6}
                />
              </FormField>
              <FormField
                label="TLS Private Key (PEM)"
                description="Private key is never returned by the server. Leave empty to keep the existing key, or provide a new key together with a matching certificate."
              >
                <Textarea
                  value={editForm.tls_key ?? ""}
                  onChange={(e) => setEditForm((f) => ({ ...f, tls_key: e.detail.value }))}
                  placeholder="-----BEGIN EC PRIVATE KEY-----"
                  rows={6}
                />
              </FormField>
            </SpaceBetween>
          </Form>
        )}

        {mode === "add-route" && (
          <Form>
            <SpaceBetween size="m">
              <FormField
                label="Path prefix"
                description="Requests whose path starts with this prefix are routed to the service. Leave empty or use / to match all paths."
              >
                <Input
                  value={routePathPrefix}
                  onChange={(e) => setRoutePathPrefix(e.detail.value)}
                  placeholder="/"
                />
              </FormField>
              <FormField label="Service" constraintText="Required">
                <Select
                  options={serviceOptions}
                  selectedOption={routeServiceOption}
                  onChange={(e) => setRouteServiceOption(e.detail.selectedOption)}
                  placeholder="Select a service"
                />
              </FormField>
            </SpaceBetween>
          </Form>
        )}

        {mode === "delete-confirm" && (
          <Alert type="warning">
            Delete <strong>{domain?.name}</strong>? This will also remove all routes attached to this domain. This cannot be undone.
          </Alert>
        )}
      </Modal>
    </ContentLayout>
  );
}
