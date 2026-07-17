import { useState } from "react";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Container from "@cloudscape-design/components/container";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import Modal from "@cloudscape-design/components/modal";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Table from "@cloudscape-design/components/table";
import Textarea from "@cloudscape-design/components/textarea";
import { api, ClusterState, ConfigValue, formatAge } from "../api";

interface Props {
  state: ClusterState | null;
  loading: boolean;
  refetch: () => void;
}

export default function Config({ state, loading, refetch }: Props) {
  const configValues: ConfigValue[] = state?.config_values ?? [];

  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [selected, setSelected] = useState<ConfigValue[]>([]);

  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ name: "", value: "" });
  const [saving, setSaving] = useState(false);

  const [showEdit, setShowEdit] = useState(false);
  const [editValue, setEditValue] = useState("");
  const [editSaving, setEditSaving] = useState(false);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  async function handleCreate() {
    if (!form.name || !form.value) {
      addFlash("error", "Name and value are required");
      return;
    }
    setSaving(true);
    try {
      await api.createConfigValue({ name: form.name, value: form.value });
      addFlash("success", `Config value "${form.name}" created`);
      setShowCreate(false);
      setForm({ name: "", value: "" });
      refetch();
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setSaving(false);
    }
  }

  async function handleUpdate() {
    if (selected.length !== 1 || !editValue) {
      addFlash("error", "A new value is required");
      return;
    }
    const target = selected[0];
    setEditSaving(true);
    try {
      await api.updateConfigValue(target.id, { value: editValue });
      addFlash("success", `Updated "${target.name}"`);
      setShowEdit(false);
      setEditValue("");
      setSelected([]);
      refetch();
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setEditSaving(false);
    }
  }

  async function handleDelete() {
    for (const cv of selected) {
      try {
        await api.deleteConfigValue(cv.id);
        addFlash("success", `Deleted "${cv.name}"`);
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
        <Header variant="h1" actions={<Button iconName="refresh" onClick={refetch}>Refresh</Button>}>
          Config
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Flashbar items={flash} />

        <Container>
          <Table
            loading={loading}
            loadingText="Loading config values"
            header={
              <Header
                description="Plaintext values shared across workloads, stored directly in cluster state. Reference them with --config ENV_VAR=name (ctl run/stack). For anything sensitive, use Secrets instead."
                actions={
                  <SpaceBetween direction="horizontal" size="xs">
                    <Button disabled={selected.length === 0} onClick={handleDelete}>
                      Delete
                    </Button>
                    <Button
                      disabled={selected.length !== 1}
                      onClick={() => {
                        setEditValue(selected[0]?.value ?? "");
                        setShowEdit(true);
                      }}
                    >
                      Edit value
                    </Button>
                    <Button variant="primary" onClick={() => setShowCreate(true)}>
                      Create config value
                    </Button>
                  </SpaceBetween>
                }
              >
                Config
              </Header>
            }
            selectionType="multi"
            selectedItems={selected}
            onSelectionChange={(e) => setSelected(e.detail.selectedItems)}
            trackBy="id"
            columnDefinitions={[
              { id: "name", header: "Name", cell: (cv) => cv.name },
              { id: "value", header: "Value", cell: (cv) => cv.value },
              { id: "age", header: "Age", cell: (cv) => formatAge(cv.created_at) },
            ]}
            items={configValues}
            empty={
              <Box textAlign="center" color="inherit">
                <b>No config values</b>
                <Box variant="p" color="inherit">
                  Create a shared value like a database host or port.
                </Box>
              </Box>
            }
          />
        </Container>

        {/* Create config value modal */}
        <Modal
          visible={showCreate}
          header="Create config value"
          onDismiss={() => { setShowCreate(false); setForm({ name: "", value: "" }); }}
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => { setShowCreate(false); setForm({ name: "", value: "" }); }}>
                  Cancel
                </Button>
                <Button variant="primary" loading={saving} onClick={handleCreate}>
                  Create
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <SpaceBetween size="m">
            <FormField
              label="Name"
              description="Unique label used to reference this value in workloads (e.g. db-ip)."
            >
              <Input
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.detail.value }))}
                placeholder="db-ip"
              />
            </FormField>
            <FormField label="Value" description="Plaintext value, visible to anyone with cluster access.">
              <Textarea
                value={form.value}
                onChange={(e) => setForm((f) => ({ ...f, value: e.detail.value }))}
                placeholder="10.0.0.5"
                rows={4}
              />
            </FormField>
          </SpaceBetween>
        </Modal>

        {/* Edit config value modal */}
        <Modal
          visible={showEdit}
          header={`Edit value${selected.length === 1 ? ` — ${selected[0].name}` : ""}`}
          onDismiss={() => { setShowEdit(false); setEditValue(""); }}
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => { setShowEdit(false); setEditValue(""); }}>
                  Cancel
                </Button>
                <Button variant="primary" loading={editSaving} onClick={handleUpdate}>
                  Save
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <SpaceBetween size="m">
            <FormField label="Value" description="Overwrites the current value in place; workloads pick it up on next placement.">
              <Textarea
                value={editValue}
                onChange={(e) => setEditValue(e.detail.value)}
                placeholder="new-value"
                rows={4}
              />
            </FormField>
          </SpaceBetween>
        </Modal>
      </SpaceBetween>
    </ContentLayout>
  );
}
