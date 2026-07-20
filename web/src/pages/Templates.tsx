import { ChangeEvent, useEffect, useRef, useState } from "react";
import Alert from "@cloudscape-design/components/alert";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Checkbox from "@cloudscape-design/components/checkbox";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import Link from "@cloudscape-design/components/link";
import Modal from "@cloudscape-design/components/modal";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Table from "@cloudscape-design/components/table";
import Textarea from "@cloudscape-design/components/textarea";
import { ClusterState, CreateTemplateRequest, Volume, WorkloadTemplate, api, formatAge } from "../api";
import {
  MountEntry,
  RefEntry,
  SecretVolumeEntry,
  entriesToMounts,
  entriesToRefs,
  entriesToSecretVolumes,
  mountsToEntries,
  refsToEntries,
  volumesToSecretEntries,
} from "../workloadRefs";

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

const emptyForm = (): CreateTemplateRequest => ({ name: "", description: "", kind: "stack", compose_yaml: "", image: "", insecure_registry: false });


// Phases that mean a deployed workload is still live; in any of these the
// template's Deploy button becomes Redeploy (which replaces the workload).
const ACTIVE_PHASES = new Set(["pending", "scheduled", "running"]);

export default function Templates({ state, loading, error, refetch, onNavigate }: Props) {
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
  const [secretRefRows, setSecretRefRows] = useState<RefEntry[]>([]);
  const [configRefRows, setConfigRefRows] = useState<RefEntry[]>([]);
  const [secretMountRows, setSecretMountRows] = useState<MountEntry[]>([]);
  const [containerSecretVolumeRows, setContainerSecretVolumeRows] = useState<SecretVolumeEntry[]>([]);
  const [otherVolumes, setOtherVolumes] = useState<Volume[]>([]); // non-secret volumes preserved from the loaded template
  const [saving, setSaving] = useState(false);

  const [deleteTarget, setDeleteTarget] = useState<WorkloadTemplate | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deploying, setDeploying] = useState<string | null>(null);
  const [exporting, setExporting] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);
  const importFileInputRef = useRef<HTMLInputElement>(null);

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
    setSecretRefRows([]);
    setConfigRefRows([]);
    setSecretMountRows([]);
    setContainerSecretVolumeRows([]);
    setOtherVolumes([]);
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
      insecure_registry: t.insecure_registry ?? false,
    });
    setSecretRefRows(refsToEntries(t.secret_refs));
    setConfigRefRows(refsToEntries(t.config_refs));
    setSecretMountRows(mountsToEntries(t.secret_mounts));
    setContainerSecretVolumeRows(volumesToSecretEntries(t.volumes));
    setOtherVolumes((t.volumes ?? []).filter((v) => v.type !== "secret"));
    setEditTarget(t);
    setShowNew(true);
  }

  async function handleSave() {
    if (!form.name) { addFlash("error", "Name is required"); return; }
    const payload: CreateTemplateRequest = {
      ...form,
      secret_refs: entriesToRefs(secretRefRows),
      config_refs: entriesToRefs(configRefRows),
      secret_mounts: isStack ? entriesToMounts(secretMountRows) : undefined,
      volumes: isStack ? undefined : entriesToSecretVolumes(containerSecretVolumeRows, otherVolumes),
    };
    setSaving(true);
    try {
      if (editTarget) {
        const updated = await api.updateTemplate(editTarget.id, payload);
        setTemplates((ts) => ts.map((t) => (t.id === editTarget.id ? updated : t)));
        addFlash("success", `Template "${updated.name}" updated`);
      } else {
        const created = await api.createTemplate(payload);
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

  async function handleExportTemplate(t: WorkloadTemplate) {
    setExporting(t.id);
    try {
      const blob = await api.exportTemplate(t.id);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `template-${t.name}.yaml`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setExporting(null);
    }
  }

  async function handleImportFile(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    e.target.value = ""; // allow re-selecting the same file next time
    if (!file) return;
    setImporting(true);
    try {
      const text = await file.text();
      const created = await api.importTemplate(text, false);
      setTemplates((ts) => [...ts, created]);
      addFlash("success", `Template "${created.name}" imported`);
    } catch (err) {
      addFlash("error", String(err));
    } finally {
      setImporting(false);
    }
  }

  const isStack = form.kind === "stack";

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={
            <>
              Reusable container/stack definitions that can reference Secrets and Config values.{" "}
              <Link
                onFollow={(e) => {
                  e.preventDefault();
                  onNavigate("docs-templates-secrets-config");
                }}
              >
                How to use Secrets and Config in templates
              </Link>
            </>
          }
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              <Button iconName="refresh" onClick={() => { refetch(); loadTemplates(); }}>Refresh</Button>
              <Button iconName="upload" loading={importing} onClick={() => importFileInputRef.current?.click()}>
                Import from file
              </Button>
              <input
                ref={importFileInputRef}
                type="file"
                accept=".yaml,.yml"
                style={{ display: "none" }}
                onChange={handleImportFile}
              />
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
                  <Button variant="inline-link" loading={exporting === t.id} onClick={() => handleExportTemplate(t)}>Export</Button>
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
          <FormField
            label="Secret refs"
            description="Inject a secret as an env var: ENV_VAR → secret name (resolved from OpenBao at deploy time)"
          >
            <SpaceBetween size="xs">
              {secretRefRows.map((row, i) => (
                <SpaceBetween key={i} direction="horizontal" size="xs">
                  <Input
                    value={row.envVar}
                    onChange={(e) => setSecretRefRows((rows) => rows.map((r, j) => (j === i ? { ...r, envVar: e.detail.value } : r)))}
                    placeholder="ENV_VAR"
                  />
                  <Input
                    value={row.name}
                    onChange={(e) => setSecretRefRows((rows) => rows.map((r, j) => (j === i ? { ...r, name: e.detail.value } : r)))}
                    placeholder="secret_name"
                  />
                  <Button iconName="close" variant="icon" onClick={() => setSecretRefRows((rows) => rows.filter((_, j) => j !== i))} />
                </SpaceBetween>
              ))}
              <Button iconName="add-plus" onClick={() => setSecretRefRows((rows) => [...rows, { envVar: "", name: "" }])}>
                Add secret ref
              </Button>
            </SpaceBetween>
          </FormField>
          <FormField
            label="Config refs"
            description="Inject a shared config value as an env var: ENV_VAR → config value name (plaintext, resolved at deploy time)"
          >
            <SpaceBetween size="xs">
              {configRefRows.map((row, i) => (
                <SpaceBetween key={i} direction="horizontal" size="xs">
                  <Input
                    value={row.envVar}
                    onChange={(e) => setConfigRefRows((rows) => rows.map((r, j) => (j === i ? { ...r, envVar: e.detail.value } : r)))}
                    placeholder="ENV_VAR"
                  />
                  <Input
                    value={row.name}
                    onChange={(e) => setConfigRefRows((rows) => rows.map((r, j) => (j === i ? { ...r, name: e.detail.value } : r)))}
                    placeholder="config_value_name"
                  />
                  <Button iconName="close" variant="icon" onClick={() => setConfigRefRows((rows) => rows.filter((_, j) => j !== i))} />
                </SpaceBetween>
              ))}
              <Button iconName="add-plus" onClick={() => setConfigRefRows((rows) => [...rows, { envVar: "", name: "" }])}>
                Add config ref
              </Button>
            </SpaceBetween>
          </FormField>
          {isStack ? (
            <FormField
              label="Secret mounts"
              description="Mount a secret as a file: service → secret name → target path (default /run/secrets/<secret name>), resolved onto a tmpfs-backed staging dir at deploy time"
            >
              <SpaceBetween size="xs">
                {secretMountRows.map((row, i) => (
                  <SpaceBetween key={i} direction="horizontal" size="xs">
                    <Input
                      value={row.service}
                      onChange={(e) => setSecretMountRows((rows) => rows.map((r, j) => (j === i ? { ...r, service: e.detail.value } : r)))}
                      placeholder="service"
                    />
                    <Input
                      value={row.secretName}
                      onChange={(e) => setSecretMountRows((rows) => rows.map((r, j) => (j === i ? { ...r, secretName: e.detail.value } : r)))}
                      placeholder="secret_name"
                    />
                    <Input
                      value={row.target}
                      onChange={(e) => setSecretMountRows((rows) => rows.map((r, j) => (j === i ? { ...r, target: e.detail.value } : r)))}
                      placeholder="/run/secrets/secret_name"
                    />
                    <Input
                      value={row.mode}
                      onChange={(e) => setSecretMountRows((rows) => rows.map((r, j) => (j === i ? { ...r, mode: e.detail.value } : r)))}
                      placeholder="0400"
                    />
                    <Button iconName="close" variant="icon" onClick={() => setSecretMountRows((rows) => rows.filter((_, j) => j !== i))} />
                  </SpaceBetween>
                ))}
                <Button iconName="add-plus" onClick={() => setSecretMountRows((rows) => [...rows, { service: "", secretName: "", target: "", mode: "" }])}>
                  Add secret mount
                </Button>
              </SpaceBetween>
            </FormField>
          ) : (
            <FormField
              label="Secret file mounts"
              description="Mount a secret as a file at /run/secrets/<secret name> (or a custom target) inside the container, resolved onto a tmpfs-backed staging dir at deploy time. Mode defaults to 0400."
            >
              <SpaceBetween size="xs">
                {containerSecretVolumeRows.map((row, i) => (
                  <SpaceBetween key={i} direction="horizontal" size="xs">
                    <Input
                      value={row.secretName}
                      onChange={(e) => setContainerSecretVolumeRows((rows) => rows.map((r, j) => (j === i ? { ...r, secretName: e.detail.value } : r)))}
                      placeholder="secret_name"
                    />
                    <Input
                      value={row.target}
                      onChange={(e) => setContainerSecretVolumeRows((rows) => rows.map((r, j) => (j === i ? { ...r, target: e.detail.value } : r)))}
                      placeholder="/run/secrets/secret_name"
                    />
                    <Input
                      value={row.mode}
                      onChange={(e) => setContainerSecretVolumeRows((rows) => rows.map((r, j) => (j === i ? { ...r, mode: e.detail.value } : r)))}
                      placeholder="0400"
                    />
                    <Button iconName="close" variant="icon" onClick={() => setContainerSecretVolumeRows((rows) => rows.filter((_, j) => j !== i))} />
                  </SpaceBetween>
                ))}
                <Button iconName="add-plus" onClick={() => setContainerSecretVolumeRows((rows) => [...rows, { secretName: "", target: "", mode: "" }])}>
                  Add secret file mount
                </Button>
              </SpaceBetween>
            </FormField>
          )}
          <Checkbox
            checked={form.insecure_registry ?? false}
            onChange={(e) => setForm((f) => ({ ...f, insecure_registry: e.detail.checked }))}
          >
            Insecure registry
            <Box variant="small" color="text-body-secondary">
              Allow pulling from a plain-HTTP or self-signed registry (passes --insecure-registry to nerdctl)
            </Box>
          </Checkbox>
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
