package nerdctl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/eghansah/orchestrator/pkg/types"
)

const maxComposeYAMLSize = 1 << 20 // 1 MiB

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.+-]*$`)

// validateName rejects names that would cause path traversal (e.g. "../foo")
// or argument injection (e.g. "--flag") when passed to nerdctl.
func validateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid name %q: must start with [a-zA-Z0-9] and contain only [a-zA-Z0-9_.+-]", name)
	}
	return nil
}

// isSpecDescriptor returns true when the YAML looks like the workload spec
// descriptor format produced by the UI's Definition panel (top-level "kind:"
// and "compose_yaml:" keys) rather than a raw Docker Compose file.
func isSpecDescriptor(yaml string) bool {
	hasKind := strings.Contains(yaml, "\nkind:") || strings.HasPrefix(yaml, "kind:")
	hasWrapper := strings.Contains(yaml, "\ncompose_yaml:") || strings.HasPrefix(yaml, "compose_yaml:")
	return hasKind && hasWrapper
}

const (
	workloadIDLabel  = "orchestrator.workload-id"
	defaultNamespace = "orchestrator"
)

type Client struct {
	binary    string
	namespace string
	address   string // containerd socket path, passed as --address to nerdctl
	dataDir   string // root for compose file storage
	dnsIP     string // injected into containers for svc.local resolution
	dnsPort   uint32 // DNS port; if != 53, adds resolv.conf "options port:N"

	mu          sync.Mutex
	meshNetwork string // when set, managed containers attach to this nerdctl network for a mesh IP
}

// SetMeshNetwork enables (or, with "", disables) attaching managed containers to
// the named mesh network. Called once the leader has assigned this node a mesh
// subnet and the network has been created. Safe for concurrent use.
func (c *Client) SetMeshNetwork(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.meshNetwork = name
}

// meshNet returns the currently configured mesh network name (or "").
func (c *Client) meshNet() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.meshNetwork
}

// detectContainerdSocket returns the first containerd socket that exists,
// trying both common rootless layouts under XDG_RUNTIME_DIR and the
// /run/user/<uid> fallback for environments where XDG_RUNTIME_DIR is unset.
func detectContainerdSocket() string {
	dirs := []string{os.Getenv("XDG_RUNTIME_DIR")}
	if dirs[0] == "" {
		dirs[0] = fmt.Sprintf("/run/user/%d", os.Getuid())
	} else {
		dirs = append(dirs, fmt.Sprintf("/run/user/%d", os.Getuid()))
	}
	suffixes := []string{
		filepath.Join("containerd-rootless", "containerd.sock"),
		filepath.Join("containerd", "containerd.sock"),
	}
	for _, d := range dirs {
		for _, s := range suffixes {
			p := filepath.Join(d, s)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// NewClient creates a nerdctl client. address is the containerd socket path;
// pass an empty string to auto-detect from XDG_RUNTIME_DIR.
func NewClient(binary, namespace, address, dataDir, dnsIP string, dnsPort uint32) (*Client, error) {
	if binary == "" {
		binary = "nerdctl"
	}
	if namespace == "" {
		namespace = defaultNamespace
	}
	if address == "" {
		address = detectContainerdSocket()
	}
	return &Client{binary: binary, namespace: namespace, address: address, dataDir: dataDir, dnsIP: dnsIP, dnsPort: dnsPort}, nil
}

// Address returns the containerd socket path being used (empty = nerdctl default).
func (c *Client) Address() string { return c.address }

// DataDir returns the root directory used for compose file storage.
func (c *Client) DataDir() string { return c.dataDir }

// statsLine matches the per-line JSON from `nerdctl stats --no-stream --format '{{json .}}'`.
type statsLine struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`  // e.g. "0.50%"
	MemUsage string `json:"MemUsage"` // e.g. "10.5MiB / 7.7GiB"
}

