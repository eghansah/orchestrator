import { useState } from "react";
import Alert from "@cloudscape-design/components/alert";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Checkbox from "@cloudscape-design/components/checkbox";
import Container from "@cloudscape-design/components/container";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import Modal from "@cloudscape-design/components/modal";
import SpaceBetween from "@cloudscape-design/components/space-between";
import StatusIndicator from "@cloudscape-design/components/status-indicator";
import Table from "@cloudscape-design/components/table";
import Textarea from "@cloudscape-design/components/textarea";
import { api, ClusterState, TrustedCA, formatAge } from "../api";

interface Props {
  state: ClusterState | null;
  loading: boolean;
  refetch: () => void;
}

const THIRTY_DAYS_SECONDS = 30 * 24 * 60 * 60;

function expiryStatus(ca: TrustedCA) {
  if (ca.expired) {
    return <StatusIndicator type="error">Expired</StatusIndicator>;
  }
  if (!ca.notAfter) {
    return <StatusIndicator type="info">Unknown</StatusIndicator>;
  }
  const date = new Date(ca.notAfter * 1000).toLocaleDateString();
  const nowSeconds = Date.now() / 1000;
  if (ca.notAfter - nowSeconds < THIRTY_DAYS_SECONDS) {
    return <StatusIndicator type="warning">Expiring soon — {date}</StatusIndicator>;
  }
  return <StatusIndicator type="success">{date}</StatusIndicator>;
}

function appliesToText(ca: TrustedCA) {
  const targets = [];
  if (ca.appliesToOpenBao) targets.push("OpenBao");
  if (ca.appliesToRegistries) targets.push("Registries");
  return targets.length > 0 ? targets.join(", ") : "—";
}

const emptyForm = { label: "", pem: "", appliesToOpenBao: false, appliesToRegistries: false };

