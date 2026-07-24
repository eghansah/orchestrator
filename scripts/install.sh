#!/usr/bin/env bash
# install.sh — sets up a usermode orchestrator node: rootless container
# runtime, orchestrator/ctl/proxyd binaries, and systemd --user services.
#
# This script never runs as root and never invokes sudo. Anything that
# genuinely requires root (cgroup delegation, lingering) is a one-time host
# prerequisite it only checks for; it prints the exact command for an
# administrator to run separately if a check fails.
#
# Auto-detects mode by looking for a vendored nerdctl-full-linux-*.tar.gz
# next to this script:
#   - present  -> "offline" mode: extract and install that bundle. No network
#                 access is used by this script in this mode.
#   - absent   -> "online" mode: assume rootless nerdctl is already installed
#                 (see docs/deploy.md §1) and just probe for it.
#
# See docs/install.md for the full walkthrough.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Read early: the XDG_RUNTIME_DIR persistence loop below checks it before
# argument parsing (and thus before the "defaults" block) runs.
DRY_RUN=false

# ── logging ──────────────────────────────────────────────────────────────────
log()  { printf '[install.sh] %s\n' "$*"; }
warn() { printf '[install.sh][WARN] %s\n' "$*" >&2; }
fail() { printf '[install.sh][FAIL] %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] && fail "install.sh must not be run as root (this is a strictly usermode installer with no root/sudo steps of its own). Run it as the target user."

# rootless tooling (systemctl --user, the vendored containerd-rootless-setuptool.sh)
# needs XDG_RUNTIME_DIR pointed at this user's runtime dir. A real login sets this
# via PAM; reaching this shell through 'sudo -u'/'su' does not, so default it here
# rather than let systemctl --user fail later with a cryptic "Needs systemd" error.
if [ -z "${XDG_RUNTIME_DIR:-}" ]; then
	XDG_RUNTIME_DIR="/run/user/$(id -u)"
	export XDG_RUNTIME_DIR
	[ -d "$XDG_RUNTIME_DIR" ] || fail "XDG_RUNTIME_DIR is unset and $XDG_RUNTIME_DIR doesn't exist — user@$(id -u).service probably isn't running. Enable lingering ('sudo loginctl enable-linger $USER') or log in as $USER directly, then re-run install.sh."
fi

# Persist the same export for future shells on this account (e.g. 'sudo -u' /
# 'su' hops for manual nerdctl-full commands like containerd-rootless-setuptool.sh),
# which don't go through PAM and so never get XDG_RUNTIME_DIR set on their own.
XDG_RUNTIME_DIR_MARKER="# added by orchestrator install.sh"
# .bashrc is sourced by interactive non-login shells (plain 'sudo -u user bash'),
# so always ensure it exists. .bash_profile/.profile cover login shells
# ('sudo -u user -i') — only touch those if the account already has one, since
# creating one from scratch can change existing login-shell behavior.
for rcfile in "$HOME/.bashrc" "$HOME/.bash_profile" "$HOME/.profile"; do
	[ "$rcfile" = "$HOME/.bashrc" ] || [ -f "$rcfile" ] || continue
	if [ -f "$rcfile" ] && grep -qF "$XDG_RUNTIME_DIR_MARKER" "$rcfile" 2>/dev/null; then
		continue
	fi
	if $DRY_RUN; then
		log "[DRY-RUN] would persist XDG_RUNTIME_DIR export to $rcfile"
	else
		printf '\n%s\nexport XDG_RUNTIME_DIR=/run/user/%s\n' "$XDG_RUNTIME_DIR_MARKER" "$(id -u)" >>"$rcfile"
		log "persisted XDG_RUNTIME_DIR export to $rcfile"
	fi
done

# ── defaults ─────────────────────────────────────────────────────────────────
DATA_DIR="${HOME}/.local/share/orchestrator"
NODE_ID="$(hostname)"
GRPC_ADDR="0.0.0.0:7946"
RAFT_ADDR="0.0.0.0:7947"
WEB_ADDR="0.0.0.0:7948"
INGRESS_ADDR="0.0.0.0:8080"
INGRESS_TLS_ADDR="0.0.0.0:8443"
DNS_ADDR="0.0.0.0:5353"
NAMESPACE="orchestrator"
ADMIN_TOKEN=""
BOOTSTRAP=false
JOIN=""
JOIN_TOKEN=""
NERDCTL_OVERRIDE=""
DRY_RUN=false
SKIP_RUNTIME_SETUP=false

run() {
	if $DRY_RUN; then
		printf '[install.sh][DRY-RUN] %s\n' "$*"
	else
		"$@"
	fi
}

usage() {
	cat <<'EOF'
Usage: install.sh [options]

Cluster topology (passed through to the orchestrator systemd unit):
  --bootstrap              Bootstrap a brand-new cluster (use once, on the first node)
  --join HOST:PORT         Join an existing cluster through this node's gRPC address
  --join-token TOKEN       Cluster join secret (omit to load <data-dir>/join-token on restart)
  --node-id ID             This node's ID (default: hostname)
  --grpc-addr ADDR         gRPC listen address (default: 0.0.0.0:7946)
  --raft-addr ADDR         Raft transport address (default: 0.0.0.0:7947)
  --web-addr ADDR          Web console listen address (default: 0.0.0.0:7948)
  --ingress-addr ADDR      HTTP ingress listen address (default: 0.0.0.0:8080)
  --ingress-tls-addr ADDR  HTTPS ingress listen address (default: 0.0.0.0:8443)
  --dns-addr ADDR          svc.local DNS listen address (default: 0.0.0.0:5353)
  --data-dir DIR           Persistent storage dir (default: ~/.local/share/orchestrator)
  --admin-token TOKEN      Bearer token for ControlService/web console (default: auto-generated)
  --namespace NS           nerdctl namespace for managed containers (default: orchestrator)
  --nerdctl PATH           Use this nerdctl binary instead of auto-detecting one

Installer behavior:
  --dry-run                Print what would happen without doing it
  --skip-runtime-setup     Don't touch the container runtime; assume nerdctl already works
  -h, --help               Show this help
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--bootstrap) BOOTSTRAP=true; shift ;;
	--join) JOIN="$2"; shift 2 ;;
	--join-token) JOIN_TOKEN="$2"; shift 2 ;;
	--node-id) NODE_ID="$2"; shift 2 ;;
	--grpc-addr) GRPC_ADDR="$2"; shift 2 ;;
	--raft-addr) RAFT_ADDR="$2"; shift 2 ;;
	--web-addr) WEB_ADDR="$2"; shift 2 ;;
	--ingress-addr) INGRESS_ADDR="$2"; shift 2 ;;
	--ingress-tls-addr) INGRESS_TLS_ADDR="$2"; shift 2 ;;
	--dns-addr) DNS_ADDR="$2"; shift 2 ;;
	--data-dir) DATA_DIR="$2"; shift 2 ;;
	--admin-token) ADMIN_TOKEN="$2"; shift 2 ;;
	--namespace) NAMESPACE="$2"; shift 2 ;;
	--nerdctl) NERDCTL_OVERRIDE="$2"; shift 2 ;;
	--dry-run) DRY_RUN=true; shift ;;
	--skip-runtime-setup) SKIP_RUNTIME_SETUP=true; shift ;;
	-h | --help) usage; exit 0 ;;
	*) fail "unknown argument: $1 (see --help)" ;;
	esac
