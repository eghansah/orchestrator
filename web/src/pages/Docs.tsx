import { useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import ContentLayout from "@cloudscape-design/components/content-layout";
import Header from "@cloudscape-design/components/header";
import Tabs from "@cloudscape-design/components/tabs";
import Box from "@cloudscape-design/components/box";
import Spinner from "@cloudscape-design/components/spinner";

function getBasePath(): string {
  const meta = document.querySelector<HTMLMetaElement>('meta[name="base-path"]');
  return meta?.content?.replace(/\/$/, "") ?? "";
}

function DocContent({ name }: { name: string }) {
  const [content, setContent] = useState<string | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    setContent(null);
    setError("");
    const token = localStorage.getItem("orchestrator_token") ?? "";
    fetch(getBasePath() + `/api/docs/${name}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.text();
      })
      .then(setContent)
      .catch((e) => setError(String(e)));
  }, [name]);

  if (error) return <Box color="text-status-error">{error}</Box>;
  if (content === null) return <Spinner />;

  return (
    <div style={{ maxWidth: 900, lineHeight: 1.6 }}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          code({ className, children, ...props }) {
            const inline = !className;
            return inline ? (
              <code
                style={{
                  background: "var(--color-background-container-content, #f2f3f3)",
                  padding: "1px 5px",
                  borderRadius: 3,
                  fontFamily: "monospace",
                  fontSize: "0.875em",
                }}
                {...props}
              >
                {children}
              </code>
            ) : (
              <pre
                style={{
                  background: "var(--color-background-container-content, #f2f3f3)",
                  padding: "12px 16px",
                  borderRadius: 4,
                  overflowX: "auto",
                  fontFamily: "monospace",
                  fontSize: "0.875em",
                }}
              >
                <code {...props}>{children}</code>
              </pre>
            );
          },
          table({ children }) {
            return (
              <table
                style={{
                  borderCollapse: "collapse",
                  width: "100%",
                  marginBottom: 16,
                  fontSize: "0.9em",
                }}
              >
                {children}
              </table>
            );
          },
          th({ children }) {
            return (
              <th
                style={{
                  border: "1px solid #d5dbdb",
                  padding: "8px 12px",
                  background: "var(--color-background-container-content, #f2f3f3)",
                  textAlign: "left",
                }}
              >
                {children}
              </th>
            );
          },
          td({ children }) {
            return (
              <td style={{ border: "1px solid #d5dbdb", padding: "8px 12px" }}>
                {children}
              </td>
            );
          },
        }}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}

export default function Docs() {
  return (
    <ContentLayout
      header={<Header variant="h1" description="Deployment and operations reference for this cluster.">Documentation</Header>}
    >
      <Tabs
        tabs={[
          {
            id: "deploy",
            label: "Deployment Guide",
            content: <DocContent name="deploy" />,
          },
          {
            id: "production",
            label: "Production Reference",
            content: <DocContent name="production" />,
          },
          {
            id: "changelog",
            label: "Changelog",
            content: <DocContent name="changelog" />,
          },
        ]}
      />
    </ContentLayout>
  );
}
