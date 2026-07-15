package raft

import (
	"testing"
)

func TestParseComposeContainerPorts(t *testing.T) {
	yaml := `
services:
  web:
    image: nginx:latest
    ports:
      - "8080:80"
      - "443:443/tcp"
  api:
    image: myapp:latest
    ports:
      - "127.0.0.1:3000:3000"
      - "0.0.0.0:9000:9000/udp"
      - "8888"
  noports:
    image: redis:latest
`
	got := parseComposeContainerPorts(yaml)
	want := []struct {
		container uint32
		proto     string
	}{
		{80, "tcp"},
		{443, "tcp"},
		{3000, "tcp"},
		{9000, "udp"},
		{8888, "tcp"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d allocations, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.ContainerPort != w.container || g.Protocol != w.proto {
			t.Errorf("[%d] got {%d %s}, want {%d %s}",
				i, g.ContainerPort, g.Protocol, w.container, w.proto)
		}
		if g.AllocatedPort != 0 {
			t.Errorf("[%d] AllocatedPort should be 0 (FSM assigns it), got %d", i, g.AllocatedPort)
		}
	}
}

// Each port declaration line produces its own entry; there is no deduplication.
// Two services each declaring port 80 must each get their own host binding.
func TestParseComposeContainerPortsSamePortTwoServices(t *testing.T) {
	yaml := `
services:
  web:
    ports:
      - "80"
  api:
    ports:
      - "80"
`
	got := parseComposeContainerPorts(yaml)
	if len(got) != 2 {
		t.Fatalf("expected 2 allocations (one per service), got %d: %+v", len(got), got)
	}
	for i, pa := range got {
		if pa.ContainerPort != 80 {
			t.Errorf("[%d] unexpected container port: %d", i, pa.ContainerPort)
		}
	}
}
