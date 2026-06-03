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
import { api, ClusterState, Secret, formatAge } from "../api";

interface Props {
  state: ClusterState | null;
  loading: boolean;
  refetch: () => void;
}

export default function Secrets({ state, loading, refetch }: Props) {
  const secrets: Secret[] = state?.secrets ?? [];
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [selected, setSelected] = useState<Secret[]>([]);
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ name: "", value: "" });
  const [saving, setSaving] = useState(false);

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
      await api.createSecret({ name: form.name, value: form.value });
      addFlash("success", `Secret "${form.name}" created`);
      setShowCreate(false);
      setForm({ name: "", value: "" });
      refetch();
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete() {
    for (const s of selected) {
      try {
        await api.deleteSecret(s.id);
        addFlash("success", `Deleted "${s.name}"`);
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
          Secrets
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Flashbar items={flash} />

        <Container>
          <Table
            loading={loading}
            loadingText="Loading secrets"
            header={
              <Header
                description="Secrets are encrypted with AES-256-GCM before storage. Values are never shown after creation."
                actions={
                  <SpaceBetween direction="horizontal" size="xs">
                    <Button disabled={selected.length === 0} onClick={handleDelete}>
                      Delete
                    </Button>
                    <Button variant="primary" onClick={() => setShowCreate(true)}>
                      Create secret
                    </Button>
                  </SpaceBetween>
                }
              >
                Secrets
              </Header>
            }
            selectionType="multi"
            selectedItems={selected}
            onSelectionChange={(e) => setSelected(e.detail.selectedItems)}
            trackBy="id"
            columnDefinitions={[
              { id: "name", header: "Name", cell: (s) => s.name },
              { id: "id",   header: "ID",   cell: (s) => s.id },
              { id: "age",  header: "Age",  cell: (s) => formatAge(s.created_at) },
            ]}
            items={secrets}
            empty={<Box color="text-body-secondary">No secrets stored. Use "Create secret" to add one.</Box>}
          />
        </Container>

        <Modal
          visible={showCreate}
          header="Create secret"
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
              description="Unique label used to reference this secret in workloads (e.g. db-password)"
            >
              <Input
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.detail.value }))}
                placeholder="db-password"
              />
            </FormField>
            <FormField
              label="Value"
              description="Plaintext value — encrypted before storage. Not retrievable after creation."
            >
              <Textarea
                value={form.value}
                onChange={(e) => setForm((f) => ({ ...f, value: e.detail.value }))}
                placeholder="super-secret-value"
                rows={4}
              />
            </FormField>
          </SpaceBetween>
        </Modal>
      </SpaceBetween>
    </ContentLayout>
  );
}