export default function TrustedCAs({ state, loading, refetch }: Props) {
  const cas: TrustedCA[] = state?.trusted_cas ?? [];

  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [selected, setSelected] = useState<TrustedCA[]>([]);
  const [showCreate, setShowCreate] = useState(false);
  const [editTarget, setEditTarget] = useState<TrustedCA | null>(null);
  const [form, setForm] = useState(emptyForm);
  const [saving, setSaving] = useState(false);

  const hasExpired = cas.some((ca) => ca.expired);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  async function handleCreate() {
    if (!form.label || !form.pem) {
      addFlash("error", "Label and certificate are required");
      return;
    }
    setSaving(true);
    try {
      await api.createTrustedCA(form);
      addFlash("success", `Trusted CA "${form.label}" added`);
      setShowCreate(false);
      setForm(emptyForm);
      refetch();
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setSaving(false);
    }
  }

  function openEdit(ca: TrustedCA) {
    setEditTarget(ca);
    setForm({
      label: ca.label,
      pem: "",
      appliesToOpenBao: ca.appliesToOpenBao,
      appliesToRegistries: ca.appliesToRegistries,
    });
  }

  async function handleUpdate() {
    if (!editTarget) return;
    if (!form.label) {
      addFlash("error", "Label is required");
      return;
    }
    setSaving(true);
    try {
      await api.updateTrustedCA(editTarget.id, form);
      addFlash("success", `Trusted CA "${form.label}" updated`);
      setEditTarget(null);
      setForm(emptyForm);
      refetch();
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete() {
    for (const ca of selected) {
      try {
        await api.deleteTrustedCA(ca.id);
        addFlash("success", `Deleted "${ca.label}"`);
      } catch (e) {
        addFlash("error", String(e));
      }
    }
    setSelected([]);
    refetch();
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="CA certificates trusted cluster-wide. Each entry can apply to the OpenBao connection, container/compose registry image pulls, or both."
          actions={<Button iconName="refresh" onClick={refetch}>Refresh</Button>}
        >
          Trusted CAs
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Flashbar items={flash} />

        {hasExpired && (
          <Alert type="warning">
            One or more trusted CAs have expired and are excluded from active verification. Replace the certificate to restore trust.
          </Alert>
        )}

        <Container>
          <Table
            loading={loading}
            loadingText="Loading trusted CAs"
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
                    <Button variant="primary" onClick={() => setShowCreate(true)}>
                      Add trusted CA
                    </Button>
                  </SpaceBetween>
                }
              >
                Trusted CAs
              </Header>
            }
            selectionType="multi"
            selectedItems={selected}
            onSelectionChange={(e) => setSelected(e.detail.selectedItems)}
            trackBy="id"
            columnDefinitions={[
              { id: "label", header: "Label", cell: (ca) => ca.label },
              { id: "appliesTo", header: "Applies to", cell: (ca) => appliesToText(ca) },
              { id: "expiry", header: "Expiry", cell: (ca) => expiryStatus(ca) },
              { id: "age", header: "Age", cell: (ca) => formatAge(ca.createdAt) },
            ]}
            items={cas}
            empty={<Box color="text-body-secondary">No trusted CAs configured.</Box>}
          />
        </Container>

        {/* Add modal */}
        <Modal
          visible={showCreate}
          header="Add trusted CA"
          onDismiss={() => setShowCreate(false)}
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setShowCreate(false)}>Cancel</Button>
                <Button variant="primary" loading={saving} onClick={handleCreate}>Add</Button>
              </SpaceBetween>
            </Box>
          }
        >
          <SpaceBetween size="m">
            <FormField label="Label" description="Display name for this CA, e.g. Internal Corp CA">
              <Input
                value={form.label}
                onChange={(e) => setForm((f) => ({ ...f, label: e.detail.value }))}
                placeholder="Internal Corp CA"
              />
            </FormField>
            <FormField
              label="Certificate"
              description="A single CA certificate in PEM format. Add multiple entries for multiple CAs."
            >
              <Textarea
                value={form.pem}
                onChange={(e) => setForm((f) => ({ ...f, pem: e.detail.value }))}
                placeholder="-----BEGIN CERTIFICATE-----..."
                rows={8}
              />
            </FormField>
            <Checkbox
              checked={form.appliesToOpenBao}
              onChange={(e) => setForm((f) => ({ ...f, appliesToOpenBao: e.detail.checked }))}
            >
              Apply to OpenBao connection
            </Checkbox>
            <Checkbox
              checked={form.appliesToRegistries}
              onChange={(e) => setForm((f) => ({ ...f, appliesToRegistries: e.detail.checked }))}
            >
              Apply to registry image pulls
            </Checkbox>
          </SpaceBetween>
        </Modal>

        {/* Edit modal */}
        <Modal
          visible={editTarget !== null}
          header={`Edit trusted CA — ${editTarget?.label ?? ""}`}
          onDismiss={() => { setEditTarget(null); setForm(emptyForm); }}
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => { setEditTarget(null); setForm(emptyForm); }}>
                  Cancel
                </Button>
                <Button variant="primary" loading={saving} onClick={handleUpdate}>Save</Button>
              </SpaceBetween>
            </Box>
          }
        >
          <SpaceBetween size="m">
            <FormField label="Label">
              <Input
                value={form.label}
                onChange={(e) => setForm((f) => ({ ...f, label: e.detail.value }))}
              />
            </FormField>
            <FormField label="Certificate" description="Leave blank to keep the existing certificate">
              <Textarea
                value={form.pem}
                onChange={(e) => setForm((f) => ({ ...f, pem: e.detail.value }))}
                placeholder="unchanged"
                rows={8}
              />
            </FormField>
            <Checkbox
              checked={form.appliesToOpenBao}
              onChange={(e) => setForm((f) => ({ ...f, appliesToOpenBao: e.detail.checked }))}
            >
              Apply to OpenBao connection
            </Checkbox>
            <Checkbox
              checked={form.appliesToRegistries}
              onChange={(e) => setForm((f) => ({ ...f, appliesToRegistries: e.detail.checked }))}
            >
              Apply to registry image pulls
            </Checkbox>
          </SpaceBetween>
        </Modal>
      </SpaceBetween>
    </ContentLayout>
  );
}
