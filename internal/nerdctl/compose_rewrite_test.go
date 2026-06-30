package nerdctl

import (
	"slices"
	"strings"
	"testing"

	"github.com/eghansah/orchestrator/pkg/types"
	"gopkg.in/yaml.v3"
)

func TestRewriteComposePorts(t *testing.T) {
	yaml := `services:
  web:
    image: nginx:latest
    ports:
      - "8080:80"
      - "443:443/tcp"
  api:
    ports:
      - "3000"
`
	allocations := []types.PortAllocation{
		{ContainerPort: 80, AllocatedPort: 30000, Protocol: "tcp"},
		{ContainerPort: 443, AllocatedPort: 30001, Protocol: "tcp"},
		{ContainerPort: 3000, AllocatedPort: 30002, Protocol: "tcp"},
	}
	got := rewriteComposePorts(yaml, allocations)

	checks := []string{
		`"127.0.0.1:30000:80/tcp"`,
		`"127.0.0.1:30001:443/tcp"`,
		`"127.0.0.1:30002:3000/tcp"`,
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("rewritten YAML missing %s\ngot:\n%s", c, got)
		}
	}
	if strings.Contains(got, `"8080:80"`) || strings.Contains(got, `"443:443`) {
		t.Errorf("rewritten YAML still contains original host-side ports\ngot:\n%s", got)
	}
}

// Two services both exposing port 80 must each get their own loopback binding.
func TestRewriteComposePortsSamePortTwoServices(t *testing.T) {
	yaml := `services:
  web:
    ports:
      - "80"
  api:
    ports:
      - "80"
`
	allocations := []types.PortAllocation{
		{ContainerPort: 80, AllocatedPort: 30000, Protocol: "tcp"},
		{ContainerPort: 80, AllocatedPort: 30001, Protocol: "tcp"},
	}
	got := rewriteComposePorts(yaml, allocations)
	if !strings.Contains(got, `"127.0.0.1:30000:80/tcp"`) {
		t.Errorf("missing binding for web service\ngot:\n%s", got)
	}
	if !strings.Contains(got, `"127.0.0.1:30001:80/tcp"`) {
		t.Errorf("missing binding for api service\ngot:\n%s", got)
	}
}

func TestRewriteComposePortsNoAllocations(t *testing.T) {
	input := `services:
  web:
    ports:
      - "80"
`
	got := rewriteComposePorts(input, nil)
	if got != input {
		t.Errorf("expected unchanged YAML when no allocations, got:\n%s", got)
	}
}

// parsedNetworks parses a rewritten compose YAML and returns the network names
// declared for the named service.
func parsedNetworks(t *testing.T, composeYAML, svcName string) []string {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		t.Fatalf("parse YAML: %v\n%s", err, composeYAML)
	}
	svcs, _ := doc["services"].(map[string]any)
	svc, _ := svcs[svcName].(map[string]any)
	return svcNetworkNames(svc)
}

func topLevelNetworkKeys(t *testing.T, composeYAML string) []string {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		t.Fatalf("parse YAML: %v", err)
	}
	nets, _ := doc["networks"].(map[string]any)
	keys := make([]string, 0, len(nets))
	for k := range nets {
		keys = append(keys, k)
	}
	return keys
}

func hasTopLevelNetwork(t *testing.T, composeYAML, netName string) bool {
	t.Helper()
	return slices.Contains(topLevelNetworkKeys(t, composeYAML), netName)
}

// TestInjectMeshNetwork_NoNetworks: services with no networks key get
// [default, mesh0]; top-level networks gains mesh0 (external) and default.
func TestInjectMeshNetwork_NoNetworks(t *testing.T) {
	input := `services:
  web:
    image: nginx
  db:
    image: postgres
`
	got := injectMeshNetwork(input, "mesh0")

	for _, svc := range []string{"web", "db"} {
		nets := parsedNetworks(t, got, svc)
		if !slices.Contains(nets, "default") {
			t.Errorf("service %s: expected 'default' in networks, got %v", svc, nets)
		}
		if !slices.Contains(nets, "mesh0") {
			t.Errorf("service %s: expected 'mesh0' in networks, got %v", svc, nets)
		}
	}
	if !hasTopLevelNetwork(t, got, "mesh0") {
		t.Error("top-level networks missing 'mesh0'")
	}
	if !hasTopLevelNetwork(t, got, "default") {
		t.Error("top-level networks missing 'default' (needed because service was migrated from implicit default)")
	}
}

// TestInjectMeshNetwork_ExistingNetworks: mesh0 is appended without dropping
// what was there; no spurious 'default' added to top-level.
func TestInjectMeshNetwork_ExistingNetworks(t *testing.T) {
	input := `services:
  api:
    image: busybox
    networks:
      - backend
networks:
  backend: {}
`
	got := injectMeshNetwork(input, "mesh0")

	nets := parsedNetworks(t, got, "api")
	if !slices.Contains(nets, "backend") {
		t.Errorf("existing network 'backend' dropped: %v", nets)
	}
	if !slices.Contains(nets, "mesh0") {
		t.Errorf("'mesh0' not appended: %v", nets)
	}
	// 'default' should NOT appear because the service had explicit networks.
	if slices.Contains(nets, "default") {
		t.Errorf("unexpected 'default' injected when service already had explicit networks: %v", nets)
	}
}

// TestInjectMeshNetwork_Idempotent: calling twice doesn't duplicate mesh0.
func TestInjectMeshNetwork_Idempotent(t *testing.T) {
	input := `services:
  web:
    image: nginx
`
	once := injectMeshNetwork(input, "mesh0")
	twice := injectMeshNetwork(once, "mesh0")

	nets := parsedNetworks(t, twice, "web")
	count := 0
	for _, n := range nets {
		if n == "mesh0" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected mesh0 exactly once, got %d times in %v", count, nets)
	}
}

// TestInjectMeshNetwork_EmptyName: no-op when network name is empty.
func TestInjectMeshNetwork_EmptyName(t *testing.T) {
	input := `services:
  web:
    image: nginx
`
	got := injectMeshNetwork(input, "")
	if got != input {
		t.Errorf("expected unchanged YAML when networkName is empty")
	}
}

// TestInjectMeshNetwork_MapFormNetworks: services declaring networks in map
// form (with options) also get mesh0 appended.
func TestInjectMeshNetwork_MapFormNetworks(t *testing.T) {
	input := `services:
  app:
    image: busybox
    networks:
      frontend:
        aliases:
          - apphost
networks:
  frontend: {}
`
	got := injectMeshNetwork(input, "mesh0")
	nets := parsedNetworks(t, got, "app")
	if !slices.Contains(nets, "mesh0") {
		t.Errorf("mesh0 not added to map-form networks: %v", nets)
	}
	if !slices.Contains(nets, "frontend") {
		t.Errorf("existing 'frontend' dropped from map-form networks: %v", nets)
	}
}