done

for c in tar sha256sum systemctl loginctl grep id uname; do
	command -v "$c" >/dev/null 2>&1 || fail "required command '$c' not found on PATH"
done

NERDCTL_BUNDLE="$(find "$SCRIPT_DIR" -maxdepth 1 -name 'nerdctl-full-linux-*.tar.gz' | head -1)"
if [ -n "$NERDCTL_BUNDLE" ]; then
	MODE=offline
else
	MODE=online
fi
log "mode: $MODE $([ "$MODE" = offline ] && echo "(bundle: $(basename "$NERDCTL_BUNDLE"))")"

# ── Phase 1: preflight (read-only) ──────────────────────────────────────────

check_kernel_version() {
	local kver min="5.11"
	kver="$(uname -r | grep -oE '^[0-9]+\.[0-9]+')"
	if [ "$(printf '%s\n%s\n' "$min" "$kver" | sort -V | head -1)" != "$min" ]; then
		fail "kernel $kver is older than the required $min (cgroup v2 + user namespace delegation)"
	fi
	log "kernel $kver OK"
}

check_cgroup_v2() {
	[ -f /sys/fs/cgroup/cgroup.controllers ] || fail "cgroup v2 not active (no /sys/fs/cgroup/cgroup.controllers). See docs/deploy.md §1."
	local slice_dir missing=""
	slice_dir="/sys/fs/cgroup/user.slice/user-$(id -u).slice"
	if [ ! -d "$slice_dir" ]; then
		fail "no cgroup slice at $slice_dir — user@$(id -u).service isn't running for this account. This usually means you reached this shell via 'sudo -u $USER' or 'su $USER' instead of a real login: those skip the PAM session that starts the systemd --user manager, so there's nothing for cgroup delegation to attach to. Fix by logging in as $USER directly (e.g. ssh), or ask an administrator to run once: 'sudo loginctl enable-linger $USER' (this also starts user@$(id -u).service immediately, independent of any session). Then re-run install.sh."
	fi
	local slice="$slice_dir/cgroup.controllers"
	if [ -r "$slice" ]; then
		for ctrl in cpu memory pids; do
			grep -qw "$ctrl" "$slice" || missing="$missing $ctrl"
		done
	else
		missing=" cpu memory pids"
	fi
	if [ -n "$missing" ]; then
		fail "cgroup controllers$missing are not delegated to your user slice ($slice). This is a one-time host change that requires root and is out of scope for this installer — it will not run sudo on your behalf. Ask an administrator to run once, on this host:
  sudo mkdir -p /etc/systemd/system/user@.service.d
  printf '[Service]\nDelegate=cpu cpuset io memory pids\n' | sudo tee /etc/systemd/system/user@.service.d/delegate.conf
  sudo systemctl daemon-reload
Then re-run install.sh."
	fi
	log "cgroup v2 delegation OK"
}

