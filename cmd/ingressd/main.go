package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eghansah/orchestrator/internal/ingresscfg"
)

func main() {
	var configPath string
	var haproxyBin string
	var haproxyCfg string
	var certsDir string
	var httpAddr string
	var httpsAddr string
	var proxyProtocol bool

	flag.StringVar(&configPath, "config", "", "path to ingress config.json (required)")
	flag.StringVar(&haproxyBin, "haproxy-bin", "haproxy", "path to haproxy binary")
	flag.StringVar(&haproxyCfg, "haproxy-cfg", "", "path to write generated haproxy.cfg (default: <config-dir>/haproxy.cfg)")
	flag.StringVar(&certsDir, "certs-dir", "", "directory for per-domain PEM files (default: <config-dir>/certs)")
	flag.StringVar(&httpAddr, "http-addr", ":80", "HAProxy HTTP bind address")
	flag.StringVar(&httpsAddr, "https-addr", ":443", "HAProxy HTTPS bind address (empty = disabled)")
	flag.BoolVar(&proxyProtocol, "proxy-protocol", false, "accept PROXY protocol headers from upstream (e.g. host HAProxy); preserves real client IP")
	flag.Parse()

	if configPath == "" {
		fmt.Fprintln(os.Stderr, "usage: ingressd --config <path>")
		os.Exit(1)
	}

	configDir := filepath.Dir(configPath)
	if haproxyCfg == "" {
		haproxyCfg = filepath.Join(configDir, "haproxy.cfg")
	}
	if certsDir == "" {
		certsDir = filepath.Join(configDir, "certs")
	}

	d := &daemon{
		configPath:    configPath,
		haproxyBin:    haproxyBin,
		haproxyCfg:    haproxyCfg,
		certsDir:      certsDir,
		httpAddr:      httpAddr,
		httpsAddr:     httpsAddr,
		proxyProtocol: proxyProtocol,
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		slog.Warn("initial config load failed, waiting for file", "path", configPath, "err", err)
	} else {
		d.apply(cfg)
	}

	go d.watchConfig()

	// Forward SIGTERM/SIGINT to the haproxy child so it can drain connections.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	d.mu.Lock()
	child := d.haproxyCmd
	d.mu.Unlock()
	if child != nil && child.Process != nil {
		_ = child.Process.Signal(sig)
		_ = child.Wait()
	}
}

type daemon struct {
	configPath    string
	haproxyBin    string
	haproxyCfg    string
	certsDir      string
	httpAddr      string
	httpsAddr     string
	proxyProtocol bool // accept PROXY protocol v1/v2 headers from upstream

	mu         sync.Mutex
	haproxyCmd *exec.Cmd // current running haproxy child; nil if not started
	lastCfgSum [32]byte  // SHA-256 of the last written haproxy.cfg
}

// watchConfig polls the config file's mtime/size and reloads when it changes.
//
// We deliberately poll rather than use inotify/fsnotify: ingressd runs in a
// container with the host's ingress directory bind-mounted in, and inotify
// events do not cross that rootless bind-mount boundary, so a watch here never
// fires for orchestrator-side writes. os.Stat, by contrast, reads the real file
// metadata through the mount and reliably observes host writes — including the
// tmp + rename the orchestrator uses, which replaces the inode but updates mtime.
func (d *daemon) watchConfig() {
	const interval = 2 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastMod time.Time
	var lastSize int64 = -1
	for range ticker.C {
		info, err := os.Stat(d.configPath)
		if err != nil {
			continue
		}
		// Reload on any mtime or size change. apply() dedupes by hashing the
		// generated haproxy.cfg, so a redundant reload here is a cheap no-op;
		// tracking size as well guards against filesystems with coarse mtime
		// granularity where two distinct writes could share a timestamp.
		if info.ModTime().Equal(lastMod) && info.Size() == lastSize {
			continue
		}
		lastMod = info.ModTime()
		lastSize = info.Size()
		d.reload()
	}
}

func (d *daemon) reload() {
	cfg, err := loadConfig(d.configPath)
	if err != nil {
		slog.Warn("config reload failed", "err", err)
		return
	}
	d.apply(cfg)
}

