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

type ModalMode = "create" | "edit" | "credentials";

export default function Domains() {
  const [domains, setDomains] = useState<Domain[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [mode, setMode] = useState<ModalMode | null>(null);
  const [editTarget, setEditTarget] = useState<Domain | null>(null);
  const [form, setForm] = useState<CreateDomainRequest>({ name: "", tls_cert: "", tls_key: "" });
  const [generated, setGenerated] = useState<Domain | null>(null);
  const [selected, setSelected] = useState<Domain[]>([]);

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

  useEffect(() => {
    load();
  }, [load]);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  function openCreate() {
    setEditTarget(null);
    setForm({ name: "", tls_cert: "", tls_key: "" });
    setMode("create");
  }

  function openEdit(domain: Domain) {
    setEditTarget(domain);
    // tls_key is never returned by the server after creation; leave it blank.
    // Submitting with both cert and key empty triggers server-side regeneration;
    // submitting with only the key blank preserves the existing key.
    setForm({ name: domain.name, tls_cert: domain.tls_cert, tls_key: "" });
    setMode("edit");
  }

  function closeModal() {
    setMode(null);
    setEditTarget(null);
    setGenerated(null);
    setForm({ name: "", tls_cert: "", tls_key: "" });
  }

  async function handleCreate() {
    if (!form.name) { addFlash("error", "Domain name is required"); return; }
    try {
      const resp = await api.createDomain(form);
      const userProvidedCert = !!(form.tls_cert && form.tls_key);
      if (!userProvidedCert) {
        setGenerated(resp);
        setMode("credentials");
      } else {
        addFlash("success", `Domain ${resp.name} created`);
        closeModal();
        load();
      }
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleUpdate() {
    if (!editTarget) return;
    try {
      const resp = await api.updateDomain(editTarget.id, form);
      // Cert/key left empty means regenerate — show them if they changed.
      const userProvidedCert = !!(form.tls_cert && form.tls_key);
      const certChanged = resp.tls_cert !== editTarget.tls_cert;
      if (!userProvidedCert && certChanged) {
        setGenerated(resp);
        setMode("credentials");
      } else {
        addFlash("success", `Domain ${resp.name} updated`);
        closeModal();
        load();
      }
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleToggle() {
    for (const d of selected) {
      try {
        const resp = await api.toggleDomain(d.id);
        addFlash("success", `${resp.name} ${resp.enabled ? "enabled" : "disabled"}`);
      } catch (e) {
        addFlash("error", String(e));
      }
    }
    setSelected([]);
    load();
  }

  async function handleDelete() {
    for (const d of selected) {
      try {
        await api.deleteDomain(d.id);
        addFlash("success", `Deleted ${d.name}`);
      } catch (e) {
        addFlash("error", String(e));
      }
    }
    setSelected([]);
    load();
  }

  function certPreview(cert: string): string {
    if (!cert) return "—";
    const lines = cert.trim().split("\n");
    return lines[0] + (lines.length > 1 ? " …" : "");
  }

  const isEditing = mode === "edit";
  const modalTitle =
    mode === "credentials" ? "Credentials generated" :
    isEditing ? `Edit domain — ${editTarget?.name}` :
    "Create domain";

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          description="Named TLS certificates for HTTPS ingress termination. Domains are matched by SNI hostname."
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
              <SpaceBetween direction="horizontal" size="xs">
                <Button disabled={selected.length === 0} onClick={handleDelete}>
                  Delete
                </Button>
                <Button
                  disabled={selected.length !== 1}
                  onClick={() => selected.length === 1 && openEdit(selected[0])}
                >
                  Edit
                </Button>
                <Button
                  disabled={selected.length === 0}
                  onClick={handleToggle}
                >
                  {selected.length === 1 && !selected[0].enabled ? "Enable" : "Disable"}
                </Button>
                <Button variant="primary" onClick={openCreate}>
                  Create domain
                </Button>
              </SpaceBetween>
            }
          >
            Domains
          </Header>
        }
        selectionType="multi"
        selectedItems={selected}
        onSelectionChange={(e) => setSelected(e.detail.selectedItems)}
        trackBy="id"
        columnDefinitions={[
          { id: "name", header: "Name", cell: (d) => d.name },
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
          { id: "cert", header: "Certificate", cell: (d) => certPreview(d.tls_cert) },
          { id: "age", header: "Age", cell: (d) => formatAge(d.created_at) },
          {
            id: "actions",
            header: "",
            cell: (d) => (
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="inline-link" onClick={() => openEdit(d)}>Edit</Button>
                <Button
                  variant="inline-link"
                  onClick={async () => {
                    try {
                      const resp = await api.toggleDomain(d.id);
                      addFlash("success", `${resp.name} ${resp.enabled ? "enabled" : "disabled"}`);
                      load();
                    } catch (e) {
                      addFlash("error", String(e));
                    }
                  }}
                >
                  {d.enabled ? "Disable" : "Enable"}
                </Button>
              </SpaceBetween>
            ),
          },
        ]}
        items={domains}
        empty="No domains"
      />

      <Modal
        visible={mode !== null}
        onDismiss={closeModal}
        header={modalTitle}
        footer={
          mode === "credentials" ? (
            <Button
              variant="primary"
              onClick={() => {
                addFlash("success", `Domain ${generated?.name} ${isEditing ? "updated" : "created"}`);
                closeModal();
                load();
              }}
            >
              Done
            </Button>
          ) : (
            <SpaceBetween direction="horizontal" size="xs">
              <Button variant="link" onClick={closeModal}>Cancel</Button>
              <Button variant="primary" onClick={isEditing ? handleUpdate : handleCreate}>
                {isEditing ? "Save" : "Create"}
              </Button>
            </SpaceBetween>
          )
        }
      >
        {mode === "credentials" ? (
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
          </SpaceBetween>
        ) : (
          <Form>
            <SpaceBetween size="m">
              <FormField
                label="Domain name"
                description="Hostname matched by SNI, e.g. api.example.com"
                constraintText={isEditing ? undefined : "Required — must be unique"}
              >
                <Input
                  value={form.name ?? ""}
                  onChange={(e) => setForm((f) => ({ ...f, name: e.detail.value }))}
                  placeholder="api.example.com"
                />
              </FormField>
              <FormField
                label="TLS Certificate (PEM)"
                description={
                  isEditing
                    ? "Leave both cert and key empty to regenerate a self-signed certificate"
                    : "Leave empty to auto-generate a self-signed certificate"
                }
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
                description={
                  isEditing
                    ? "Private key is never returned by the server. Leave empty to keep the existing key, or provide a new key together with a matching certificate."
                    : "Leave empty to auto-generate alongside the certificate"
                }
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
        )}
      </Modal>
    </ContentLayout>
  );
}