// parseHumanBytes converts nerdctl human-readable byte strings to uint64.
// Supports B, KiB, MiB, GiB, TiB, kB, MB, GB, TB (case-insensitive).
func parseHumanBytes(s string) uint64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		factor uint64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1_000_000_000_000}, {"GB", 1_000_000_000}, {"MB", 1_000_000}, {"kB", 1_000},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			num := strings.TrimSuffix(s, u.suffix)
			if f, err := strconv.ParseFloat(strings.TrimSpace(num), 64); err == nil {
				return uint64(f * float64(u.factor))
			}
		}
	}
	return 0
}

// Stats returns per-container resource usage for all running containers in the namespace.
// Best-effort: returns an empty slice rather than an error when nerdctl stats fails or
// there are no running containers.
func (c *Client) Stats(ctx context.Context) ([]types.ContainerStats, error) {
	out, err := c.run(ctx, "stats", "--no-stream", "--format", "{{json .}}")
	if err != nil {
		return nil, nil // no containers running or nerdctl stats not supported
	}
	var result []types.ContainerStats
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var sl statsLine
		if err := json.Unmarshal([]byte(line), &sl); err != nil {
			continue
		}
		cpuStr := strings.TrimSuffix(strings.TrimSpace(sl.CPUPerc), "%")
		cpu, _ := strconv.ParseFloat(cpuStr, 64)

		var memUsed, memLimit uint64
		if parts := strings.SplitN(sl.MemUsage, "/", 2); len(parts) == 2 {
			memUsed = parseHumanBytes(parts[0])
			memLimit = parseHumanBytes(parts[1])
		}
		result = append(result, types.ContainerStats{
			ContainerID:   sl.ID,
			Name:          sl.Name,
			CPUPercent:    cpu,
			MemUsedBytes:  memUsed,
			MemLimitBytes: memLimit,
		})
	}
	return result, scanner.Err()
}

// CollectNodeMetrics reads node-level memory from /proc/meminfo and disk usage
// from syscall.Statfs on the given directory. Best-effort; returns zero values on error.
func CollectNodeMetrics(dataDir string) types.NodeMetrics {
	var m types.NodeMetrics

	// Memory from /proc/meminfo
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, available uint64
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			val, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				continue
			}
			switch fields[0] {
			case "MemTotal:":
				total = val * 1024
			case "MemAvailable:":
				available = val * 1024
			}
		}
		if total > 0 {
			m.MemTotalBytes = total
			m.MemUsedBytes = total - available
		}
	}

	// Disk from syscall.Statfs on data directory
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &stat); err == nil {
		m.DiskTotalBytes = stat.Blocks * uint64(stat.Bsize)
		m.DiskUsedBytes = (stat.Blocks - stat.Bfree) * uint64(stat.Bsize)
	}

	return m
}

// Probe verifies that nerdctl can reach the containerd daemon.
func (c *Client) Probe(ctx context.Context) error {
	out, err := c.run(ctx, "info")
	if err != nil {
		return fmt.Errorf("nerdctl not available: %w", err)
	}
	if !bytes.Contains(out, []byte("Server")) {
		return fmt.Errorf("unexpected nerdctl info output: %s", out)
	}
	return nil
}

// run executes nerdctl with --namespace (and --address when set) as global flags.
// CONTAINERD_ADDRESS is also injected as an env var so that nerdctl's rootless
// pre-flight check uses the right socket instead of the hardcoded containerd-rootless path.
// stderr is merged into the error message verbatim.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	return c.runInsecure(ctx, false, args...)
}

