package raft

import (
	"fmt"
	"testing"
)

func TestAllocMeshSubnet_FirstFree(t *testing.T) {
	subnet, addr, err := allocMeshSubnet(defaultMeshCIDR, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subnet != "100.64.0.0/24" {
		t.Errorf("subnet = %q, want 100.64.0.0/24", subnet)
	}
	if addr != "100.64.0.1" {
		t.Errorf("addr = %q, want 100.64.0.1", addr)
	}
}

func TestAllocMeshSubnet_SkipsInUse(t *testing.T) {
	inUse := map[string]bool{
		"100.64.0.0/24": true,
		"100.64.1.0/24": true,
	}
	subnet, addr, err := allocMeshSubnet(defaultMeshCIDR, inUse)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subnet != "100.64.2.0/24" {
		t.Errorf("subnet = %q, want 100.64.2.0/24", subnet)
	}
	if addr != "100.64.2.1" {
		t.Errorf("addr = %q, want 100.64.2.1", addr)
	}
}

// Carries past a /24 third-octet boundary into the next higher octet.
func TestAllocMeshSubnet_OctetCarry(t *testing.T) {
	inUse := make(map[string]bool)
	// Mark 100.64.0.0/24 .. 100.64.255.0/24 all in use.
	for i := range 256 {
		inUse[fmt.Sprintf("100.64.%d.0/24", i)] = true
	}
	subnet, _, err := allocMeshSubnet(defaultMeshCIDR, inUse)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subnet != "100.65.0.0/24" {
		t.Errorf("subnet = %q, want 100.65.0.0/24 after carrying past third octet", subnet)
	}
}

func TestAllocMeshSubnet_Exhausted(t *testing.T) {
	// A /24 CIDR contains exactly one /24; mark it used → exhausted.
	inUse := map[string]bool{"10.0.0.0/24": true}
	if _, _, err := allocMeshSubnet("10.0.0.0/24", inUse); err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
}

func TestAllocMeshSubnet_RejectsTooSmallAndIPv6(t *testing.T) {
	if _, _, err := allocMeshSubnet("10.0.0.0/25", nil); err == nil {
		t.Error("expected error for prefix smaller than /24")
	}
	if _, _, err := allocMeshSubnet("fd00::/10", nil); err == nil {
		t.Error("expected error for IPv6 CIDR")
	}
}