check_lingering() {
	local linger
	linger="$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo no)"
	if [ "$linger" != "yes" ]; then
		warn "lingering is not enabled for $USER, so orchestrator/proxyd will stop when you log out. This requires root and is out of scope for this installer. Ask an administrator to run once: 'sudo loginctl enable-linger $USER'."
	else
		log "lingering already enabled for $USER"
	fi
}

check_uidmap() {
	command -v newuidmap >/dev/null 2>&1 || fail \
		"newuidmap not found. Install it via your OS package manager (e.g. 'dnf install shadow-utils' on Fedora/RHEL, 'apt install uidmap' on Debian/Ubuntu) — this installer does not fetch packages."
	command -v newgidmap >/dev/null 2>&1 || fail "newgidmap not found (see newuidmap message above)"
	# `getent subuid` depends on an optional NSS module and can report "unknown
	# database" even when /etc/subuid has a valid entry, so read the files
	# directly instead.
	grep -q "^${USER}:" /etc/subuid 2>/dev/null || fail \
		"no /etc/subuid entry for $USER. As root: 'usermod --add-subuids 100000-165535 $USER'"
	grep -q "^${USER}:" /etc/subgid 2>/dev/null || fail \
		"no /etc/subgid entry for $USER. As root: 'usermod --add-subgids 100000-165535 $USER'"
	log "newuidmap/newgidmap + subuid/subgid OK"
}

check_kernel_version
check_cgroup_v2
check_uidmap
check_lingering

# ── Phase 2: runtime install ─────────────────────────────────────────────────

VENDOR_DIR="$DATA_DIR/vendor/nerdctl-full"
NERDCTL_PATH=""
CONTAINERD_ADDR="%t/containerd-rootless/containerd.sock"
RUNTIME_PATH_PREFIX=""

if $SKIP_RUNTIME_SETUP; then
	log "skipping runtime setup (--skip-runtime-setup)"
	NERDCTL_PATH="$(command -v nerdctl || true)"
	[ -n "$NERDCTL_PATH" ] || fail "nerdctl not found on PATH and --skip-runtime-setup was given"
	RUNTIME_PATH_PREFIX="$(dirname "$NERDCTL_PATH")"
