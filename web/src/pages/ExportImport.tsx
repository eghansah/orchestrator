import React, { useRef, useState } from "react";
import Alert from "@cloudscape-design/components/alert";
import Box from "@cloudscape-design/components/box";
import Button from "@cloudscape-design/components/button";
import Checkbox from "@cloudscape-design/components/checkbox";
import ColumnLayout from "@cloudscape-design/components/column-layout";
import Container from "@cloudscape-design/components/container";
import Header from "@cloudscape-design/components/header";
import SpaceBetween from "@cloudscape-design/components/space-between";
import StatusIndicator from "@cloudscape-design/components/status-indicator";
import { api, ImportReport } from "../api";

export default function ExportImport() {
  // ── Export ──────────────────────────────────────────────────────────────────
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState<string | null>(null);

  async function handleExport() {
    setExporting(true);
    setExportError(null);
    try {
      const blob = await api.exportCluster();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `cluster-export-${new Date().toISOString().slice(0, 10)}.yaml`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setExportError(String(e));
    } finally {
      setExporting(false);
    }
  }

  // ── Import ──────────────────────────────────────────────────────────────────
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [overwrite, setOverwrite] = useState(false);
  const [importing, setImporting] = useState(false);
  const [importError, setImportError] = useState<string | null>(null);
  const [report, setReport] = useState<ImportReport | null>(null);

  function handleFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0] ?? null;
    setSelectedFile(file);
    setReport(null);
    setImportError(null);
  }

  async function handleImport() {
    if (!selectedFile) return;
    setImporting(true);
    setImportError(null);
    setReport(null);
    try {
      const text = await selectedFile.text();
      const result = await api.importCluster(text, overwrite);
      setReport(result);
    } catch (e) {
      setImportError(String(e));
    } finally {
      setImporting(false);
    }
  }

  const totalImported = report
    ? Object.values(report.imported).reduce((a, b) => a + b, 0)
    : 0;

  return (
    <SpaceBetween size="l">
      <Header variant="h1" description="Move your cluster configuration between orchestrator instances">
        Export / Import
      </Header>

      {/* Export */}
      <Container
        header={
          <Header
            variant="h2"
            description="Downloads a portable YAML bundle containing workloads, domains, ingress rules, services, secret references (not values), registries, and templates."
          >
            Export configuration
          </Header>
        }
      >
        <SpaceBetween size="m">
          {exportError && (
            <Alert type="error" header="Export failed">
              {exportError}
            </Alert>
          )}
          <Box>
            <Button
              variant="primary"
              loading={exporting}
              onClick={handleExport}
            >
              Download cluster bundle
            </Button>
          </Box>
          <Alert type="info">
            Secret <strong>values</strong> are not included in the bundle — they remain in
            OpenBao. After importing, point the new cluster at the same OpenBao instance (or a
            replica) and secrets will resolve automatically.
          </Alert>
        </SpaceBetween>
      </Container>

      {/* Import */}
      <Container
        header={
          <Header
            variant="h2"
            description="Apply a previously exported YAML bundle to this cluster. Resources are matched by name."
          >
            Import configuration
          </Header>
        }
      >
        <SpaceBetween size="m">
          {importError && (
            <Alert type="error" header="Import failed">
              {importError}
            </Alert>
          )}

          {report && (
            <Alert
              type={report.errors && report.errors.length > 0 ? "warning" : "success"}
              header={
                report.errors && report.errors.length > 0
                  ? `Import completed with ${report.errors.length} error(s)`
                  : `Import complete — ${totalImported} resource(s) applied`
              }
            >
              <SpaceBetween size="xs">
                {/* Imported counts */}
                {Object.entries(report.imported).some(([, v]) => v > 0) && (
                  <ColumnLayout columns={3} variant="text-grid">
                    {Object.entries(report.imported)
                      .filter(([, v]) => v > 0)
                      .map(([k, v]) => (
                        <div key={k}>
                          <StatusIndicator type="success">{k}: {v}</StatusIndicator>
                        </div>
                      ))}
                  </ColumnLayout>
                )}

                {/* Skipped */}
                {report.skipped && report.skipped.length > 0 && (
                  <SpaceBetween size="xxxs">
                    <Box variant="small" fontWeight="bold">Skipped (already exist):</Box>
                    {report.skipped.map((s, i) => (
                      <StatusIndicator key={i} type="warning">{s}</StatusIndicator>
                    ))}
                  </SpaceBetween>
                )}

                {/* Errors */}
                {report.errors && report.errors.length > 0 && (
                  <SpaceBetween size="xxxs">
                    <Box variant="small" fontWeight="bold">Errors:</Box>
                    {report.errors.map((e, i) => (
                      <StatusIndicator key={i} type="error">{e}</StatusIndicator>
                    ))}
                  </SpaceBetween>
                )}
              </SpaceBetween>
            </Alert>
          )}

          <SpaceBetween size="s">
            <Box>
              <Button
                onClick={() => fileInputRef.current?.click()}
                disabled={importing}
              >
                {selectedFile ? selectedFile.name : "Choose bundle file…"}
              </Button>
              <input
                ref={fileInputRef}
                type="file"
                accept=".yaml,.yml"
                style={{ display: "none" }}
                onChange={handleFileChange}
              />
            </Box>

            <Checkbox
              checked={overwrite}
              onChange={({ detail }) => setOverwrite(detail.checked)}
            >
              Overwrite resources that already exist (matched by name)
            </Checkbox>

            <Button
              variant="primary"
              disabled={!selectedFile || importing}
              loading={importing}
              onClick={handleImport}
            >
              Import
            </Button>
          </SpaceBetween>
        </SpaceBetween>
      </Container>
    </SpaceBetween>
  );
}
