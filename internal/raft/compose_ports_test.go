package raft

import (
	"testing"
)

func TestParseComposePortAllocations(t *testing.T) {
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
  noports:
    image: redis:latest
`
	got := parseComposePortAllocations(yaml)
	want := []struct{ host, container uint32; proto string }{
		{8080, 80,   "tcp"},
		{443,  443,  "tcp"},
		{3000, 3000, "tcp"},
		{9000, 9000, "udp"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d allocations, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.AllocatedPort != w.host || g.ContainerPort != w.container || g.Protocol != w.proto {
			t.Errorf("[%d] got {%d %d %s}, want {%d %d %s}",
				i, g.AllocatedPort, g.ContainerPort, g.Protocol,
				w.host, w.container, w.proto)
		}
	}
}

func TestParseComposePortSkipsContainerOnly(t *testing.T) {
	yaml := `
services:
  web:
    ports:
      - "80"
      - "8080:80"
`
	got := parseComposePortAllocations(yaml)
	if len(got) != 1 {
		t.Fatalf("expected 1 allocation (container-only skipped), got %d: %+v", len(got), got)
	}
	if got[0].AllocatedPort != 8080 || got[0].ContainerPort != 80 {
		t.Errorf("unexpected allocation: %+v", got[0])
	}
}
