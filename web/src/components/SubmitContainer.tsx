import { useState } from "react";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Form from "@cloudscape-design/components/form";
import FormField from "@cloudscape-design/components/form-field";
import Input from "@cloudscape-design/components/input";
import Modal from "@cloudscape-design/components/modal";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Textarea from "@cloudscape-design/components/textarea";
import { api } from "../api";

interface Props {
  visible: boolean;
  onDismiss: () => void;
  onSuccess: (workloadId: string) => void;
  onError: (msg: string) => void;
}

const empty = { name: "", image: "", command: "", env: "", ports: "" };

export default function SubmitContainer({ visible, onDismiss, onSuccess, onError }: Props) {
  const [fields, setFields] = useState(empty);
  const [submitting, setSubmitting] = useState(false);

  function set(key: keyof typeof empty) {
    return (value: string) => setFields((f) => ({ ...f, [key]: value }));
  }

  function reset() {
    setFields(empty);
  }

  async function handleSubmit() {
    if (!fields.name || !fields.image) {
      onError("Name and Image are required");
      return;
    }
    setSubmitting(true);
    try {
      const res = await api.submitContainer({
        name: fields.name.trim(),
        image: fields.image.trim(),
        command: fields.command.trim() ? fields.command.trim().split(/\s+/) : [],
        env: fields.env.split("\n").map((s) => s.trim()).filter(Boolean),
        ports: fields.ports.split("\n").map((s) => s.trim()).filter(Boolean),
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
      header="Submit Container"
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
          <FormField label="Name" constraintText="Container name (alphanumeric, dashes, dots)">
            <Input value={fields.name} onChange={(e) => set("name")(e.detail.value)} />
          </FormField>
          <FormField label="Image">
            <Input
              value={fields.image}
              placeholder="nginx:latest"
              onChange={(e) => set("image")(e.detail.value)}
            />
          </FormField>
          <FormField label="Command" constraintText="Optional — space-separated arguments">
            <Input
              value={fields.command}
              placeholder="nginx -g 'daemon off;'"
              onChange={(e) => set("command")(e.detail.value)}
            />
          </FormField>
          <FormField label="Environment variables" constraintText="One KEY=VALUE per line">
            <Textarea
              value={fields.env}
              placeholder={"PORT=8080\nDEBUG=true"}
              onChange={(e) => set("env")(e.detail.value)}
            />
          </FormField>
          <FormField
            label="Ports"
            constraintText="One HOST:CONTAINER[/proto] per line"
          >
            <Textarea
              value={fields.ports}
              placeholder={"8080:80\n9000:9000/udp"}
              onChange={(e) => set("ports")(e.detail.value)}
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}
