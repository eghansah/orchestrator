import { useState } from "react";
import Alert from "@cloudscape-design/components/alert";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Form from "@cloudscape-design/components/form";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Modal from "@cloudscape-design/components/modal";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Textarea from "@cloudscape-design/components/textarea";
import { api } from "../api";
import { MountEntry, RefEntry, entriesToMounts, entriesToRefs } from "../workloadRefs";

// Returns true if the YAML contains port bindings that expose on all interfaces
// (i.e. missing the 127.0.0.1: prefix), e.g. "8080:80" or "0.0.0.0:8080:80",
// or uses network_mode: host.
function hasDirectHostBinding(yaml: string): boolean {
  if (/network_mode\s*:\s*["']?host["']?/m.test(yaml)) return true;
  const portLine = /^\s*-\s+["']?(.+?)["']?\s*$/gm;
  let m: RegExpExecArray | null;
  while ((m = portLine.exec(yaml)) !== null) {
    const val = m[1].trim().replace(/\/\w+$/, ""); // strip /tcp /udp
    const parts = val.split(":");
    if (parts.length === 2 && /^\d+$/.test(parts[0])) {
      return true; // HOST_PORT:CONTAINER_PORT → binds to 0.0.0.0
    }
    if (parts.length === 3 && parts[0] !== "127.0.0.1") {
      return true; // ADDR:HOST_PORT:CONTAINER_PORT where ADDR is not loopback
    }
  }
  return false;
}

interface Props {
  visible: boolean;
  onDismiss: () => void;
  onSuccess: (workloadId: string) => void;
  onError: (msg: string) => void;
}

const PLACEHOLDER_YAML = `services:
  web:
    image: nginx:latest
    ports:
      - "8080:80"
`;

const empty = { name: "", compose_yaml: "" };

export default function SubmitStack({ visible, onDismiss, onSuccess, onError }: Props) {
  const [fields, setFields] = useState(empty);
  const [secretRefRows, setSecretRefRows] = useState<RefEntry[]>([]);
  const [configRefRows, setConfigRefRows] = useState<RefEntry[]>([]);
  const [secretMountRows, setSecretMountRows] = useState<MountEntry[]>([]);
  const [submitting, setSubmitting] = useState(false);

  function set(key: keyof typeof empty) {
    return (value: string) => setFields((f) => ({ ...f, [key]: value }));
  }

  function reset() {
    setFields(empty);
    setSecretRefRows([]);
    setConfigRefRows([]);
    setSecretMountRows([]);
  }

  async function handleSubmit() {
    if (!fields.name || !fields.compose_yaml.trim()) {
      onError("Name and Compose YAML are required");
      return;
    }
    setSubmitting(true);
    try {
      const res = await api.submitStack({
        name: fields.name.trim(),
        compose_yaml: fields.compose_yaml,
        secret_refs: entriesToRefs(secretRefRows),
        config_refs: entriesToRefs(configRefRows),
        secret_mounts: entriesToMounts(secretMountRows),
      });
      if (!res.accepted) {
        onError(`Rejected: ${res.reason}`);
      } else {
        reset();
        onSuccess(res.workload_id ?? "");
      }
    } catch (e) {
      onError(String(e));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible={visible}
      onDismiss={() => { reset(); onDismiss(); }}
      header="Submit Compose Stack"
      size="large"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={() => { reset(); onDismiss(); }}>
              Cancel
            </Button>
            <Button variant="primary" loading={submitting} onClick={handleSubmit}>
              Submit
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          <FormField label="Stack name" constraintText="Alphanumeric, dashes, dots">
            <Input value={fields.name} onChange={(e) => set("name")(e.detail.value)} />
          </FormField>
          <FormField label="Compose YAML">
            <Textarea
              rows={14}
              value={fields.compose_yaml}
              placeholder={PLACEHOLDER_YAML}
              onChange={(e) => set("compose_yaml")(e.detail.value)}
            />
          </FormField>
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
          {hasDirectHostBinding(fields.compose_yaml) && (
            <Alert type="warning" header="Direct host binding detected">
              One or more containers bind ports to all network interfaces (e.g.{" "}
              <code>8080:80</code>) or use <code>network_mode: host</code>. This
              exposes services on the host's public interfaces. Prefer binding to{" "}
              <code>127.0.0.1</code> (e.g. <code>127.0.0.1:8080:80</code>) unless
              external access is intentional.
            </Alert>
          )}
        </SpaceBetween>
      </Form>
    </Modal>
  );
}
