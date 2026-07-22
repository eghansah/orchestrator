package nerdctl

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/eghansah/orchestrator/pkg/types"
)

// tmpfsTestDir returns a directory under /dev/shm for tests that need a real
// tmpfs mount, skipping the test if /dev/shm isn't usable in this environment.
func tmpfsTestDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("/dev/shm", "orchestrator-test-"+strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Skipf("/dev/shm not usable in this environment: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestVerifyTmpfs_AcceptsTmpfs(t *testing.T) {
	dir := tmpfsTestDir(t)
	if err := verifyTmpfs(dir); err != nil {
		t.Errorf("expected /dev/shm subdir to pass as tmpfs, got: %v", err)
	}
}

// TestVerifyTmpfs_RejectsNonTmpfs asserts the check refuses a directory on
// persistent disk rather than silently allowing secret plaintext to be
// written there. Uses the package's own source directory, which lives on the
// repo's real filesystem in ordinary checkouts; if that assumption doesn't
// hold in some environment (e.g. the whole checkout sits on tmpfs), the test
// skips rather than asserting something false.
func TestVerifyTmpfs_RejectsNonTmpfs(t *testing.T) {
	dir := filepath.Join(".", "testdata-tmpfs-check-"+strconv.Itoa(os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(dir) })

	err := verifyTmpfs(dir)
	if err == nil {
		t.Skip("this checkout's filesystem is itself tmpfs-backed; cannot exercise the rejection path here")
	}
	if !strings.Contains(err.Error(), "not tmpfs") {
		t.Errorf("expected a 'not tmpfs' error, got: %v", err)
	}
}

func TestStageSecretFiles_WritesContentAndMode(t *testing.T) {
	root := tmpfsTestDir(t)
	files := []types.ResolvedSecretFile{
		{Name: "db-pass", Plaintext: "hunter2", Mode: 0o400},
		{Name: "api-key", Plaintext: "sk-abc123"}, // Mode unset -> default 0400
	}
	if err := stageSecretFiles(root, files, func(types.ResolvedSecretFile) string { return "web" }); err != nil {
		t.Fatalf("stageSecretFiles: %v", err)
	}
	for _, f := range files {
		path := filepath.Join(root, "web", f.Name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read staged file %s: %v", path, err)
		}
		if string(data) != f.Plaintext {
			t.Errorf("staged file %s: got %q, want %q", path, data, f.Plaintext)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat staged file %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o400 {
			t.Errorf("staged file %s: got mode %o, want 0400", path, info.Mode().Perm())
		}
	}
}

// TestStageSecretFiles_RedeployOverwritesReadOnlyFile guards against a
// redeploy regression: staged files are written 0400 (read-only), so
// restaging into the same root without clearing it first used to fail with
// EACCES on the second deploy, since owner write permission isn't
// root-exempt even for the file's own owner.
func TestStageSecretFiles_RedeployOverwritesReadOnlyFile(t *testing.T) {
	root := tmpfsTestDir(t)
	files := []types.ResolvedSecretFile{
		{Name: "db-pass", Plaintext: "hunter2"},
	}
	subdir := func(types.ResolvedSecretFile) string { return "auth" }
	if err := stageSecretFiles(root, files, subdir); err != nil {
		t.Fatalf("first stageSecretFiles: %v", err)
	}
	files[0].Plaintext = "hunter3"
	if err := stageSecretFiles(root, files, subdir); err != nil {
		t.Fatalf("second stageSecretFiles (redeploy) failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "auth", "db-pass"))
	if err != nil {
		t.Fatalf("read restaged file: %v", err)
	}
	if string(data) != "hunter3" {
		t.Errorf("got %q, want %q", data, "hunter3")
	}
}

func TestStageSecretFiles_NoFilesIsNoop(t *testing.T) {
	// A root that doesn't exist and isn't tmpfs would fail verifyTmpfs if
	// reached; passing no files must short-circuit before that check.
	if err := stageSecretFiles("/nonexistent/definitely/not/tmpfs", nil, func(types.ResolvedSecretFile) string { return "" }); err != nil {
		t.Errorf("expected no-op for empty files, got: %v", err)
	}
}

// TestBuildRunArgs_SkipsSecretVolumes: secret-typed VolumeMount entries must
// never be passed straight through as a raw bind mount (Source is a secret
// name, not a host path) — RunContainer materializes and mounts them
// separately after staging, so buildRunArgs must skip them entirely.
func TestBuildRunArgs_SkipsSecretVolumes(t *testing.T) {
	spec := types.ContainerSpec{
		Name: "web",
		Volumes: []types.VolumeMount{
			{Source: "/host/data", Target: "/data"},
			{Type: types.VolumeTypeSecret, Source: "db-pass", Target: "/run/secrets/db-pass"},
		},
	}
	args := buildRunArgs("wl-1", spec, nil, "", 0, "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/host/data:/data") {
		t.Errorf("expected bind mount to be present, got args: %v", args)
	}
	if strings.Contains(joined, "db-pass") {
		t.Errorf("secret volume entry leaked into raw -v args: %v", args)
	}
}
