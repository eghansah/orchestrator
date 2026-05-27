import { useEffect, useState } from "react";
import AppLayout from "@cloudscape-design/components/app-layout";
import SideNavigation, {
  SideNavigationProps,
} from "@cloudscape-design/components/side-navigation";
import Button from "@cloudscape-design/components/button";
import Container from "@cloudscape-design/components/container";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { api, hasToken, setToken, setAuthErrorHandler, useClusterState } from "./api";
import Overview from "./pages/Overview";
import Workloads from "./pages/Workloads";
import Nodes from "./pages/Nodes";
import Ingress from "./pages/Ingress";
import Services from "./pages/Services";

type Page = "overview" | "workloads" | "nodes" | "ingress" | "services";

const NAV_ITEMS: SideNavigationProps.Item[] = [
  { type: "link", text: "Overview", href: "#overview" },
  { type: "link", text: "Workloads", href: "#workloads" },
  { type: "link", text: "Nodes", href: "#nodes" },
  { type: "link", text: "Ingress", href: "#ingress" },
  { type: "link", text: "Services", href: "#services" },
  { type: "divider" },
  { type: "link", text: "Sign out", href: "#signout" },
];

function LoginForm({ onAuthed }: { onAuthed: () => void }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function handleSubmit() {
    if (!password) { setError("Password is required"); return; }
    setLoading(true);
    setError("");
    try {
      const data = await api.login(username, password);
      setToken(data.token);
      onAuthed();
    } catch (e) {
      setError(String(e).replace(/^Error:\s*/, ""));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div style={{ display: "flex", justifyContent: "center", alignItems: "center", height: "100vh" }}>
      <div style={{ width: 400 }}>
        <Container header={<Header variant="h2">Sign in to Orchestrator</Header>}>
          <SpaceBetween size="m">
            <FormField label="Username">
              <Input
                type="text"
                value={username}
                onChange={(e) => setUsername(e.detail.value)}
                onKeyDown={(e) => { if (e.detail.key === "Enter") handleSubmit(); }}
              />
            </FormField>
            <FormField label="Password" errorText={error}>
              <Input
                type="password"
                value={password}
                onChange={(e) => { setPassword(e.detail.value); setError(""); }}
                onKeyDown={(e) => { if (e.detail.key === "Enter") handleSubmit(); }}
                placeholder="Web console password"
              />
            </FormField>
            <Button variant="primary" onClick={handleSubmit} loading={loading} fullWidth>
              Sign in
            </Button>
          </SpaceBetween>
        </Container>
      </div>
    </div>
  );
}

export default function App() {
  const [authed, setAuthed] = useState(hasToken());
  const [activePage, setActivePage] = useState<Page>("overview");
  const { state, error, loading, refetch } = useClusterState();

  useEffect(() => {
    setAuthErrorHandler(() => setAuthed(false));
  }, []);

  if (!authed) {
    return <LoginForm onAuthed={() => setAuthed(true)} />;
  }

  async function handleSignOut() {
    try { await api.logout(); } catch { /* ignore */ }
    setToken("");
    setAuthed(false);
  }

  const sharedProps = { state, error, loading, refetch };

  const content = {
    overview: <Overview {...sharedProps} />,
    workloads: <Workloads {...sharedProps} />,
    nodes: <Nodes {...sharedProps} />,
    ingress: <Ingress />,
    services: <Services />,
  }[activePage];

  return (
    <AppLayout
      navigation={
        <SideNavigation
          header={{ text: "Orchestrator", href: "#overview" }}
          items={NAV_ITEMS}
          activeHref={`#${activePage}`}
          onFollow={(e) => {
            e.preventDefault();
            const id = e.detail.href.slice(1);
            if (id === "signout") { handleSignOut(); return; }
            setActivePage(id as Page);
          }}
        />
      }
      content={content}
      toolsHide
    />
  );
}
