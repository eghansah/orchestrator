package nerdctl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	address   string   // containerd socket path, passed as --address to nerdctl
	dataDir   string   // root for compose file storage
	dnsIP     string   // injected into containers for svc.local resolution
	dnsPort   uint32   // DNS port; if != 53, adds resolv.conf "options port:N"
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
	global := []string{"--namespace", c.namespace}
	if c.address != "" {
		global = append(global, "--address", c.address)
	}
	full := append(global, args...)
	cmd := exec.CommandContext(ctx, c.binary, full...)
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

func (c *Client) Pull(ctx context.Context, image string) error {
	_, err := c.run(ctx, "pull", image)
	return err
}

// RunContainer starts a detached container from the given spec, tagged with workloadID.
// portAllocations maps container ports to auto-assigned host ports, always bound on
// 127.0.0.1 so containers are only reachable via the ingress proxy.
func (c *Client) RunContainer(ctx context.Context, workloadID string, spec types.ContainerSpec, portAllocations []types.PortAllocation) error {
	if err := validateName(spec.Name); err != nil {
		return err
	}

	// Build containerPort → allocatedPort lookup from auto-assigned allocations.
	allocMap := make(map[uint32]uint32, len(portAllocations))
	for _, pa := range portAllocations {
		allocMap[pa.ContainerPort] = pa.AllocatedPort
	}

	args := []string{"run", "-d", "--name", spec.Name}
	for _, env := range spec.Env {
		args = append(args, "-e", env)
	}
	for _, p := range spec.Ports {
		if alloc, ok := allocMap[p.ContainerPort]; ok && alloc > 0 {
			// Bind on loopback only — containers are not reachable from the network.
			args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d/%s", alloc, p.ContainerPort, p.Protocol))
		}
		// Ports without an allocation are intentionally not bound.
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
	if c.dnsIP != "" {
		args = append(args, "--dns", c.dnsIP, "--dns-search", "svc.local")
		if c.dnsPort != 0 && c.dnsPort != 53 {
			args = append(args, "--dns-opt", fmt.Sprintf("port:%d", c.dnsPort))
		}
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)

	_, err := c.run(ctx, args...)
	return err
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
	return result, scanner.Err()
}

func (c *Client) composeDir(stackName string) (string, error) {
	if err := validateName(stackName); err != nil {
		return "", err
	}
	return filepath.Join(c.dataDir, "stacks", stackName), nil
}

// ComposeUp writes the compose YAML and runs nerdctl compose up -d.
func (c *Client) ComposeUp(ctx context.Context, _ string, spec types.ComposeStackSpec) error {
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
	composeFile := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composeFile, []byte(spec.ComposeYAML), 0o600); err != nil {
		return fmt.Errorf("write compose file: %w", err)
	}
	_, err = c.run(ctx, "compose", "-f", composeFile, "--project-name", spec.Name, "up", "-d")
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
