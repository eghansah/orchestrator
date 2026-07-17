import { useEffect, useState } from "react";
import Alert from "@cloudscape-design/components/alert";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Checkbox from "@cloudscape-design/components/checkbox";
import ColumnLayout from "@cloudscape-design/components/column-layout";
import Container from "@cloudscape-design/components/container";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import Link from "@cloudscape-design/components/link";
import Modal from "@cloudscape-design/components/modal";
import SpaceBetween from "@cloudscape-design/components/space-between";
import StatusIndicator from "@cloudscape-design/components/status-indicator";
import Table from "@cloudscape-design/components/table";
import Textarea from "@cloudscape-design/components/textarea";
import { api, BaoSealStatus, ClusterState, OpenBaoStatus, Secret, formatAge } from "../api";
import "./secrets.css";

interface Props {
  state: ClusterState | null;
  loading: boolean;
  refetch: () => void;
  onNavigate: (page: string) => void;
}

export default function Secrets({ state, loading, refetch, onNavigate }: Props) {
  const secrets: Secret[] = state?.secrets ?? [];

  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [selected, setSelected] = useState<Secret[]>([]);
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ name: "", value: "" });
  const [showCreateValue, setShowCreateValue] = useState(false);
  const [saving, setSaving] = useState(false);

  const [showEdit, setShowEdit] = useState(false);
  const [editValue, setEditValue] = useState("");
  const [showEditValue, setShowEditValue] = useState(false);
  const [editSaving, setEditSaving] = useState(false);

  // OpenBao connection state
  const [baoStatus, setBaoStatus] = useState<OpenBaoStatus | null>(null);
  const [baoLoading, setBaoLoading] = useState(true);
  const [showBaoConfig, setShowBaoConfig] = useState(false);
  const [baoForm, setBaoForm] = useState({
    address: "",
    token: "",
    mount: "secret",
    insecureSkipVerify: false,
    roleId: "",
    secretId: "",
    authMount: "approle",
  });
  const [baoSaving, setBaoSaving] = useState(false);

  // OpenBao seal status + unseal
  const [sealStatus, setSealStatus] = useState<BaoSealStatus | null>(null);
  const [sealLoading, setSealLoading] = useState(true);
  const [unsealKey, setUnsealKey] = useState("");
  const [unsealing, setUnsealing] = useState(false);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [
      ...f,
      { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) },
    ]);
  }

  function loadBaoStatus() {
    setBaoLoading(true);
    api
      .getOpenBaoStatus()
      .then(setBaoStatus)
      .catch(() => setBaoStatus(null))
      .finally(() => setBaoLoading(false));
  }

  function loadSealStatus() {
    setSealLoading(true);
    api
      .getBaoSealStatus()
      .then(setSealStatus)
      .catch(() => setSealStatus(null))
      .finally(() => setSealLoading(false));
  }

  useEffect(() => {
    loadBaoStatus();
    loadSealStatus();
  }, []);

  async function handleUnseal() {
    if (!unsealKey) {
      addFlash("error", "Key is required");
      return;
    }
    setUnsealing(true);
    try {
      const st = await api.unsealBao(unsealKey);
      setSealStatus(st);
      setUnsealKey("");
      if (st.sealed) {
        addFlash("success", `Key accepted — progress ${st.progress}/${st.threshold}`);
      } else {
        addFlash("success", "OpenBao unsealed");
        loadBaoStatus();
      }
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setUnsealing(false);
    }
  }

  async function handleSaveBaoConfig() {
    if (!baoForm.address) {
      addFlash("error", "Address is required");
      return;
    }
    setBaoSaving(true);
    try {
      await api.setOpenBaoConfig({
        address: baoForm.address,
        token: baoForm.token,
        mount: baoForm.mount || "secret",
        insecureSkipVerify: baoForm.insecureSkipVerify,
        roleId: baoForm.roleId,
        secretId: baoForm.secretId,
        authMount: baoForm.authMount || "approle",
      });
      addFlash("success", "OpenBao connection saved");
      setShowBaoConfig(false);
      loadBaoStatus();
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setBaoSaving(false);
    }
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

  async function handleUpdate() {
    if (selected.length !== 1 || !editValue) {
      addFlash("error", "A new value is required");
      return;
    }
    const target = selected[0];
    setEditSaving(true);
    try {
      await api.updateSecret(target.id, { value: editValue });
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

  const baoConnected = baoStatus?.connected === true;

  function baoStatusIndicator() {
    if (baoLoading) return <StatusIndicator type="loading">Checking…</StatusIndicator>;
    if (!baoStatus?.configured) return <StatusIndicator type="stopped">Not configured</StatusIndicator>;
    if (baoConnected) return <StatusIndicator type="success">Connected — {baoStatus.address}</StatusIndicator>;
    return <StatusIndicator type="error">Unreachable — {baoStatus.error}</StatusIndicator>;
  }

  return (
    <ContentLayout
      header={
        <Header variant="h1" actions={<Button iconName="refresh" onClick={() => { refetch(); loadBaoStatus(); loadSealStatus(); }}>Refresh</Button>}>
          Secrets
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Flashbar items={flash} />

        {/* OpenBao connection card */}
        <Container
          header={
            <Header
              variant="h2"
              description={
                <>
                  Secrets are stored in OpenBao (Vault-compatible KV v2). Values are never written to cluster state.{" "}
                  <Link
                    onFollow={(e) => {
                      e.preventDefault();
                      onNavigate("docs-openbao-setup");
                    }}
                  >
                    Set up OpenBao for this cluster
                  </Link>
                </>
              }
              actions={
                <Button onClick={() => {
                  setBaoForm({
                    address: baoStatus?.address ?? "",
                    token: "",
                    mount: baoStatus?.mount ?? "secret",
                    insecureSkipVerify: baoStatus?.insecureSkipVerify ?? false,
                    roleId: baoStatus?.roleId ?? "",
                    secretId: "",
                    authMount: baoStatus?.authMount ?? "approle",
                  });
                  setShowBaoConfig(true);
                }}>
                  {baoStatus?.configured ? "Update connection" : "Configure connection"}
                </Button>
              }
            >
              OpenBao connection
            </Header>
          }
        >
          <ColumnLayout columns={2} variant="text-grid">
            <div>
              <Box variant="awsui-key-label">Status</Box>
              <div>{baoStatusIndicator()}</div>
            </div>
            <div>
              <Box variant="awsui-key-label">Mount</Box>
              <Box>{baoStatus?.mount ?? "—"}</Box>
            </div>
          </ColumnLayout>
          {baoStatus?.configured && !baoConnected && (
            <Box margin={{ top: "s" }}>
              <Alert type="warning">
                OpenBao is unreachable. Secret creation is disabled until the connection is restored.
              </Alert>
            </Box>
          )}
        </Container>

        {/* OpenBao unseal card */}
        {sealStatus?.configured && (
          <Container
            header={
              <Header
                variant="h2"
                description="Submit Shamir unseal key shares one at a time. OpenBao must be unsealed before secrets can be read or written."
              >
                Unseal OpenBao
              </Header>
            }
          >
            <SpaceBetween size="m">
              <ColumnLayout columns={2} variant="text-grid">
                <div>
                  <Box variant="awsui-key-label">Seal status</Box>
                  <div>
                    {sealLoading ? (
                      <StatusIndicator type="loading">Checking…</StatusIndicator>
                    ) : !sealStatus.reachable ? (
                      <StatusIndicator type="error">Unreachable — {sealStatus.error}</StatusIndicator>
                    ) : sealStatus.sealed ? (
                      <StatusIndicator type="warning">Sealed</StatusIndicator>
                    ) : (
                      <StatusIndicator type="success">Unsealed</StatusIndicator>
                    )}
                  </div>
                </div>
                <div>
                  <Box variant="awsui-key-label">Progress</Box>
                  <Box>
                    {sealStatus.reachable && sealStatus.threshold > 0
                      ? `${sealStatus.progress}/${sealStatus.threshold} key shares (of ${sealStatus.shares} total)`
                      : "—"}
                  </Box>
                </div>
              </ColumnLayout>
              {sealStatus.reachable && sealStatus.sealed && (
                <FormField label="Unseal key share" description="Submit one key share per call until the threshold is met.">
                  <SpaceBetween direction="horizontal" size="xs">
                    <Input
                      type="password"
                      value={unsealKey}
                      onChange={(e) => setUnsealKey(e.detail.value)}
                      placeholder="unseal key share"
                    />
                    <Button variant="primary" loading={unsealing} onClick={handleUnseal}>
                      Submit key
                    </Button>
                  </SpaceBetween>
                </FormField>
              )}
            </SpaceBetween>
          </Container>
        )}

        {/* Secrets table */}
        <Container>
          <Table
            loading={loading}
            loadingText="Loading secrets"
            header={
              <Header
                description="Secret values live in OpenBao and are injected at placement time. They are never returned after creation."
                actions={
                  <SpaceBetween direction="horizontal" size="xs">
                    <Button disabled={selected.length === 0} onClick={handleDelete}>
                      Delete
                    </Button>
                    <Button
                      disabled={selected.length !== 1 || !baoConnected}
                      onClick={() => {
                        setEditValue("");
                        setShowEditValue(false);
                        setShowEdit(true);
                      }}
                    >
                      Edit value
                    </Button>
                    <Button
                      variant="primary"
                      disabled={!baoConnected}
                      onClick={() => setShowCreate(true)}
                    >
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
            empty={<Box color="text-body-secondary">No secrets stored. Configure OpenBao and use "Create secret" to add one.</Box>}
          />
        </Container>

        {/* Create secret modal */}
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
              description="Unique label used to reference this secret in workloads (e.g. db-password). Stored at orchestrator/<name> in OpenBao."
            >
              <Input
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.detail.value }))}
                placeholder="db-password"
              />
            </FormField>
            <FormField
              label="Value"
              description="Plaintext value written directly to OpenBao. Not retrievable after creation."
              secondaryControl={
                <Button
                  variant="inline-link"
                  onClick={() => setShowCreateValue((v) => !v)}
                >
                  {showCreateValue ? "Hide" : "Show"}
                </Button>
              }
            >
              <div className={showCreateValue ? undefined : "secret-value-masked"}>
                <Textarea
                  value={form.value}
                  onChange={(e) => setForm((f) => ({ ...f, value: e.detail.value }))}
                  placeholder="super-secret-value"
                  rows={4}
                  spellcheck={false}
                />
              </div>
            </FormField>
          </SpaceBetween>
        </Modal>

        {/* Edit secret value modal */}
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
            <FormField
              label="New value"
              description="Overwrites the existing value at the same OpenBao path. The current value cannot be displayed here."
              secondaryControl={
                <Button
                  variant="inline-link"
                  onClick={() => setShowEditValue((v) => !v)}
                >
                  {showEditValue ? "Hide" : "Show"}
                </Button>
              }
            >
              <div className={showEditValue ? undefined : "secret-value-masked"}>
                <Textarea
                  value={editValue}
                  onChange={(e) => setEditValue(e.detail.value)}
                  placeholder="new-secret-value"
                  rows={4}
                  spellcheck={false}
                />
              </div>
            </FormField>
          </SpaceBetween>
        </Modal>

        {/* OpenBao config modal */}
        <Modal
          visible={showBaoConfig}
          header="OpenBao connection"
          onDismiss={() => setShowBaoConfig(false)}
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setShowBaoConfig(false)}>Cancel</Button>
                <Button variant="primary" loading={baoSaving} onClick={handleSaveBaoConfig}>
                  Save &amp; test
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <SpaceBetween size="m">
            <FormField label="Address" description="OpenBao server URL, e.g. https://bao.example.com:8200">
              <Input
                value={baoForm.address}
                onChange={(e) => setBaoForm((f) => ({ ...f, address: e.detail.value }))}
                placeholder="https://bao.example.com:8200"
              />
            </FormField>
            <FormField label="Token" description="Service token with KV read/write on <mount>/data/orchestrator/*. Ignored when Role ID is set.">
              <Input
                type="password"
                value={baoForm.token}
                onChange={(e) => setBaoForm((f) => ({ ...f, token: e.detail.value }))}
                placeholder="hvs.XXXXXXXX"
              />
            </FormField>
            <FormField label="Role ID" description="AppRole role_id — leave blank to authenticate with the token above instead">
              <Input
                value={baoForm.roleId}
                onChange={(e) => setBaoForm((f) => ({ ...f, roleId: e.detail.value }))}
                placeholder="AppRole role_id"
              />
            </FormField>
            <FormField label="Secret ID" description="AppRole secret_id, required when Role ID is set">
              <Input
                type="password"
                value={baoForm.secretId}
                onChange={(e) => setBaoForm((f) => ({ ...f, secretId: e.detail.value }))}
                placeholder="AppRole secret_id"
              />
            </FormField>
            <FormField label="Auth mount" description="AppRole auth mount path (default: approle)">
              <Input
                value={baoForm.authMount}
                onChange={(e) => setBaoForm((f) => ({ ...f, authMount: e.detail.value }))}
                placeholder="approle"
              />
            </FormField>
            <FormField label="Mount" description="KV v2 mount path (default: secret)">
              <Input
                value={baoForm.mount}
                onChange={(e) => setBaoForm((f) => ({ ...f, mount: e.detail.value }))}
                placeholder="secret"
              />
            </FormField>
            <Alert type="info">
              To trust a certificate signed by an internal CA for this connection, add it on the{" "}
              <b>Trusted CAs</b> page and check "Apply to OpenBao connection" — no need to paste it here.
            </Alert>
            <Checkbox
              checked={baoForm.insecureSkipVerify}
              onChange={(e) => setBaoForm((f) => ({ ...f, insecureSkipVerify: e.detail.checked }))}
            >
              Skip TLS certificate verification (testing only — disables all cert checks)
            </Checkbox>
          </SpaceBetween>
        </Modal>
      </SpaceBetween>
    </ContentLayout>
  );
}
