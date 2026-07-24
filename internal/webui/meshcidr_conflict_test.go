package webui

import (
	"net"
	"testing"
)

func mustParseCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}

func TestCIDRsOverlap(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "100.64.0.0/24", "100.64.0.0/24", true},
		{"host subnet nested in mesh pool", "100.64.0.0/10", "100.64.0.0/24", true},
		{"mesh pool nested in host subnet", "100.64.0.0/24", "100.64.0.0/10", true},
		{"disjoint", "100.64.0.0/10", "10.4.3.0/24", false},
		{"adjacent but disjoint /24s", "100.64.0.0/24", "100.64.1.0/24", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := mustParseCIDR(t, c.a)
			b := mustParseCIDR(t, c.b)
			if got := cidrsOverlap(a, b); got != c.want {
				t.Errorf("cidrsOverlap(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestCheckMeshCIDROverlap_EmptyOrInvalid(t *testing.T) {
	if got := checkMeshCIDROverlap(""); got != nil {
		t.Errorf("expected nil for empty CIDR, got %v", got)
	}
	if got := checkMeshCIDROverlap("not-a-cidr"); got != nil {
		t.Errorf("expected nil for invalid CIDR, got %v", got)
	}
}
