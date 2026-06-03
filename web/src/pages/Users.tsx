import { useCallback, useEffect, useState } from "react";
import {
  Badge,
  Button,
  ContentLayout,
  Flashbar,
  FlashbarProps,
  FormField,
  Header,
  Input,
  Modal,
  SpaceBetween,
  Table,
} from "@cloudscape-design/components";
import { api, User } from "../api";
import { formatAge } from "../api";

export default function Users() {
  const [users, setUsers] = useState<User[]>([]);
  const [loading, setLoading] = useState(true);
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [creating, setCreating] = useState(false);
  const [username, setUsername] = useState("");
  const [selected, setSelected] = useState<User[]>([]);

  const load = useCallback(async () => {
    try {
      const data = await api.listUsers();
      setUsers(data ?? []);
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
    setUsername("");
    setCreating(true);
  }

  function closeCreate() {
    setCreating(false);
    setUsername("");
  }

  async function handleCreate() {
    if (!username.trim()) {
      addFlash("error", "Username is required");
      return;
    }
    try {
      const resp = await api.createUser({ username: username.trim() });
      addFlash("success", `User ${resp.username} created`);
      closeCreate();
      load();
    } catch (e) {
      addFlash("error", String(e));
    }
  }

  async function handleToggle() {
    for (const u of selected) {
      try {
        const resp = await api.toggleUser(u.id);
        addFlash("success", `${resp.username} ${resp.enabled ? "enabled" : "disabled"}`);
      } catch (e) {
        addFlash("error", String(e));
      }
    }
    setSelected([]);
    load();
  }

  async function handleDelete() {
    for (const u of selected) {
      try {
        await api.deleteUser(u.id);
        addFlash("success", `Deleted ${u.username}`);
      } catch (e) {
        addFlash("error", String(e));
      }
    }
    setSelected([]);
    load();
  }

  const allSelectedEnabled = selected.length > 0 && selected.every((u) => u.enabled);
  const toggleLabel = allSelectedEnabled ? "Disable" : "Enable";

  return (
    <ContentLayout
      notifications={<Flashbar items={flash} />}
      header={
        <Header
          variant="h1"
          description="AD accounts permitted to access the web console. Users authenticate with their AD credentials via LDAP."
          actions={<Button iconName="refresh" onClick={load}>Refresh</Button>}
        >
          Users
        </Header>
      }
    >
      <Table
        loading={loading}
        loadingText="Loading users"
        header={
          <Header
            actions={
              <SpaceBetween direction="horizontal" size="xs">
                <Button disabled={selected.length === 0} onClick={handleDelete}>
                  Delete
                </Button>
                <Button disabled={selected.length === 0} onClick={handleToggle}>
                  {toggleLabel}
                </Button>
                <Button variant="primary" onClick={openCreate}>
                  Add user
                </Button>
              </SpaceBetween>
            }
          >
            Users
          </Header>
        }
        selectionType="multi"
        selectedItems={selected}
        onSelectionChange={(e) => setSelected(e.detail.selectedItems)}
        trackBy="id"
        columnDefinitions={[
          { id: "username", header: "Username", cell: (u) => u.username },
          {
            id: "status",
            header: "Status",
            cell: (u) =>
              u.enabled ? (
                <Badge color="green">Enabled</Badge>
              ) : (
                <Badge color="grey">Disabled</Badge>
              ),
          },
          {
            id: "mfa",
            header: "MFA",
            cell: (u) =>
              u.mfa_enabled ? (
                <Badge color="blue">Enrolled</Badge>
              ) : (
                <Badge color="grey">Not enrolled</Badge>
              ),
          },
          { id: "age", header: "Added", cell: (u) => formatAge(u.created_at) },
          {
            id: "actions",
            header: "",
            cell: (u) => (
              <SpaceBetween direction="horizontal" size="xs">
                <Button
                  variant="inline-link"
                  onClick={async () => {
                    try {
                      const resp = await api.toggleUser(u.id);
                      addFlash("success", `${resp.username} ${resp.enabled ? "enabled" : "disabled"}`);
                      load();
                    } catch (e) {
                      addFlash("error", String(e));
                    }
                  }}
                >
                  {u.enabled ? "Disable" : "Enable"}
                </Button>
                {u.mfa_enabled && (
                  <Button
                    variant="inline-link"
                    onClick={async () => {
                      try {
                        await api.resetUserMFA(u.id);
                        addFlash("success", `MFA reset for ${u.username}`);
                        load();
                      } catch (e) {
                        addFlash("error", String(e));
                      }
                    }}
                  >
                    Reset MFA
                  </Button>
                )}
              </SpaceBetween>
            ),
          },
        ]}
        items={users}
        empty="No users — add an AD account to get started"
      />

      <Modal
        visible={creating}
        onDismiss={closeCreate}
        header="Add user"
        footer={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={closeCreate}>Cancel</Button>
            <Button variant="primary" onClick={handleCreate}>Add</Button>
          </SpaceBetween>
        }
      >
        <FormField
          label="AD username"
          description="The user will authenticate with their existing AD password via LDAP."
          constraintText="Required — must match the AD account name exactly"
        >
          <Input
            value={username}
            onChange={(e) => setUsername(e.detail.value)}
            onKeyDown={(e) => { if (e.detail.key === "Enter") handleCreate(); }}
            placeholder="alice"
          />
        </FormField>
      </Modal>
    </ContentLayout>
  );
}
