import React, { useEffect, useState } from "react";
import Box from "@cloudscape-design/components/box";
import Badge from "@cloudscape-design/components/badge";
import ContentLayout from "@cloudscape-design/components/content-layout";
import ExpandableSection from "@cloudscape-design/components/expandable-section";
import Header from "@cloudscape-design/components/header";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Spinner from "@cloudscape-design/components/spinner";
import TextContent from "@cloudscape-design/components/text-content";
import { api, ChangelogRelease } from "../api";

const sectionColor: Record<string, "blue" | "green" | "red" | "grey"> = {
  Added: "green",
  Changed: "blue",
  Fixed: "red",
  Removed: "grey",
};

function SectionBadge({ title }: { title: string }) {
  const color = sectionColor[title] ?? "grey";
  return <Badge color={color}>{title}</Badge>;
}

function ReleaseCard({ release, defaultExpanded }: { release: ChangelogRelease; defaultExpanded: boolean }) {
  return (
    <div
      style={{
        display: "flex",
        gap: 24,
        paddingBottom: 32,
        position: "relative",
      }}
    >
      {/* Timeline spine */}
      <div style={{ display: "flex", flexDirection: "column", alignItems: "center", flexShrink: 0 }}>
        <div
          style={{
            width: 14,
            height: 14,
            borderRadius: "50%",
            background: "var(--color-background-button-primary-default, #0972d3)",
            marginTop: 6,
            flexShrink: 0,
          }}
        />
        <div
          style={{
            width: 2,
            flex: 1,
            background: "var(--color-border-divider-default, #d5dbdb)",
            marginTop: 6,
          }}
        />
      </div>

      {/* Content */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <ExpandableSection
          defaultExpanded={defaultExpanded}
          headerText={
            <SpaceBetween direction="horizontal" size="xs" alignItems="center">
              <span style={{ fontWeight: 700, fontSize: "1.05em" }}>{release.version}</span>
              <Box color="text-body-secondary" fontSize="body-s">{release.date}</Box>
            </SpaceBetween>
          }
          headerDescription={release.summary}
          variant="container"
        >
          <SpaceBetween size="m">
            {release.sections.map((sec) => (
              <div key={sec.title}>
                <div style={{ marginBottom: 8 }}>
                  <SectionBadge title={sec.title} />
                </div>
                <TextContent>
                  <ul style={{ margin: 0, paddingLeft: 20 }}>
                    {sec.items.map((item, i) => (
                      <li key={i} style={{ marginBottom: 4 }}>
                        {item}
                      </li>
                    ))}
                  </ul>
                </TextContent>
              </div>
            ))}
          </SpaceBetween>
        </ExpandableSection>
      </div>
    </div>
  );
}

export default function Changelog() {
  const [releases, setReleases] = useState<ChangelogRelease[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .getChangelog()
      .then((data) => { setReleases(data); setError(null); })
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, []);

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="History of features, changes, and fixes across all releases."
        >
          Changelog
        </Header>
      }
    >
      {loading && <Spinner />}
      {error && <Box color="text-status-error">{error}</Box>}
      {!loading && !error && (
        <div style={{ maxWidth: 860 }}>
          {releases.map((r, i) => (
            <ReleaseCard key={r.version} release={r} defaultExpanded={i === 0} />
          ))}
          {/* End cap */}
          <div style={{ display: "flex", gap: 24 }}>
            <div style={{ display: "flex", flexDirection: "column", alignItems: "center", flexShrink: 0 }}>
              <div
                style={{
                  width: 14,
                  height: 14,
                  borderRadius: "50%",
                  background: "var(--color-border-divider-default, #d5dbdb)",
                  flexShrink: 0,
                }}
              />
            </div>
            <Box color="text-body-secondary" fontSize="body-s" padding={{ top: "xxs" }}>
              Project start
            </Box>
          </div>
        </div>
      )}
    </ContentLayout>
  );
}
