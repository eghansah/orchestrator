package nerdctl

import (
	"strings"
	"testing"

	"github.com/eghansah/orchestrator/pkg/types"
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
	yaml := `services:
  web:
    ports:
      - "80"
`
	got := rewriteComposePorts(yaml, nil)
	if got != yaml {
		t.Errorf("expected unchanged YAML when no allocations, got:\n%s", got)
	}
}