elif [ "$MODE" = offline ]; then
	BUNDLE_SHA="$(sha256sum "$NERDCTL_BUNDLE" | awk '{print $1}')"
	MARKER="$VENDOR_DIR/.bundle-sha256"
	if [ -f "$MARKER" ] && [ "$(cat "$MARKER")" = "$BUNDLE_SHA" ]; then
		log "vendored nerdctl-full already extracted at $VENDOR_DIR (unchanged)"
	else
		log "extracting vendored nerdctl-full bundle to $VENDOR_DIR"
		run mkdir -p "$VENDOR_DIR"
		run tar -xzf "$NERDCTL_BUNDLE" -C "$VENDOR_DIR"
		$DRY_RUN || echo "$BUNDLE_SHA" >"$MARKER"
	fi

	CNI_DIR="$HOME/.local/lib/cni"
	run mkdir -p "$CNI_DIR"
	if $DRY_RUN; then
		log "[DRY-RUN] would link CNI plugins from $VENDOR_DIR/libexec/cni into $CNI_DIR"
	else
		for f in "$VENDOR_DIR"/libexec/cni/*; do
			case "$(basename "$f")" in LICENSE | README.md) continue ;; esac
			ln -sf "$f" "$CNI_DIR/$(basename "$f")"
		done
		log "CNI plugins linked into $CNI_DIR (nerdctl's default --cni-path)"
	fi

	VENDOR_BIN="$VENDOR_DIR/bin"
	NERDCTL_PATH="$VENDOR_BIN/nerdctl"
	RUNTIME_PATH_PREFIX="$VENDOR_BIN"

	# containerd's CRI plugin defaults to an NRI socket at /var/run/nri/nri.sock,
	# which is root's /run and isn't writable under rootless — this makes CRI
	# (and so containerd startup) fail with "bind: permission denied". NRI has no
	# use here (no Kubernetes/kubelet involved), so disable it outright rather
	# than try to relocate the socket under XDG_RUNTIME_DIR.
	CONTAINERD_CONFIG="$HOME/.config/containerd/config.toml"
	run mkdir -p "$(dirname "$CONTAINERD_CONFIG")"
	if $DRY_RUN; then
		log "[DRY-RUN] would ensure NRI is disabled in $CONTAINERD_CONFIG"
	elif [ -f "$CONTAINERD_CONFIG" ] && grep -qF '[plugins."io.containerd.nri.v1.nri"]' "$CONTAINERD_CONFIG"; then
		log "containerd config already has an NRI section at $CONTAINERD_CONFIG (leaving as-is)"
	else
		printf '\n[plugins."io.containerd.nri.v1.nri"]\n  disable = true\n' >>"$CONTAINERD_CONFIG"
		log "disabled NRI in $CONTAINERD_CONFIG"
	fi

	# containerd-rootless-setuptool.sh generates containerd.service without a PATH
	# override, but its ExecStart (containerd-rootless.sh) execs a bare
	# "rootlesskit" by name — that only resolves if VENDOR_BIN is on PATH, which
	# it isn't under the user manager's default environment. Pin it via drop-in
	# rather than relying on ambient shell PATH (absent for linger-only accounts
	# with no real login session).
	CONTAINERD_DROPIN_DIR="$HOME/.config/systemd/user/containerd.service.d"
	run mkdir -p "$CONTAINERD_DROPIN_DIR"
	if $DRY_RUN; then
		log "[DRY-RUN] would write $CONTAINERD_DROPIN_DIR/path-override.conf (PATH=$VENDOR_BIN:...)"
	else
		cat >"$CONTAINERD_DROPIN_DIR/path-override.conf" <<EOF
[Service]
Environment=PATH=$VENDOR_BIN:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
EOF
	fi
	run systemctl --user daemon-reload

	if systemctl --user is-active containerd.service >/dev/null 2>&1; then
		log "containerd.service already running"
	else
		log "running vendored containerd-rootless-setuptool.sh install"
		if $DRY_RUN; then
			log "[DRY-RUN] would run: PATH=$VENDOR_BIN:/usr/sbin:/sbin:/usr/bin:/bin $VENDOR_BIN/containerd-rootless-setuptool.sh install"
		else
			PATH="$VENDOR_BIN:/usr/sbin:/sbin:/usr/bin:/bin" "$VENDOR_BIN/containerd-rootless-setuptool.sh" install
		fi
	fi
else
	NERDCTL_PATH="$(command -v nerdctl || true)"
	[ -n "$NERDCTL_PATH" ] || fail "nerdctl not found on PATH. Install rootless nerdctl per docs/deploy.md §1, or run this script next to a nerdctl-full-linux-*.tar.gz bundle for offline mode."
	RUNTIME_PATH_PREFIX="$(dirname "$NERDCTL_PATH")"
	"$NERDCTL_PATH" info >/dev/null 2>&1 || fail "nerdctl found at $NERDCTL_PATH but 'nerdctl info' failed — is rootless containerd running?"
	log "using existing nerdctl at $NERDCTL_PATH"
fi

[ -n "$NERDCTL_OVERRIDE" ] && { NERDCTL_PATH="$NERDCTL_OVERRIDE"; RUNTIME_PATH_PREFIX="$(dirname "$NERDCTL_OVERRIDE")"; }

# ── Phase 3: install orchestrator binaries ──────────────────────────────────

mkdir -p "$HOME/.local/bin" "$DATA_DIR"
for bin in orchestrator ctl proxyd; do
	[ -f "$SCRIPT_DIR/$bin" ] || fail "$bin binary not found next to install.sh"
	run install -m755 "$SCRIPT_DIR/$bin" "$HOME/.local/bin/$bin"
done
log "installed orchestrator, ctl, proxyd to ~/.local/bin"

# ── Phase 4: systemd --user units ───────────────────────────────────────────

TOPOLOGY_FLAGS=""
if $BOOTSTRAP; then
	TOPOLOGY_FLAGS="--bootstrap"
elif [ -n "$JOIN" ]; then
	TOPOLOGY_FLAGS="--join $JOIN"
	[ -n "$JOIN_TOKEN" ] && TOPOLOGY_FLAGS="$TOPOLOGY_FLAGS --join-token $JOIN_TOKEN"
fi
[ -n "$ADMIN_TOKEN" ] && TOPOLOGY_FLAGS="$TOPOLOGY_FLAGS --admin-token $ADMIN_TOKEN"

render_orchestrator_unit() {
	cat <<EOF
[Unit]
Description=Container Orchestrator Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=PATH=$RUNTIME_PATH_PREFIX:/usr/sbin:/sbin:/usr/bin:/bin

ExecStart=$HOME/.local/bin/orchestrator \\
  --nerdctl $NERDCTL_PATH \\
  --containerd-addr $CONTAINERD_ADDR \\
  --namespace $NAMESPACE \\
  --node-id $NODE_ID \\
  --grpc-addr $GRPC_ADDR \\
  --raft-addr $RAFT_ADDR \\
  --data-dir $DATA_DIR \\
  --web-addr $WEB_ADDR \\
  --ingress-addr $INGRESS_ADDR \\
  --ingress-tls-addr $INGRESS_TLS_ADDR \\
  --dns-addr $DNS_ADDR $TOPOLOGY_FLAGS

Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s

StandardOutput=journal
StandardError=journal
SyslogIdentifier=orchestrator

NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=default.target
EOF
}

render_proxyd_unit() {
	cat <<EOF
[Unit]
Description=Service Proxy and DNS (svc.local)
After=network-online.target orchestrator.service
Requires=orchestrator.service
Wants=network-online.target

[Service]
Type=simple

ExecStart=$HOME/.local/bin/proxyd \\
  --config $DATA_DIR/proxy/config.json \\
  --node-id $NODE_ID

Restart=on-failure
RestartSec=3s
TimeoutStopSec=15s

StandardOutput=journal
StandardError=journal
SyslogIdentifier=proxyd

NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=default.target
EOF
}

install_unit_if_changed() {
	local name="$1" content="$2" path="$HOME/.config/systemd/user/$1"
	mkdir -p "$(dirname "$path")"
	if [ -f "$path" ] && [ "$(cat "$path")" = "$content" ]; then
		log "$name unchanged"
		return 1
	fi
	if $DRY_RUN; then
		log "[DRY-RUN] would write $path"
		return 0
	fi
	printf '%s\n' "$content" >"$path"
	log "wrote $path"
	return 0
}

CHANGED=false
if install_unit_if_changed orchestrator.service "$(render_orchestrator_unit)"; then CHANGED=true; fi
if install_unit_if_changed proxyd.service "$(render_proxyd_unit)"; then CHANGED=true; fi

if $CHANGED || ! systemctl --user is-enabled orchestrator.service >/dev/null 2>&1; then
	run systemctl --user daemon-reload
fi
run systemctl --user enable --now orchestrator.service
run systemctl --user enable --now proxyd.service

# ── Phase 5: summary ─────────────────────────────────────────────────────────

if $DRY_RUN; then
	log "dry run complete — no changes were made"
	exit 0
fi

DETECTED_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
GRPC_PORT="${GRPC_ADDR##*:}"
WEB_PORT="${WEB_ADDR##*:}"

cat <<EOF

==> Orchestrator installed and started.

Node ID:       $NODE_ID
Data dir:      $DATA_DIR
Admin token:   $DATA_DIR/admin-token
Join token:    $DATA_DIR/join-token   (share with operators adding nodes)
Web console:   http://${DETECTED_IP:-<this-node-ip>}:${WEB_PORT}   (user: admin, password in $DATA_DIR/web-password)

Check status:
  export ORCHESTRATOR_TOKEN=\$(cat $DATA_DIR/admin-token)
  $HOME/.local/bin/ctl --server ${DETECTED_IP:-<this-node-ip>}:${GRPC_PORT} status

To add another node, on that machine run:
  ./install.sh --node-id nodeN --grpc-addr <nodeN-ip>:${GRPC_PORT} --raft-addr <nodeN-ip>:${RAFT_ADDR##*:} \\
    --join ${DETECTED_IP:-<this-node-ip>}:${GRPC_PORT} --join-token \$(cat $DATA_DIR/join-token)

Logs:
  journalctl --user -u orchestrator -f
  journalctl --user -u proxyd -f
EOF
