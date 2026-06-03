import React, { useEffect, useState } from "react";
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
import TextContent from "@cloudscape-design/components/text-content";
import { QRCodeSVG } from "qrcode.react";
import { api, hasToken, setToken, setAuthErrorHandler, useClusterState } from "./api";
import Overview from "./pages/Overview";
import Workloads from "./pages/Workloads";
import Nodes from "./pages/Nodes";
import Ingress from "./pages/Ingress";
import Services from "./pages/Services";
import Domains from "./pages/Domains";
import DomainDetail from "./pages/DomainDetail";
import Users from "./pages/Users";
import Registries from "./pages/Registries";
import Secrets from "./pages/Secrets";
import WorkflowBuilder from "./pages/WorkflowBuilder";
import WorkloadDetail from "./pages/WorkloadDetail";
import NodeDetail from "./pages/NodeDetail";
import Containers from "./pages/Containers";
import Templates from "./pages/Templates";

type Page = "overview" | "workloads" | "containers" | "templates" | "nodes" | "ingress" | "services" | "domains" | "users" | string;

const NAV_ITEMS: SideNavigationProps.Item[] = [
  { type: "link", text: "Overview", href: "#overview" },
  { type: "link", text: "Workloads", href: "#workloads" },
  { type: "link", text: "Containers", href: "#containers" },
  { type: "link", text: "Templates", href: "#templates" },
  { type: "link", text: "Nodes", href: "#nodes" },
  { type: "link", text: "Ingress", href: "#ingress" },
  { type: "link", text: "Domains", href: "#domains" },
  { type: "link", text: "Services", href: "#services" },
  { type: "link", text: "Users", href: "#users" },
  { type: "link", text: "Registries", href: "#registries" },
  { type: "link", text: "Secrets", href: "#secrets" },
  { type: "link", text: "Workflow Builder", href: "#workflow-builder" },
  { type: "divider" },
  { type: "link", text: "Sign out", href: "#signout" },
];

type LoginStage = "credentials" | "mfa_setup" | "mfa_required";

function LoginForm({ onAuthed }: { onAuthed: () => void }) {
  const [stage, setStage] = useState<LoginStage>("credentials");
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [pendingToken, setPendingToken] = useState("");
  const [setupSecret, setSetupSecret] = useState("");
  const [setupQRUri, setSetupQRUri] = useState("");
  const [totpCode, setTotpCode] = useState("");

  async function handleSubmit() {
    if (!password) { setError("Password is required"); return; }
    setLoading(true);
    setError("");
    try {
      const data = await api.login(username, password);
      if ("token" in data) {
        setToken(data.token);
        onAuthed();
      } else if (data.status === "mfa_setup") {
        setPendingToken(data.pending_token);
        setSetupSecret(data.secret);
        setSetupQRUri(data.qr_uri);
        setStage("mfa_setup");
      } else {
        setPendingToken(data.pending_token);
        setStage("mfa_required");
      }
    } catch (e) {
      setError(String(e).replace(/^Error:\s*/, ""));
    } finally {
      setLoading(false);
    }
  }

  async function handleMFAVerify() {
    if (!totpCode) { setError("Code is required"); return; }
    setLoading(true);
    setError("");
    try {
      const data = await api.verifyMFA(pendingToken, totpCode);
      setToken(data.token);
      onAuthed();
    } catch (e) {
      setError(String(e).replace(/^Error:\s*/, ""));
      setTotpCode("");
    } finally {
      setLoading(false);
    }
  }

  const wrapper = (content: React.ReactNode) => (
    <div style={{ display: "flex", justifyContent: "center", alignItems: "center", height: "100vh" }}>
      <div style={{ width: 420 }}>{content}</div>
    </div>
  );

  if (stage === "mfa_setup") {
    return wrapper(
      <Container header={<Header variant="h2">Set up two-factor authentication</Header>}>
        <SpaceBetween size="m">
          <TextContent>
            <p>Scan the QR code with your authenticator app (Google Authenticator, Authy, etc.), then enter the 6-digit code below to confirm setup.</p>
          </TextContent>
          <div style={{ display: "flex", justifyContent: "center" }}>
            <QRCodeSVG value={setupQRUri} size={200} />
          </div>
          <TextContent>
            <p>Or enter this key manually: <code style={{ wordBreak: "break-all" }}>{setupSecret}</code></p>
          </TextContent>
          <FormField label="Verification code" errorText={error}>
            <Input
              type="text"
              inputMode="numeric"
              value={totpCode}
              onChange={(e) => { setTotpCode(e.detail.value); setError(""); }}
              onKeyDown={(e) => { if (e.detail.key === "Enter") handleMFAVerify(); }}
              placeholder="6-digit code"
            />
          </FormField>
          <Button variant="primary" onClick={handleMFAVerify} loading={loading} fullWidth>
            Confirm setup
          </Button>
        </SpaceBetween>
      </Container>
    );
  }

  if (stage === "mfa_required") {
    return wrapper(
      <Container header={<Header variant="h2">Two-factor authentication</Header>}>
        <SpaceBetween size="m">
          <TextContent>
            <p>Enter the 6-digit code from your authenticator app.</p>
          </TextContent>
          <FormField label="Verification code" errorText={error}>
            <Input
              type="text"
              inputMode="numeric"
              value={totpCode}
              onChange={(e) => { setTotpCode(e.detail.value); setError(""); }}
              onKeyDown={(e) => { if (e.detail.key === "Enter") handleMFAVerify(); }}
              placeholder="6-digit code"
            />
          </FormField>
          <Button variant="primary" onClick={handleMFAVerify} loading={loading} fullWidth>
            Verify
          </Button>
          <Button variant="inline-link" onClick={() => { setStage("credentials"); setError(""); setTotpCode(""); }}>
            ← Back to sign in
          </Button>
        </SpaceBetween>
      </Container>
    );
  }

  return wrapper(
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
  const navProps = { ...sharedProps, onNavigate: setActivePage };

  const content = activePage.startsWith("workload-") ? (
    <WorkloadDetail workloadId={activePage.slice("workload-".length)} {...navProps} />
  ) : activePage.startsWith("node-") ? (
    <NodeDetail nodeId={activePage.slice("node-".length)} {...navProps} />
  ) : activePage.startsWith("domain-") ? (
    <DomainDetail domainId={activePage.slice("domain-".length)} onNavigate={setActivePage} />
  ) : ({
    overview: <Overview {...sharedProps} />,
    workloads: <Workloads {...navProps} />,
    containers: <Containers state={state} loading={loading} error={error} refetch={refetch} />,
    templates: <Templates {...navProps} />,
    nodes: <Nodes {...navProps} />,
    ingress: <Ingress />,
    domains: <Domains onNavigate={setActivePage} />,
    services: <Services />,
    users: <Users />,
    registries: <Registries state={state} loading={loading} refetch={refetch} />,
    secrets: <Secrets state={state} loading={loading} refetch={refetch} />,
    "workflow-builder": <WorkflowBuilder onNavigate={setActivePage} />,
  } as Record<string, React.ReactNode>)[activePage];

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
