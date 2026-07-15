package nerdctl

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

func TestRegistryHost(t *testing.T) {
	cases := []struct {
		image string
		want  string
	}{
		{"nginx:latest", ""},
		{"library/nginx:latest", ""},
		{"myuser/myrepo:tag", ""},
		{"127.0.0.1:5000/foo:latest", "127.0.0.1:5000"},
		{"localhost:5000/foo:latest", "localhost:5000"},
		{"localhost/foo", "localhost"},
		{"registry.example.com/foo:tag", "registry.example.com"},
		{"registry.example.com:5000/foo:tag", "registry.example.com:5000"},
		{"registry.example.com/team/foo@sha256:deadbeef", "registry.example.com"},
	}
	for _, c := range cases {
		if got := registryHost(c.image); got != c.want {
			t.Errorf("registryHost(%q) = %q, want %q", c.image, got, c.want)
		}
	}
}

func TestIsLocalhostImage(t *testing.T) {
	if !isLocalhostImage("127.0.0.1:5000/foo:latest") {
		t.Error("expected 127.0.0.1 image to be detected as localhost")
	}
	if isLocalhostImage("registry.example.com/foo:latest") {
		t.Error("did not expect a real registry host to be detected as localhost")
	}
	if isLocalhostImage("nginx:latest") {
		t.Error("did not expect a bare image name to be detected as localhost")
	}
}

func TestComposeImageHosts(t *testing.T) {
	yaml := `services:
  web:
    image: registry.example.com/web:latest
  api:
    image: registry.example.com/api:latest
  db:
    image: postgres:15
`
	hosts := composeImageHosts(yaml)
	sort.Strings(hosts)
	want := []string{"registry.example.com"}
	if !slices.Equal(hosts, want) {
		t.Errorf("composeImageHosts() = %v, want %v (deduped, excluding bare images)", hosts, want)
	}
}

func TestMaterializeRegistryCA(t *testing.T) {
	dir := t.TempDir()
	c := &Client{dataDir: dir}

	hostsDir, err := c.materializeRegistryCA([]string{"registry.example.com:5000"}, "FAKE-PEM-BUNDLE")
	if err != nil {
		t.Fatalf("materializeRegistryCA: %v", err)
	}
	wantDir := filepath.Join(dir, "registry-certs")
	if hostsDir != wantDir {
		t.Errorf("hostsDir = %q, want %q", hostsDir, wantDir)
	}
	caFile := filepath.Join(wantDir, "registry.example.com:5000", "ca.crt")
	got, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("read %s: %v", caFile, err)
	}
	if string(got) != "FAKE-PEM-BUNDLE" {
		t.Errorf("ca.crt content = %q, want %q", got, "FAKE-PEM-BUNDLE")
	}

	// Empty bundle or no hosts should be a no-op, not an error.
	if hostsDir, err := c.materializeRegistryCA(nil, "FAKE-PEM-BUNDLE"); err != nil || hostsDir != "" {
		t.Errorf("materializeRegistryCA(nil hosts) = (%q, %v), want (\"\", nil)", hostsDir, err)
	}
	if hostsDir, err := c.materializeRegistryCA([]string{"x"}, ""); err != nil || hostsDir != "" {
		t.Errorf("materializeRegistryCA(empty bundle) = (%q, %v), want (\"\", nil)", hostsDir, err)
	}
}
