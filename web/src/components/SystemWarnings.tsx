import React, { useEffect, useState } from "react";
import Alert from "@cloudscape-design/components/alert";
import SpaceBetween from "@cloudscape-design/components/space-between";
import { api } from "../api";

// Polls cluster-wide diagnostic warnings (e.g. a port 443 conflict with
// another ingress controller) and renders them as alert banners.
export default function SystemWarnings() {
  const [warnings, setWarnings] = useState<string[]>([]);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      api
        .getSystemDiagnostics()
        .then((d) => { if (!cancelled) setWarnings(d.warnings); })
        .catch(() => { /* diagnostics are best-effort; ignore fetch errors */ });
    };
    load();
    const id = setInterval(load, 15000);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  if (warnings.length === 0) return null;

  return (
    <SpaceBetween size="s">
      {warnings.map((w, i) => (
        <Alert key={i} type="warning" header="Ingress conflict detected">
          {w}
        </Alert>
      ))}
    </SpaceBetween>
  );
}
