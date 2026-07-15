package webui

import "net/http"

// ChangelogSection groups related bullets under a heading within a release.
type ChangelogSection struct {
	Title string   `json:"title"` // e.g. "Added", "Changed", "Fixed"
	Items []string `json:"items"`
}

// ChangelogRelease describes one release.
type ChangelogRelease struct {
	Version  string             `json:"version"`
	Date     string             `json:"date"` // YYYY-MM or "unreleased"
	Summary  string             `json:"summary"`
	Sections []ChangelogSection `json:"sections"`
}

var changelog = []ChangelogRelease{
	{
		Version: "v0.6.0",
		Date:    "2026-06",
		Summary: "Mesh networking, replica fan-out, and system services management",
		Sections: []ChangelogSection{
			{
				Title: "Added",
				Items: []string{
					"WireGuard mesh overlay — every node gets a /24 subnet; containers on mesh0 receive stable mesh IPs across nodes",
					"meshrouterd — userspace WireGuard router running inside a rootless network namespace, distributed as an embedded OCI image",
					"Replica fan-out — `ctl run --replicas N` spreads N instances across distinct nodes",
					"*.mesh DNS zone — proxyd resolves <workload>.mesh to all live instance mesh IPs with multi-A for round-robin",
					"System services page — web console shows live status of ingressd, meshrouterd, and proxyd with start/stop controls",
					"MeshIP field on actual container state — flows from agent through gRPC sync to the leader's DNS table",
					"GroupName on workloads — links replica children back to the parent for DNS aggregation",
				},
			},
			{
				Title: "Changed",
				Items: []string{
					"EnsureMeshNetwork now propagates WireGuard tunnel MTU (1380) to the mesh0 bridge to prevent silent packet drops",
					"Reconciler dispatches tryFanOut for workloads with Replicas > 1, placing children on distinct nodes",
				},
			},
			{
				Title: "Fixed",
				Items: []string{
					"CreateIngressResponse proto was missing the system_port field — referenced by the server and ctl ingress but never declared",
				},
			},
		},
	},
	{
		Version: "v0.5.0",
		Date:    "2026-06",
		Summary: "Networks and volumes in the web console; config reload reliability fixes",
		Sections: []ChangelogSection{
			{
				Title: "Added",
				Items: []string{
					"Networks page — browse nerdctl networks with inspect detail view",
					"Volumes page — browse named volumes with inspect detail view",
					"Web UI is now a tracked build dependency — go generate rebuilds the frontend; binary always contains current UI",
				},
			},
			{
				Title: "Fixed",
				Items: []string{
					"proxyd config watcher was using inotify on the config file; atomic rename from the orchestrator killed the watch silently — switched to parent-directory watching",
					"ingressd had the same atomic-rename problem; switched to mtime polling (inotify cannot cross its bind mount)",
				},
			},
		},
	},
	{
		Version: "v0.4.0",
		Date:    "2026-06",
		Summary: "Pure-Go build tooling, containerised ingress, and embedded image registry",
		Sections: []ChangelogSection{
			{
				Title: "Added",
				Items: []string{
					"ingressd — HAProxy wrapper running as a rootless managed container; replaces the in-process reverse proxy",
					"Built-in OCI registry — serves embedded container images over localhost HTTP so nerdctl can pull without an external registry",
					"Pure-Go image assembler (cmd/build-image) — builds scratch-based OCI images from a compiled binary; replaces docker build in release pipelines",
					"All release binaries land in dist/ — orchestrator, ctl, proxyd, ingressd, meshrouterd",
					"Build tags with_ingressd_image and with_meshrouterd_image gate embedded image blobs",
					"Systemd user unit files for ingressd and proxyd",
					"Digest-based auto-replace for ingressd — container is recreated automatically when the embedded image changes",
				},
			},
			{
				Title: "Changed",
				Items: []string{
					"Routing model changed from workload-based to container FQDN + port — services reference containers by stable DNS name",
					"go-containerregistry/pkg/registry replaces the hand-rolled OCI registry implementation",
				},
			},
		},
	},
	{
		Version: "v0.3.0",
		Date:    "2026-05",
		Summary: "Secrets, MFA, standalone proxyd, templates, and domain TLS management",
		Sections: []ChangelogSection{
			{
				Title: "Added",
				Items: []string{
					"Secrets management — AES-256-GCM encryption at rest; --secret ENV=name flag for run and stacks; compose ${ENV_VAR} substitution via injected .env file",
					"proxyd — standalone TCP proxy and DNS binary serving svc.local. A records; extracted from in-process resolver",
					"TOTP-based MFA for admin login — setup on first login; --web-disable-mfa flag for development",
					"Containers page — flat list of all running containers across all nodes with live stats",
					"Templates page — save and deploy reusable workload definitions",
					"Workflow Builder — visual drag-and-drop compose stack editor (experimental)",
					"Domain TLS management — CSR generation, key regeneration with custom subject fields, PEM certificate import",
				},
			},
			{
				Title: "Changed",
				Items: []string{
					"Compose stack services are now eligible as service proxy backends",
				},
			},
			{
				Title: "Fixed",
				Items: []string{
					"Service port routing was broken when a workload had multiple port allocations",
					"Published ports were not shown on the workload detail page",
				},
			},
		},
	},
	{
		Version: "v0.2.0",
		Date:    "2026-04",
		Summary: "Web console, node management, registries, domains, and user administration",
		Sections: []ChangelogSection{
			{
				Title: "Added",
				Items: []string{
					"Web console built with Cloudscape Design System — sidebar navigation, login, and session management",
					"Node detail page with CPU, memory, and disk utilisation",
					"Workload detail page with live actual state, phase badge, and remove action",
					"Domains and HTTPS ingress — TLS termination at the ingress layer; per-domain cert management",
					"User management — create, disable, and delete local users",
					"LDAP authentication — optional external auth backend configurable at startup",
					"Registry management — add private registries; browse image catalog and tags",
					"--web-prefix flag — serve the console under a URL sub-path",
					"Release Makefile — cross-compiles static binaries for linux/amd64 and linux/arm64",
				},
			},
		},
	},
	{
		Version: "v0.1.0",
		Date:    "2026-03",
		Summary: "Initial release — rootless container orchestration with Raft consensus",
		Sections: []ChangelogSection{
			{
				Title: "Added",
				Items: []string{
					"Single-binary orchestrator — every node runs the same binary; Raft leader election via hashicorp/raft",
					"Rootless container execution via nerdctl exec wrapper — no direct containerd or OCI runtime calls",
					"Compose stacks as first-class workloads alongside single containers",
					"Scheduler — diffs desired vs actual state and places workloads on healthy nodes",
					"Agent — polls nerdctl ps and nerdctl compose ps; reports actual state back to the leader",
					"gRPC over mTLS for node-to-node and ctl-to-node communication",
					"Port allocator — auto-assigns host ports from a configurable pool",
					"ctl CLI — run, stack, remove, nodes, ps subcommands",
					"HTTP ingress proxy — per-node reverse proxy routes requests to containers by hostname",
					"Service discovery — svc.local. DNS via the in-process resolver",
					"Web console login — Bearer-token auth for API routes; password login via the browser UI",
				},
			},
		},
	},
}

func (s *Server) handleChangelog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, changelog)
}