// apply writes TLS cert files, generates haproxy.cfg, and (re)starts HAProxy.
func (d *daemon) apply(cfg ingresscfg.Config) {
	if err := os.MkdirAll(d.certsDir, 0o700); err != nil {
		slog.Error("cannot create certs dir", "dir", d.certsDir, "err", err)
		return
	}

	// Write per-domain cert files; collect the set of active IDs.
	activeIDs := make(map[string]bool)
	for _, b := range cfg.Backends {
		if b.TLSCert == "" || b.TLSKey == "" {
			continue
		}
		pemPath := filepath.Join(d.certsDir, b.ID+".pem")
		content := b.TLSCert
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += b.TLSKey
		if err := os.WriteFile(pemPath, []byte(content), 0o600); err != nil {
			slog.Warn("failed to write cert", "id", b.ID, "err", err)
			continue
		}
		activeIDs[b.ID] = true
	}

	// Remove stale cert files.
	entries, _ := os.ReadDir(d.certsDir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".pem") {
			continue
		}
		id := strings.TrimSuffix(name, ".pem")
		if !activeIDs[id] {
			_ = os.Remove(filepath.Join(d.certsDir, name))
		}
	}

	// Generate haproxy.cfg.
	cfgContent := d.generateHAProxyCfg(cfg)
	newSum := sha256.Sum256([]byte(cfgContent))

	d.mu.Lock()
	unchanged := newSum == d.lastCfgSum
	d.mu.Unlock()

	if unchanged {
		slog.Debug("haproxy.cfg unchanged, skipping reload")
		return
	}

	tmpCfg := d.haproxyCfg + ".tmp"
	if err := os.WriteFile(tmpCfg, []byte(cfgContent), 0o600); err != nil {
		slog.Error("failed to write haproxy.cfg", "err", err)
		return
	}
	if err := os.Rename(tmpCfg, d.haproxyCfg); err != nil {
		slog.Error("failed to rename haproxy.cfg", "err", err)
		return
	}

	if err := d.reloadHAProxy(); err != nil {
		slog.Error("haproxy reload failed", "err", err)
		return
	}

	d.mu.Lock()
	d.lastCfgSum = newSum
	d.mu.Unlock()

	slog.Info("haproxy config applied", "backends", len(cfg.Backends))
}

// reloadHAProxy starts or gracefully reloads the HAProxy child process.
func (d *daemon) reloadHAProxy() error {
	d.mu.Lock()
	oldCmd := d.haproxyCmd
	d.mu.Unlock()

	args := []string{"-f", d.haproxyCfg}

	if oldCmd != nil && oldCmd.Process != nil {
		oldPid := oldCmd.Process.Pid
		args = append(args, "-sf", fmt.Sprintf("%d", oldPid))
	}

	newCmd := exec.Command(d.haproxyBin, args...)
	newCmd.Stdout = os.Stdout
	newCmd.Stderr = os.Stderr

	if err := newCmd.Start(); err != nil {
		return fmt.Errorf("start haproxy: %w", err)
	}

	d.mu.Lock()
	d.haproxyCmd = newCmd
	d.mu.Unlock()

	// Reap the old child in the background once it finishes draining.
	if oldCmd != nil {
		go func() { _ = oldCmd.Wait() }()
	}
	// Watch the new child for unexpected exits.
	go func() {
		if err := newCmd.Wait(); err != nil {
			slog.Warn("haproxy exited unexpectedly", "err", err)
		}
	}()

	slog.Info("haproxy started/reloaded", "pid", newCmd.Process.Pid)
	return nil
}

