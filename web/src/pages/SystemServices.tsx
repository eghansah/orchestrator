import React, { useCallback, useEffect, useState } from "react";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Header from "@cloudscape-design/components/header";
import SpaceBetween from "@cloudscape-design/components/space-between";
import StatusIndicator from "@cloudscape-design/components/status-indicator";
import Table from "@cloudscape-design/components/table";
import { api, SystemServiceInfo } from "../api";
import SystemWarnings from "../components/SystemWarnings";

function statusIndicator(s: SystemServiceInfo["status"]) {
  switch (s) {
    case "running":
      return <StatusIndicator type="success">Running</StatusIndicator>;
    case "stopped":
      return <StatusIndicator type="stopped">Stopped</StatusIndicator>;
    case "not found":
      return <StatusIndicator type="warning">Not found</StatusIndicator>;
    default:
      return <StatusIndicator type="loading">Unknown</StatusIndicator>;
  }
}

export default function SystemServices() {
  const [services, setServices] = useState<SystemServiceInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [acting, setActing] = useState<Record<string, boolean>>({});

  const load = useCallback(async () => {
    try {
      const data = await api.listSystemServices();
      setServices(data);
      setError(null);
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  }, [load]);

  async function act(name: string, action: "start" | "stop") {
    setActing((prev) => ({ ...prev, [name]: true }));
    try {
      if (action === "start") await api.startSystemService(name);
      else await api.stopSystemService(name);
      await load();
    } catch (e) {
      setError(String(e));
    } finally {
      setActing((prev) => ({ ...prev, [name]: false }));
    }
  }

  async function startAll() {
    const targets = services.filter((s) => s.controllable && s.status !== "running");
    await Promise.allSettled(targets.map((s) => act(s.name, "start")));
    await load();
  }

  async function stopAll() {
    const targets = services.filter((s) => s.controllable && s.status === "running");
    await Promise.allSettled(targets.map((s) => act(s.name, "stop")));
    await load();
  }

  const anyRunning = services.some((s) => s.controllable && s.status === "running");
  const anyStopped = services.some((s) => s.controllable && s.status !== "running");

  return (
    <SpaceBetween size="l">
      <SystemWarnings />
      <Table
        header={
          <Header
            variant="awsui-h1-sticky"
            actions={
              <SpaceBetween direction="horizontal" size="xs">
                <Button onClick={load} iconName="refresh" disabled={loading} />
                <Button onClick={stopAll} disabled={!anyRunning}>Stop all</Button>
                <Button variant="primary" onClick={startAll} disabled={!anyStopped}>
                  Start all
                </Button>
              </SpaceBetween>
            }
          >
            System services
          </Header>
        }
        loading={loading}
        loadingText="Loading services…"
        items={services}
        empty={
          <Box textAlign="center" color="inherit">
            {error ? <span style={{ color: "red" }}>{error}</span> : "No services found."}
          </Box>
        }
        columnDefinitions={[
          {
            id: "name",
            header: "Service",
            cell: (s) => <strong>{s.name}</strong>,
            width: 160,
          },
          {
            id: "role",
            header: "Role",
            cell: (s) => s.role,
          },
          {
            id: "kind",
            header: "Type",
            cell: (s) => (s.kind === "container" ? "Container" : "Host process"),
            width: 130,
          },
          {
            id: "status",
            header: "Status",
            cell: (s) => statusIndicator(s.status),
            width: 140,
          },
          {
            id: "actions",
            header: "",
            cell: (s) => {
              if (!s.controllable) return <Box color="text-body-secondary">—</Box>;
              const busy = !!acting[s.name];
              if (s.status === "running") {
                return (
                  <Button
                    variant="inline-link"
                    loading={busy}
                    onClick={() => act(s.name, "stop")}
                  >
                    Stop
                  </Button>
                );
              }
              return (
                <Button
                  variant="inline-link"
                  loading={busy}
                  onClick={() => act(s.name, "start")}
                >
                  Start
                </Button>
              );
            },
            width: 100,
          },
        ]}
      />
      {error && (
        <Box color="text-status-error">{error}</Box>
      )}
    </SpaceBetween>
  );
}
