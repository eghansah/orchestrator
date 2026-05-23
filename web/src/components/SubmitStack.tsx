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

const PLACEHOLDER_YAML = `services:
  web:
    image: nginx:latest
    ports:
      - "8080:80"
`;

const empty = { name: "", compose_yaml: "" };

export default function SubmitStack({ visible, onDismiss, onSuccess, onError }: Props) {
  const [fields, setFields] = useState(empty);
  const [submitting, setSubmitting] = useState(false);

  function set(key: keyof typeof empty) {
    return (value: string) => setFields((f) => ({ ...f, [key]: value }));
  }

  function reset() {
    setFields(empty);
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
        </SpaceBetween>
      </Form>
    </Modal>
  );
}