// generateHAProxyCfg builds the haproxy configuration as a string.
func (d *daemon) generateHAProxyCfg(cfg ingresscfg.Config) string {
	var sb strings.Builder

	sb.WriteString("global\n")
	sb.WriteString("    log stdout format raw local0\n")
	sb.WriteString("    maxconn 4096\n")
	sb.WriteString("\n")

	sb.WriteString("defaults\n")
	sb.WriteString("    mode http\n")
	sb.WriteString("    timeout connect 5s\n")
	sb.WriteString("    timeout client  30s\n")
	sb.WriteString("    timeout server  30s\n")
	sb.WriteString("    log global\n")
	sb.WriteString("    option httplog\n")
	sb.WriteString("    option forwardfor\n")
	sb.WriteString("\n")

	// Sort backends longest-prefix-first so use_backend rules match most-specific first.
	sorted := make([]ingresscfg.Backend, len(cfg.Backends))
	copy(sorted, cfg.Backends)
	sort.Slice(sorted, func(i, j int) bool {
		pi := sorted[i].PathPrefix
		pj := sorted[j].PathPrefix
		if pi == "" {
			pi = "/"
		}
		if pj == "" {
			pj = "/"
		}
		return len(pi) > len(pj)
	})

	// Find a catch-all backend (no host, path == "/" or empty) for use as default_backend.
	defaultID := ""
	var routed []ingresscfg.Backend
	for _, b := range sorted {
		prefix := b.PathPrefix
		if prefix == "" {
			prefix = "/"
		}
		if b.Host == "" && prefix == "/" && defaultID == "" {
			defaultID = b.ID
		} else {
			routed = append(routed, b)
		}
	}

	hasTLS := false
	for _, b := range cfg.Backends {
		if b.TLSCert != "" && b.TLSKey != "" {
			hasTLS = true
			break
		}
	}

	proxyOpt := ""
	if d.proxyProtocol {
		proxyOpt = " accept-proxy"
	}

	writeFrontend := func(name, bind string, tls bool) {
		fmt.Fprintf(&sb, "frontend %s\n", name)
		if tls {
			fmt.Fprintf(&sb, "    bind %s%s ssl crt %s/\n", bind, proxyOpt, d.certsDir)
		} else {
			fmt.Fprintf(&sb, "    bind %s%s\n", bind, proxyOpt)
		}
		for _, b := range routed {
			id := safeID(b.ID)
			if b.Host != "" {
				fmt.Fprintf(&sb, "    acl h_%s hdr(host) -i %s\n", id, b.Host)
			}
			prefix := b.PathPrefix
			if prefix == "" {
				prefix = "/"
			}
			if prefix != "/" {
				fmt.Fprintf(&sb, "    acl p_%s path_beg %s\n", id, prefix)
			}
		}
		for _, b := range routed {
			id := safeID(b.ID)
			var cond string
			hasHost := b.Host != ""
			prefix := b.PathPrefix
			if prefix == "" {
				prefix = "/"
			}
			hasPath := prefix != "/"
			switch {
			case hasHost && hasPath:
				cond = fmt.Sprintf("h_%s p_%s", id, id)
			case hasHost:
				cond = fmt.Sprintf("h_%s", id)
			case hasPath:
				cond = fmt.Sprintf("p_%s", id)
			default:
				cond = ""
			}
			if cond != "" {
				fmt.Fprintf(&sb, "    use_backend be_%s if %s\n", id, cond)
			}
		}
		if defaultID != "" {
			fmt.Fprintf(&sb, "    default_backend be_%s\n", safeID(defaultID))
		} else {
			sb.WriteString("    default_backend be_default\n")
		}
		sb.WriteString("\n")
	}

	writeFrontend("fe_http", d.httpAddr, false)

	if d.httpsAddr != "" && hasTLS {
		writeFrontend("fe_https", d.httpsAddr, true)
	}

	// Per-rule backends.
	for _, b := range cfg.Backends {
		id := safeID(b.ID)
		fmt.Fprintf(&sb, "backend be_%s\n", id)
		prefix := b.PathPrefix
		if prefix == "" {
			prefix = "/"
		}
		if prefix != "/" && b.StripPrefix {
			// Strip the prefix before forwarding so the app receives requests at its own root.
			// ^/prefix/?(.*)$ → /\1 handles /prefix, /prefix/, and /prefix/anything.
			fmt.Fprintf(&sb, "    http-request replace-path ^%s/?(.*)$ /\\1\n", rePathEscape(prefix))
		}
		fmt.Fprintf(&sb, "    server primary %s:%d check\n", b.NodeDataIP, b.SystemPort)
		sb.WriteString("\n")
	}

	// Fallback backend.
	sb.WriteString("backend be_default\n")
	sb.WriteString("    http-request deny deny_status 502\n")
	sb.WriteString("\n")

	return sb.String()
}

// rePathEscape escapes regex metacharacters in a path prefix for use in HAProxy
// replace-path patterns. Path segments rarely contain these but we escape to be safe.
func rePathEscape(s string) string {
	var sb strings.Builder
	for _, c := range s {
		switch c {
		case '.', '+', '?', '*', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			sb.WriteRune('\\')
		}
		sb.WriteRune(c)
	}
	return sb.String()
}

// safeID converts an arbitrary string into a token safe for HAProxy names.
func safeID(id string) string {
	var sb strings.Builder
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			sb.WriteRune(c)
		} else {
			sb.WriteRune('_')
		}
	}
	return sb.String()
}

func loadConfig(path string) (ingresscfg.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ingresscfg.Config{}, err
	}
	var cfg ingresscfg.Config
	return cfg, json.Unmarshal(data, &cfg)
}
