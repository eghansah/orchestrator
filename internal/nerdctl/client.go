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
	"strings"

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
	_, err := c.run(ctx, "rm", "-f", "--", name)
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
	_, err = c.run(ctx, "compose", "-f", composeFile, "--project-name", stackName, "down")
	return err
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
	out, err := c.run(ctx, "compose", "-f", composeFile, "--project-name", stackName, "ps", "--format", "{{json .}}")
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
		var si serviceInfo
		if err := json.Unmarshal([]byte(line), &si); err != nil {
			continue
		}
		result = append(result, types.ActualContainer{
			ContainerID: si.ID,
			Name:        si.Name,
			Status:      si.Status,
		})
	}
	return result, scanner.Err()
}
