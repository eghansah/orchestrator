import { useEffect, useState } from "react";
import Alert from "@cloudscape-design/components/alert";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import Modal from "@cloudscape-design/components/modal";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Table from "@cloudscape-design/components/table";
import Textarea from "@cloudscape-design/components/textarea";
import { ClusterState, CreateTemplateRequest, WorkloadTemplate, api, formatAge } from "../api";

interface Props {
  state: ClusterState | null;
  loading: boolean;
  error: string | null;
  refetch: () => void;
  onNavigate: (page: string) => void;
}

const KIND_OPTIONS = [
  { value: "stack", label: "Compose stack" },
  { value: "container", label: "Container" },
];

const emptyForm = (): CreateTemplateRequest => ({ name: "", description: "", kind: "stack", compose_yaml: "", image: "" });

// Phases that mean a deployed workload is still live; in any of these the
// template's Deploy button becomes Redeploy (which replaces the workload).
const ACTIVE_PHASES = new Set(["pending", "scheduled", "running"]);

export default function Templates({ state, loading, error, refetch, onNavigate: _nav }: Props) {
  // A template's deployed workload carries the template's name. Map name →
  // whether a live workload exists, so each row can pick Deploy vs Redeploy.
  const deployedNames = new Set(
    (state?.workloads ?? [])
      .filter((wl) => ACTIVE_PHASES.has(wl.phase))
      .map((wl) => wl.name),
  );
  const [templates, setTemplates] = useState<WorkloadTemplate[]>([]);
  const [listLoading, setListLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);

  const [showNew, setShowNew] = useState(false);
  const [editTarget, setEditTarget] = useState<WorkloadTemplate | null>(null);
  const [form, setForm] = useState<CreateTemplateRequest>(emptyForm());
  const [saving, setSaving] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<WorkloadTemplate | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deploying, setDeploying] = useState<string | null>(null);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [...f, { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) }]);
  }

  async function loadTemplates() {
    setListLoading(true);
    try {
      setTemplates(await api.listTemplates());
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setListLoading(false);
    }
  }

  useEffect(() => { loadTemplates(); }, []);

  function openNew() {
    setForm(emptyForm());
    setEditTarget(null);
    setShowNew(true);
  }

  function openEdit(t: WorkloadTemplate) {
    setForm({
      name: t.name,
      description: t.description,
      kind: t.kind,
      compose_yaml: t.compose_yaml ?? "",
      image: t.image ?? "",
    });
    setEditTarget(t);
    setShowNew(true);
  }

  async function handleSave() {
    if (!form.name) { addFlash("error", "Name is required"); return; }
    setSaving(true);
    try {
      if (editTarget) {
        const updated = await api.updateTemplate(editTarget.id, form);
        setTemplates((ts) => ts.map((t) => (t.id === editTarget.id ? updated : t)));
        addFlash("success", `Template "${updated.name}" updated`);
      } else {
        const created = await api.createTemplate(form);
        setTemplates((ts) => [...ts, created]);
        addFlash("success", `Template "${created.name}" created`);
      }
      setShowNew(false);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.deleteTemplate(deleteTarget.id);
      setTemplates((ts) => ts.filter((t) => t.id !== deleteTarget.id));
      addFlash("success", `Template "${deleteTarget.name}" deleted`);
      setDeleteTarget(null);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setDeleting(false);
    }
  }

  async function handleDeploy(t: WorkloadTemplate) {
    const redeploy = deployedNames.has(t.name);
    setDeploying(t.id);
    try {
      const res = await api.deployTemplate(t.id);
      if (!res.accepted) {
        addFlash("error", `${redeploy ? "Redeploy" : "Deploy"} rejected: ${res.reason ?? "unknown"}`);
      } else {
        addFlash("success", `${redeploy ? "Redeployed" : "Deployed"} as workload ${res.workload_id}`);
        refetch();
      }
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setDeploying(null);
    }
  }

  const isStack = form.kind === "stack";

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button iconName="refresh" onClick={() => { refetch(); loadTemplates(); }}>Refresh</Button>
              <Button variant="primary" iconName="add-plus" onClick={openNew}>New template</Button>
            </SpaceBetween>
          }
        >
          Templates
        </Header>
      }
    >
      <SpaceBetween size="m">
        {error && <Alert type="error">{error}</Alert>}
        <Flashbar items={flash} />

        <Table
          loading={listLoading || loading}
          loadingText="Loading templates…"
          empty={<Box textAlign="center" color="inherit">No templates saved yet</Box>}
          columnDefinitions={[
            { id: "name",        header: "Name",        cell: (t) => t.name,        sortingField: "name" },
            { id: "kind",        header: "Kind",        cell: (t) => t.kind },
            { id: "description", header: "Description", cell: (t) => t.description || "—" },
            { id: "age",         header: "Age",         cell: (t) => formatAge(t.created_at) },
            {
              id: "actions",
              header: "",
              cell: (t) => (
                <SpaceBetween direction="horizontal" size="xs">
                  <Button
                    variant="inline-link"
                    loading={deploying === t.id}
                    onClick={() => handleDeploy(t)}
                  >
                    {deployedNames.has(t.name) ? "Redeploy" : "Deploy"}
                  </Button>
                  <Button variant="inline-link" onClick={() => openEdit(t)}>Edit</Button>
                  <Button variant="inline-link" onClick={() => setDeleteTarget(t)}>Delete</Button>
                </SpaceBetween>
              ),
            },
          ]}
          items={templates}
          header={<Header counter={`(${templates.length})`}>Saved templates</Header>}
        />
      </SpaceBetween>

      {/* ── New / Edit modal ─────────────────────────────────────────────── */}
      <Modal
        visible={showNew}
        onDismiss={() => setShowNew(false)}
        header={editTarget ? `Edit template — ${editTarget.name}` : "New template"}
        size="large"
        footer={
          <Box float="right">
            <SpaceBetween direction="horizontal" size="xs">
              <Button variant="link" onClick={() => setShowNew(false)}>Cancel</Button>
              <Button variant="primary" loading={saving} onClick={handleSave}>
                {editTarget ? "Save changes" : "Create template"}
              </Button>
            </SpaceBetween>
          </Box>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name">
            <Input value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.detail.value }))} />
          </FormField>
          <FormField label="Description" constraintText="Optional">
            <Input value={form.description ?? ""} onChange={(e) => setForm((f) => ({ ...f, description: e.detail.value }))} />
          </FormField>
          <FormField label="Kind">
            <Select
              options={KIND_OPTIONS}
              selectedOption={KIND_OPTIONS.find((o) => o.value === form.kind) ?? KIND_OPTIONS[0]}
              onChange={(e) => setForm((f) => ({ ...f, kind: (e.detail.selectedOption.value ?? "stack") as "stack" | "container" }))}
              disabled={!!editTarget}
            />
          </FormField>
          {isStack ? (
            <FormField label="Compose YAML">
              <Textarea
                rows={16}
                value={form.compose_yaml ?? ""}
                onChange={(e) => setForm((f) => ({ ...f, compose_yaml: e.detail.value }))}
              />
            </FormField>
          ) : (
            <FormField label="Image">
              <Input value={form.image ?? ""} onChange={(e) => setForm((f) => ({ ...f, image: e.detail.value }))} placeholder="nginx:latest" />
            </FormField>
          )}
        </SpaceBetween>
      </Modal>

      {/* ── Delete confirmation ──────────────────────────────────────────── */}
      <Modal
        visible={!!deleteTarget}
        onDismiss={() => setDeleteTarget(null)}
        header="Delete template"
        footer={
          <Box float="right">
            <SpaceBetween direction="horizontal" size="xs">
              <Button variant="link" onClick={() => setDeleteTarget(null)}>Cancel</Button>
              <Button variant="primary" loading={deleting} onClick={handleDelete}>Delete</Button>
            </SpaceBetween>
          </Box>
        }
      >
        {deleteTarget && (
          <Box>Delete template <strong>{deleteTarget.name}</strong>? This cannot be undone.</Box>
        )}
      </Modal>
    </ContentLayout>
  );
}
