package nerdctl

import (
	"slices"
	"strings"
	"testing"

	"github.com/eghansah/orchestrator/pkg/types"
)

func TestNetworkMatches(t *testing.T) {
	detail := &NetworkDetail{}
	detail.IPAM.Config = []struct {
		Subnet  string `json:"Subnet"`
		Gateway string `json:"Gateway"`
	}{
		{Subnet: "100.64.3.0/24", Gateway: "100.64.3.1"},
	}
	if !networkMatches(detail, "100.64.3.0/24") {
		t.Error("expected match for declared subnet")
	}
	if networkMatches(detail, "100.64.9.0/24") {
		t.Error("expected no match for a different subnet")
	}
	if networkMatches(&NetworkDetail{}, "100.64.3.0/24") {
		t.Error("expected no match when IPAM has no config")
	}
}

func TestBuildRunArgs_MeshAttach(t *testing.T) {
	spec := types.ContainerSpec{Name: "web", Image: "nginx:latest"}
	pas := []types.PortAllocation{{ContainerPort: 80, AllocatedPort: 41000, Protocol: "tcp"}}

	// Without a mesh network: no --network flag.
	off := buildRunArgs("wl-1", spec, pas, "", 0, "")
	if slices.Contains(off, "--network") {
		t.Errorf("did not expect --network when mesh disabled: %v", off)
	}

	// With a mesh network: --network mesh0 present, and immediately after --name
	// so it precedes image/command.
	on := buildRunArgs("wl-1", spec, pas, "", 0, "mesh0")
	idx := slices.Index(on, "--network")
	if idx == -1 || on[idx+1] != "mesh0" {
		t.Fatalf("expected --network mesh0 in args: %v", on)
	}
	// Mesh attach must not disturb the existing loopback port publish.
	joined := strings.Join(on, " ")
	if !strings.Contains(joined, "-p 127.0.0.1:41000:80/tcp") {
		t.Errorf("loopback port publish missing or altered: %v", on)
	}
	// Image stays last (before any command).
	if on[len(on)-1] != "nginx:latest" {
		t.Errorf("image should remain the final arg: %v", on)
	}
}

func TestBuildRunArgs_StableWithDNSAndCommand(t *testing.T) {
	spec := types.ContainerSpec{
		Name:    "api",
		Image:   "busybox",
		Command: []string{"sh", "-c", "sleep 1"},
		Env:     []string{"FOO=bar"},
	}
	args := buildRunArgs("wl-2", spec, nil, "10.0.0.1", 5353, "mesh0")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run -d --name api",
		"--network mesh0",
		"-e FOO=bar",
		"--dns 10.0.0.1 --dns-search svc.local",
		"--dns-opt port:5353",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q in: %s", want, joined)
		}
	}
	// Command trails the image.
	if got := args[len(args)-3:]; !slices.Equal(got, []string{"sh", "-c", "sleep 1"}) {
		t.Errorf("command should trail image, got %v", got)
	}
}
