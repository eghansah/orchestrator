import { useEffect, useState } from "react";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Container from "@cloudscape-design/components/container";
import ContentLayout from "@cloudscape-design/components/content-layout";
import ExpandableSection from "@cloudscape-design/components/expandable-section";
import Flashbar, { FlashbarProps } from "@cloudscape-design/components/flashbar";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import Modal from "@cloudscape-design/components/modal";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Table from "@cloudscape-design/components/table";
import { api, ClusterState, CreateRegistryRequest, Registry, formatAge } from "../api";

interface Props {
  state: ClusterState | null;
  loading: boolean;
}

export default function Registries({ state, loading }: Props) {
  const registries: Registry[] = state?.registries ?? [];
  const [flash, setFlash] = useState<FlashbarProps.MessageDefinition[]>([]);
  const [selected, setSelected] = useState<Registry[]>([]);
  const [showCreate, setShowCreate] = useState(false);
  const [editTarget, setEditTarget] = useState<Registry | null>(null);
  const [form, setForm] = useState<CreateRegistryRequest>({ name: "", url: "", username: "", password: "" });
  const [saving, setSaving] = useState(false);

  // Catalog state
  const [catalogSearch, setCatalogSearch] = useState("");
  const [repos, setRepos] = useState<string[]>([]);
  const [catalogLoading, setCatalogLoading] = useState(false);

  // Tags modal state
  const [tagsRepo, setTagsRepo] = useState<string | null>(null);
  const [tags, setTags] = useState<string[]>([]);
  const [tagsLoading, setTagsLoading] = useState(false);

  // Env vars state: tag → env lines (null = not yet loaded)
  const [envMap, setEnvMap] = useState<Record<string, string[] | null>>({});

  const activeRegistry = selected.length === 1 ? selected[0] : null;

  // Reload catalog when selected registry changes.
  useEffect(() => {
    if (!activeRegistry) { setRepos([]); return; }
    loadCatalog(activeRegistry.id, "");
  }, [activeRegistry?.id]);

  function addFlash(type: FlashbarProps.Type, msg: string) {
    const id = String(Date.now());
    setFlash((f) => [...f, { type, content: msg, id, dismissible: true, onDismiss: () => setFlash((f) => f.filter((x) => x.id !== id)) }]);
  }

  async function loadCatalog(registryId: string, search: string) {
    setCatalogLoading(true);
    try {
      const data = await api.getRegistryCatalog(registryId, search || undefined);
      setRepos(data.repos ?? []);
    } catch (e) {
      addFlash("error", "Load catalog: " + String(e));
    } finally {
      setCatalogLoading(false);
    }
  }

  function handleSearchChange(value: string) {
    setCatalogSearch(value);
    if (activeRegistry) loadCatalog(activeRegistry.id, value);
  }

  async function handleRepoClick(repo: string) {
    setTagsRepo(repo);
    setTags([]);
    setEnvMap({});
    setTagsLoading(true);
    try {
      const data = await api.getRepoTags(activeRegistry!.id, repo);
      setTags(data.tags ?? []);
    } catch (e) {
      addFlash("error", "Load tags: " + String(e));
    } finally {
      setTagsLoading(false);
    }
  }

  async function handleLoadEnv(tag: string) {
    if (envMap[tag] !== undefined) return; // already loaded
    setEnvMap((m) => ({ ...m, [tag]: null })); // null = loading
    try {
      const data = await api.getImageEnv(activeRegistry!.id, tagsRepo!, tag);
      setEnvMap((m) => ({ ...m, [tag]: data.env ?? [] }));
    } catch (e) {
      setEnvMap((m) => ({ ...m, [tag]: [`Error: ${e}`] }));
    }
  }

  async function handleCreate() {
    if (!form.name || !form.url) { addFlash("error", "Name and URL are required"); return; }
    setSaving(true);
    try {
      await api.createRegistry(form);
      addFlash("success", `Registry "${form.name}" added`);
      setShowCreate(false);
      setForm({ name: "", url: "", username: "", password: "" });
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setSaving(false);
    }
  }

  function openEdit(r: Registry) {
    setEditTarget(r);
    setForm({ name: r.name, url: r.url, username: r.username, password: "" });
  }

  async function handleUpdate() {
    if (!editTarget) return;
    if (!form.name || !form.url) { addFlash("error", "Name and URL are required"); return; }
    setSaving(true);
    try {
      await api.updateRegistry(editTarget.id, form);
      addFlash("success", `Registry "${form.name}" updated`);
      setEditTarget(null);
      setForm({ name: "", url: "", username: "", password: "" });
    } catch (e) {
      addFlash("error", String(e));
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete() {
    for (const r of selected) {
      try {
        await api.deleteRegistry(r.id);
        addFlash("success", `Deleted "${r.name}"`);
      } catch (e) {
        addFlash("error", String(e));
      }
    }
    setSelected([]);
  }

  return (
    <ContentLayout header={<Header variant="h1">Registries</Header>}>
      <SpaceBetween size="l">
        <Flashbar items={flash} />

        {/* ── Registry list ──────────────────────────────────────────────── */}
        <Container>
          <Table
            loading={loading}
            loadingText="Loading registries"
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
                      disabled={selected.length !== 1}
                      onClick={() => selected.length === 1 && setSelected([selected[0]])}
                    >
                      Browse images
                    </Button>
                    <Button variant="primary" onClick={() => setShowCreate(true)}>
                      Add registry
                    </Button>
                  </SpaceBetween>
                }
              >
                Registries
              </Header>
            }
            selectionType="multi"
            selectedItems={selected}
            onSelectionChange={(e) => setSelected(e.detail.selectedItems)}
            trackBy="id"
            columnDefinitions={[
              {
                id: "name", header: "Name",
                cell: (r) => (
                  <Button variant="link" onClick={() => setSelected([r])}>
                    {r.name}
                  </Button>
                ),
              },
              { id: "url",      header: "URL",      cell: (r) => r.url },
              { id: "username", header: "Username", cell: (r) => r.username || "—" },
              { id: "age",      header: "Age",      cell: (r) => formatAge(r.created_at) },
            ]}
            items={registries}
            empty={<Box color="text-body-secondary">No registries configured.</Box>}
          />
        </Container>

        {/* ── Catalog ────────────────────────────────────────────────────── */}
        {activeRegistry && (
          <Container
            header={
              <Header
                description={`Browsing ${activeRegistry.url}`}
                actions={
                  <SpaceBetween direction="horizontal" size="xs">
                    <Input
                      placeholder="Search or type a repo name…"
                      value={catalogSearch}
                      onChange={(e) => handleSearchChange(e.detail.value)}
                      onKeyDown={(e) => {
                        if (e.detail.key === "Enter" && catalogSearch.trim()) {
                          handleRepoClick(catalogSearch.trim());
                        }
                      }}
                    />
                    {catalogSearch.trim() && (
                      <Button
                        iconName="search"
                        onClick={() => handleRepoClick(catalogSearch.trim())}
                      >
                        Look up
                      </Button>
                    )}
                    <Button
                      iconName="close"
                      variant="icon"
                      onClick={() => { setSelected([]); setCatalogSearch(""); setRepos([]); }}
                    />
                  </SpaceBetween>
                }
              >
                {activeRegistry.name} — images
              </Header>
            }
          >
            <Table
              loading={catalogLoading}
              loadingText="Loading catalog"
              columnDefinitions={[
                {
                  id: "repo",
                  header: "Repository",
                  cell: (repo) => (
                    <Button variant="link" onClick={() => handleRepoClick(repo)}>
                      {repo}
                    </Button>
                  ),
                },
              ]}
              items={repos}
              empty={
                <Box color="text-body-secondary">
                  {catalogSearch.trim()
                    ? `No repos matching "${catalogSearch}" in catalog — press Enter or click Look up to query directly.`
                    : "No repositories found in catalog."}
                </Box>
              }
            />
          </Container>
        )}

        {/* ── Tags modal ─────────────────────────────────────────────────── */}
        {tagsRepo !== null && (
          <Modal
            visible
            header={`Tags — ${tagsRepo}`}
            size="large"
            onDismiss={() => { setTagsRepo(null); setTags([]); setEnvMap({}); }}
            footer={
              <Box float="right">
                <Button onClick={() => { setTagsRepo(null); setTags([]); setEnvMap({}); }}>
                  Close
                </Button>
              </Box>
            }
          >
            {tagsLoading ? (
              <Box>Loading tags…</Box>
            ) : tags.length === 0 ? (
              <Box color="text-body-secondary">No tags found.</Box>
            ) : (
              <SpaceBetween size="xs">
                {tags.map((tag) => (
                  <ExpandableSection
                    key={tag}
                    headerText={tag}
                    onChange={({ detail }) => { if (detail.expanded) handleLoadEnv(tag); }}
                  >
                    {envMap[tag] === undefined ? null : envMap[tag] === null ? (
                      <Box>Loading env vars…</Box>
                    ) : envMap[tag]!.length === 0 ? (
                      <Box color="text-body-secondary">No environment variables defined.</Box>
                    ) : (
                      <Table
                        columnDefinitions={[
                          { id: "key",   header: "Variable", cell: (e) => e.split("=")[0] },
                          { id: "value", header: "Default value", cell: (e) => e.split("=").slice(1).join("=") || "—" },
                        ]}
                        items={envMap[tag]!}
                        trackBy={(e) => e}
                      />
                    )}
                  </ExpandableSection>
                ))}
              </SpaceBetween>
            )}
          </Modal>
        )}

        {/* ── Add registry modal ─────────────────────────────────────────── */}
        <Modal
          visible={showCreate}
          header="Add registry"
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
            <FormField label="Name" description="Display name for this registry">
              <Input
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.detail.value }))}
                placeholder="internal-harbor"
              />
            </FormField>
            <FormField label="URL" description="Registry root URL, e.g. https://registry.example.com">
              <Input
                value={form.url}
                onChange={(e) => setForm((f) => ({ ...f, url: e.detail.value }))}
                placeholder="https://registry.example.com"
              />
            </FormField>
            <FormField label="Username" description="Leave blank for unauthenticated registries">
              <Input
                value={form.username}
                onChange={(e) => setForm((f) => ({ ...f, username: e.detail.value }))}
              />
            </FormField>
            <FormField label="Password">
              <Input
                type="password"
                value={form.password}
                onChange={(e) => setForm((f) => ({ ...f, password: e.detail.value }))}
              />
            </FormField>
          </SpaceBetween>
        </Modal>
        {/* ── Edit registry modal ────────────────────────────────────────── */}
        <Modal
          visible={editTarget !== null}
          header={`Edit registry — ${editTarget?.name ?? ""}`}
          onDismiss={() => { setEditTarget(null); setForm({ name: "", url: "", username: "", password: "" }); }}
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => { setEditTarget(null); setForm({ name: "", url: "", username: "", password: "" }); }}>
                  Cancel
                </Button>
                <Button variant="primary" loading={saving} onClick={handleUpdate}>Save</Button>
              </SpaceBetween>
            </Box>
          }
        >
          <SpaceBetween size="m">
            <FormField label="Name">
              <Input
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.detail.value }))}
              />
            </FormField>
            <FormField label="URL" description="Registry root URL">
              <Input
                value={form.url}
                onChange={(e) => setForm((f) => ({ ...f, url: e.detail.value }))}
              />
            </FormField>
            <FormField label="Username" description="Leave blank for unauthenticated registries">
              <Input
                value={form.username}
                onChange={(e) => setForm((f) => ({ ...f, username: e.detail.value }))}
              />
            </FormField>
            <FormField label="Password" description="Leave blank to keep existing password">
              <Input
                type="password"
                value={form.password}
                onChange={(e) => setForm((f) => ({ ...f, password: e.detail.value }))}
                placeholder="unchanged"
              />
            </FormField>
          </SpaceBetween>
        </Modal>
      </SpaceBetween>
    </ContentLayout>
  );
}