// runInsecure is like run but, when insecure is true, passes --insecure-registry
// ahead of the subcommand so nerdctl will pull from plain-HTTP or self-signed
// registries. That flag is global to nerdctl and must precede the subcommand.
func (c *Client) runInsecure(ctx context.Context, insecure bool, args ...string) ([]byte, error) {
	global := []string{"--namespace", c.namespace}
	if c.address != "" {
		global = append(global, "--address", c.address)
	}
	if insecure {
		global = append(global, "--insecure-registry")
	}
	full := append(global, args...)
	cmd := exec.CommandContext(ctx, c.binary, full...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if c.address != "" {
		cmd.Env = append(os.Environ(), "CONTAINERD_ADDRESS="+c.address)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// runStdout is like run but captures stdout and stderr separately so that
// nerdctl log lines written to stderr do not corrupt parseable stdout output.
func (c *Client) runStdout(ctx context.Context, args ...string) ([]byte, error) {
	global := []string{"--namespace", c.namespace}
	if c.address != "" {
		global = append(global, "--address", c.address)
	}
	full := append(global, args...)
	cmd := exec.CommandContext(ctx, c.binary, full...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if c.address != "" {
		cmd.Env = append(os.Environ(), "CONTAINERD_ADDRESS="+c.address)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (c *Client) Pull(ctx context.Context, image string, insecure bool) error {
	_, err := c.runInsecure(ctx, insecure || isLocalhostImage(image), "pull", image)
	return err
}

// isLocalhostImage reports whether the image reference points at a loopback
// registry (127.0.0.1:* or localhost:*). Such registries require --insecure-registry
// because they are served over plain HTTP without TLS.
func isLocalhostImage(image string) bool {
	// Strip tag or digest.
	ref := image
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		// only strip the port/tag part if there's a '/' before it (i.e. it's a host:port, not name:tag)
		if strings.ContainsRune(ref[:i], '/') || strings.ContainsRune(ref[:i], ':') || isIPOrLocalhost(ref[:i]) {
			ref = ref[:i]
		}
	}
	return isIPOrLocalhost(ref)
}

func isIPOrLocalhost(host string) bool {
	return host == "localhost" ||
		strings.HasPrefix(host, "127.") ||
		host == "::1"
}

// RunContainer starts a detached container from the given spec, tagged with workloadID.
// portAllocations maps container ports to auto-assigned host ports, always bound on
// 127.0.0.1 so containers are only reachable via the ingress proxy.
func (c *Client) RunContainer(ctx context.Context, workloadID string, spec types.ContainerSpec, portAllocations []types.PortAllocation) error {
	if err := validateName(spec.Name); err != nil {
		return err
	}

	// Replace any existing container so that port-binding changes (e.g. a loopback
	// port auto-allocated after initial deployment) take effect. Errors here are
	// expected when no prior container exists and are intentionally ignored.
	_, _ = c.run(ctx, "stop", "--", spec.Name)
	_, _ = c.run(ctx, "rm", "--", spec.Name)

	args := buildRunArgs(workloadID, spec, portAllocations, c.dnsIP, c.dnsPort, c.meshNet())
	insecure := spec.InsecureRegistry || isLocalhostImage(spec.Image)

	slog.Info("nerdctl: running container", "cmd", append([]string{c.binary}, args...))
	_, err := c.runInsecure(ctx, insecure, args...)
	if err != nil {
		slog.Error("nerdctl: run container failed", "name", spec.Name, "err", err)
	}
	return err
}

// buildRunArgs assembles the `nerdctl run` argument list for a managed container.
// It is pure (no exec) so the command line can be unit-tested. When meshNetwork
// is non-empty the container is additionally attached to that network so it
// receives a mesh IP from the node's /24, on top of the existing loopback port
// publishing used by the proxy. See docs/mesh-network.md.
func buildRunArgs(workloadID string, spec types.ContainerSpec, portAllocations []types.PortAllocation, dnsIP string, dnsPort uint32, meshNetwork string) []string {
	args := []string{"run", "-d", "--name", spec.Name}
	if meshNetwork != "" {
		args = append(args, "--network", meshNetwork)
	}
	for _, env := range spec.Env {
		args = append(args, "-e", env)
	}
	// Use portAllocations as the authoritative source for -p flags. This covers
	// both ports declared in spec.Ports (assigned by the FSM on workload submit)
	// and ports auto-allocated later by ensurePortAllocated when a Service or
	// IngressRule references a port not originally declared in the workload spec.
	for _, pa := range portAllocations {
		if pa.AllocatedPort == 0 {
			continue
		}
		proto := pa.Protocol
		if proto == "" {
			proto = "tcp"
		}
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d/%s", pa.AllocatedPort, pa.ContainerPort, proto))
	}
	for _, v := range spec.Volumes {
		mount := fmt.Sprintf("%s:%s", v.Source, v.Target)
		if v.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}
	for k, v := range spec.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, "--label", fmt.Sprintf("%s=%s", workloadIDLabel, workloadID))
	if dnsIP != "" {
		args = append(args, "--dns", dnsIP, "--dns-search", "svc.local")
		if dnsPort != 0 && dnsPort != 53 {
			args = append(args, "--dns-opt", fmt.Sprintf("port:%d", dnsPort))
		}
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	return args
}

func (c *Client) ContainerLogs(ctx context.Context, name string, tail int) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	out, err := c.run(ctx, "logs", "--tail", strconv.Itoa(tail), "--", name)
	return string(out), err
}

func (c *Client) StopContainer(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	_, err := c.run(ctx, "stop", "--", name)
	return err
}

func (c *Client) StartContainer(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	_, err := c.run(ctx, "start", "--", name)
	return err
}

func (c *Client) RemoveContainer(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	// Stop first so rootlesskit tears down port-forwarding before the container
	// is deleted. rm -f would SIGKILL the container process and return before
	// the network namespace is fully released, leaving the host port held.
	_, _ = c.run(ctx, "stop", "--", name)
	_, err := c.run(ctx, "rm", "--", name)
	return err
}

// containerInfo matches the per-line JSON from `nerdctl ps --format '{{json .}}'`.
type containerInfo struct {
	ID        string `json:"ID"`
	Names     string `json:"Names"`
	Image     string `json:"Image"`
	Status    string `json:"Status"`
	Labels    string `json:"Labels"` // comma-separated key=value
	CreatedAt string `json:"CreatedAt"`
}

func (ci containerInfo) workloadID() string {
	for _, kv := range strings.Split(ci.Labels, ",") {
		parts := strings.SplitN(strings.TrimSpace(kv), "=", 2)
		if len(parts) == 2 && parts[0] == workloadIDLabel {
			return parts[1]
		}
	}
	return ""
}

// ListContainers returns all containers in the orchestrator namespace.
// For containers that are running and attached to the mesh network, the mesh IP
// is populated via a supplementary inspect call.
func (c *Client) ListContainers(ctx context.Context) ([]types.ActualContainer, error) {
	out, err := c.run(ctx, "ps", "-a", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var result []types.ActualContainer
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ci containerInfo
		if err := json.Unmarshal([]byte(line), &ci); err != nil {
			continue // skip malformed lines
		}
		result = append(result, types.ActualContainer{
			WorkloadID:  ci.workloadID(),
			ContainerID: ci.ID,
			Name:        ci.Names,
			Status:      ci.Status,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Supplement with mesh IPs for running containers when the mesh network is active.
	meshNet := c.meshNet()
	if meshNet != "" && len(result) > 0 {
		meshIPs := c.containerMeshIPs(ctx, result, meshNet)
		for i := range result {
			result[i].MeshIP = meshIPs[result[i].Name]
		}
	}
	return result, nil
}

// containerMeshIPs calls nerdctl inspect for running containers and returns a
// map from container name to the IP assigned on the given network.
func (c *Client) containerMeshIPs(ctx context.Context, containers []types.ActualContainer, netName string) map[string]string {
	var names []string
	for _, ac := range containers {
		if strings.HasPrefix(strings.ToLower(ac.Status), "up") {
			names = append(names, ac.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}

	args := append([]string{"inspect", "--type=container"}, names...)
	out, err := c.runStdout(ctx, args...)
	if err != nil {
		return nil
	}
	var results []ContainerInspectResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &results); err != nil {
		return nil
	}
	m := make(map[string]string, len(results))
	for _, r := range results {
		if nets := r.NetworkSettings.Networks; nets != nil {
			if n, ok := nets[netName]; ok && n.IPAddress != "" {
				m[r.Name] = n.IPAddress
			}
		}
	}
	return m
}

func (c *Client) composeDir(stackName string) (string, error) {
	if err := validateName(stackName); err != nil {
		return "", err
	}
	return filepath.Join(c.dataDir, "stacks", stackName), nil
}

// ComposeUp writes the compose YAML and runs nerdctl compose up -d.
// If spec.ResolvedEnv is non-empty (secrets resolved at placement time), a
// .env file is written alongside the compose file so nerdctl compose picks
// them up automatically.
func (c *Client) ComposeUp(ctx context.Context, _ string, spec types.ComposeStackSpec, portAllocations []types.PortAllocation) error {
	if err := validateName(spec.Name); err != nil {
		return err
	}
	if len(spec.ComposeYAML) > maxComposeYAMLSize {
		return fmt.Errorf("compose YAML exceeds maximum size of %d bytes", maxComposeYAMLSize)
	}
	if isSpecDescriptor(spec.ComposeYAML) {
		return fmt.Errorf("compose_yaml contains a workload spec descriptor (kind:/compose_yaml: wrapper), not a Docker Compose file — submit the raw compose YAML instead")
	}
	dir, err := c.composeDir(spec.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create compose dir: %w", err)
	}
	composeYAML := rewriteComposePorts(spec.ComposeYAML, portAllocations)
	composeYAML = injectMeshNetwork(composeYAML, c.meshNet())
	composeFile := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composeFile, []byte(composeYAML), 0o600); err != nil {
		return fmt.Errorf("write compose file: %w", err)
	}
	if len(spec.ResolvedEnv) > 0 {
		envContent := strings.Join(spec.ResolvedEnv, "\n") + "\n"
		envFile := filepath.Join(dir, ".env")
		if err := os.WriteFile(envFile, []byte(envContent), 0o600); err != nil {
			return fmt.Errorf("write env file: %w", err)
		}
	}
	_, err = c.runInsecure(ctx, spec.InsecureRegistry, "compose", "-f", composeFile, "--project-name", spec.Name, "up", "-d")
	return err
}

// ComposeDown stops and removes a compose stack.
func (c *Client) ComposeDown(ctx context.Context, stackName string) error {
	dir, err := c.composeDir(stackName)
	if err != nil {
		return err
	}
	composeFile := filepath.Join(dir, "docker-compose.yml")
	if _, err := os.Stat(composeFile); err != nil {
		return fmt.Errorf("compose file not found for stack %q: %w", stackName, err)
	}
	// Explicit stop before down for the same reason as RemoveContainer: rootlesskit
	// must release port-forwarding before containers are deleted. compose down issues
	// stops internally, but running stop first ensures the network teardown is fully
	// complete before removal begins.
	_, _ = c.run(ctx, "compose", "-f", composeFile, "--project-name", stackName, "stop")
	_, err = c.run(ctx, "compose", "-f", composeFile, "--project-name", stackName, "down", "--remove-orphans")
	return err
}

// parseComposePSOutput handles both JSON array and JSONL output from
// `nerdctl compose ps --format json`, which varies across nerdctl versions.
func parseComposePSOutput(out []byte) ([]serviceInfo, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, nil
	}
	if out[0] == '[' {
		var services []serviceInfo
		if err := json.Unmarshal(out, &services); err != nil {
			return nil, err
		}
		return services, nil
	}
	// Fall back to JSONL (one object per line).
	var services []serviceInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var si serviceInfo
		if err := json.Unmarshal(line, &si); err != nil {
			continue
		}
		services = append(services, si)
	}
	return services, scanner.Err()
}

// serviceInfo matches the per-line JSON from `nerdctl compose ps --format '{{json .}}'`.
type serviceInfo struct {
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	Service string `json:"Service"`
	Status  string `json:"Status"`
}

// ContainerInspectResult holds the fields we surface from `nerdctl inspect`.
type ContainerInspectResult struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		Pid       int    `json:"Pid"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	Config struct {
		Image string   `json:"Image"`
		Env   []string `json:"Env"`
	} `json:"Config"`
	HostConfig struct {
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress  string `json:"IPAddress"`
			Gateway    string `json:"Gateway"`
			MacAddress string `json:"MacAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

// InspectContainer returns full runtime details for the named container.
func (c *Client) InspectContainer(ctx context.Context, name string) (*ContainerInspectResult, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	out, err := c.runStdout(ctx, "inspect", "--type=container", "--", name)
	if err != nil {
		return nil, err
	}
	var results []ContainerInspectResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &results); err != nil {
		return nil, fmt.Errorf("parse container inspect: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("container %q not found", name)
	}
	return &results[0], nil
}

// RestartContainer restarts the named container.
func (c *Client) RestartContainer(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	_, err := c.run(ctx, "restart", "--", name)
	return err
}

// NetworkInfo is the per-entry data from `nerdctl network ls --format '{{json .}}'`.
type NetworkInfo struct {
	NetworkID string `json:"NetworkID"`
	Name      string `json:"Name"`
	Driver    string `json:"Driver"`
	IPv4      string `json:"IPv4"`
	Labels    string `json:"Labels"`
}

// NetworkContainer is a container attached to a network.
type NetworkContainer struct {
	Name        string `json:"Name"`
	IPv4Address string `json:"IPv4Address"`
}

// NetworkDetail is the full inspect result from `nerdctl network inspect`.
type NetworkDetail struct {
	Name   string `json:"Name"`
	ID     string `json:"Id"`
	Driver string `json:"Driver"`
	IPAM   struct {
		Config []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
	Containers map[string]NetworkContainer `json:"Containers"`
	Labels     map[string]string           `json:"Labels"`
}

// VolumeInfo is the per-entry data from `nerdctl volume ls --format '{{json .}}'`.
type VolumeInfo struct {
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Mountpoint string `json:"Mountpoint"`
	Labels     string `json:"Labels"`
}

// VolumeDetail is the full inspect result from `nerdctl volume inspect`.
type VolumeDetail struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Mountpoint string            `json:"Mountpoint"`
	Labels     map[string]string `json:"Labels"`
	Scope      string            `json:"Scope"`
}

// meshNetworkLabel marks the per-node mesh network so it is recognizable as
// orchestrator-managed (value is the subnet it was created for).
const meshNetworkLabel = "orchestrator.mesh"

// EnsureMeshNetwork makes the per-node mesh network exist with the given subnet,
// gateway and bridge MTU, creating it if absent and recreating it if its subnet
// drifted (e.g. the node was assigned a different /24). Containers attached to
// this network draw mesh IPs from subnet; gateway is this node's own mesh
// address. mtu should match the WireGuard tunnel MTU (typically 1380) so that
// container-to-container packets fit inside the encapsulated frames without
// fragmentation.
func (c *Client) EnsureMeshNetwork(ctx context.Context, name, subnet, gateway string, mtu int) error {
	if err := validateName(name); err != nil {
		return err
	}
	if detail, err := c.InspectNetwork(ctx, name); err == nil {
		if networkMatches(detail, subnet) {
			return nil // already correct
		}
		slog.Info("nerdctl: mesh network subnet changed, recreating", "name", name, "want", subnet)
		if _, err := c.run(ctx, "network", "rm", "--", name); err != nil {
			return fmt.Errorf("remove stale mesh network %q: %w", name, err)
		}
	}
	args := []string{
		"network", "create",
		"--subnet", subnet,
		"--gateway", gateway,
		"--label", meshNetworkLabel + "=" + subnet,
	}
	if mtu > 0 {
		args = append(args, "--opt", fmt.Sprintf("com.docker.network.driver.mtu=%d", mtu))
	}
	args = append(args, name)
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("create mesh network %q (%s): %w", name, subnet, err)
	}
	slog.Info("nerdctl: mesh network ready", "name", name, "subnet", subnet, "gateway", gateway, "mtu", mtu)
	return nil
}

// networkMatches reports whether detail's IPAM already declares subnet.
func networkMatches(detail *NetworkDetail, subnet string) bool {
	for _, cfg := range detail.IPAM.Config {
		if cfg.Subnet == subnet {
			return true
		}
	}
	return false
}

// ListNetworks returns all networks in the orchestrator namespace.
func (c *Client) ListNetworks(ctx context.Context) ([]NetworkInfo, error) {
	out, err := c.runStdout(ctx, "network", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var result []NetworkInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ni NetworkInfo
		if err := json.Unmarshal([]byte(line), &ni); err != nil {
			continue
		}
		result = append(result, ni)
	}
	return result, scanner.Err()
}

// InspectNetwork returns full details for a single network by name.
func (c *Client) InspectNetwork(ctx context.Context, name string) (*NetworkDetail, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	out, err := c.runStdout(ctx, "network", "inspect", "--", name)
	if err != nil {
		return nil, err
	}
	var results []NetworkDetail
	if err := json.Unmarshal(bytes.TrimSpace(out), &results); err != nil {
		return nil, fmt.Errorf("parse network inspect: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("network %q not found", name)
	}
	return &results[0], nil
}

// ListVolumes returns all named volumes in the orchestrator namespace.
func (c *Client) ListVolumes(ctx context.Context) ([]VolumeInfo, error) {
	out, err := c.runStdout(ctx, "volume", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var result []VolumeInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var vi VolumeInfo
		if err := json.Unmarshal([]byte(line), &vi); err != nil {
			continue
		}
		result = append(result, vi)
	}
	return result, scanner.Err()
}

// InspectVolume returns full details for a single volume by name.
func (c *Client) InspectVolume(ctx context.Context, name string) (*VolumeDetail, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	out, err := c.runStdout(ctx, "volume", "inspect", "--", name)
	if err != nil {
		return nil, err
	}
	var results []VolumeDetail
	if err := json.Unmarshal(bytes.TrimSpace(out), &results); err != nil {
		return nil, fmt.Errorf("parse volume inspect: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("volume %q not found", name)
	}
	return &results[0], nil
}

// ComposePS returns the services of a running compose stack.
// Returns nil, nil if the stack is not deployed on this node.
func (c *Client) ComposePS(ctx context.Context, stackName string) ([]types.ActualContainer, error) {
	dir, err := c.composeDir(stackName)
	if err != nil {
		return nil, err
	}
	composeFile := filepath.Join(dir, "docker-compose.yml")
	if _, err := os.Stat(composeFile); err != nil {
		return nil, nil
	}
	// nerdctl compose ps only accepts "table" or "json" as format values;
	// the Go template syntax {{json .}} used by plain "nerdctl ps" is not supported.
	// Use runStdout so that nerdctl log lines written to stderr do not corrupt
	// the JSON output we need to parse.
	out, err := c.runStdout(ctx, "compose", "-f", composeFile, "--project-name", stackName, "ps", "--format", "json")
	if err != nil {
		return nil, err
	}
	services, err := parseComposePSOutput(out)
	if err != nil {
		return nil, fmt.Errorf("parse compose ps output: %w", err)
	}
	result := make([]types.ActualContainer, 0, len(services))
	for _, si := range services {
		result = append(result, types.ActualContainer{
			ContainerID: si.ID,
			Name:        si.Name,
			Status:      si.Status,
		})
	}
	return result, nil
}
