package baoclient

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func serverCAPEM(srv *httptest.Server) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
}

func TestHealth_UntrustedCert_RejectedByDefault(t *testing.T) {
	srv := newTestServer(t)
	c := New(srv.URL, "token", "", "", false)
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("expected TLS verification error against an untrusted self-signed cert, got nil")
	}
}

func TestHealth_WithCACert_Trusted(t *testing.T) {
	srv := newTestServer(t)
	c := New(srv.URL, "token", "", serverCAPEM(srv), false)
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("expected success once server cert is trusted via CACert, got: %v", err)
	}
}

func TestHealth_InsecureSkipVerify_Trusted(t *testing.T) {
	srv := newTestServer(t)
	c := New(srv.URL, "token", "", "", true)
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("expected success with InsecureSkipVerify, got: %v", err)
	}
}

// newCapsServer fakes sys/capabilities-self, returning dataCaps for any path
// containing "/data/" and metaCaps for any path containing "/metadata/".
func newCapsServer(t *testing.T, dataCaps, metaCaps []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Paths []string `json:"paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Paths) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var caps []string
		switch {
		case strings.Contains(body.Paths[0], "/data/"):
			caps = dataCaps
		case strings.Contains(body.Paths[0], "/metadata/"):
			caps = metaCaps
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckSecretAccess_FullAccess(t *testing.T) {
	srv := newCapsServer(t, []string{"create", "update", "read"}, []string{"delete"})
	c := New(srv.URL, "token", "", "", false)
	if err := c.CheckSecretAccess(context.Background(), "orchestrator/"); err != nil {
		t.Fatalf("expected access granted, got: %v", err)
	}
}

func TestCheckSecretAccess_RootToken(t *testing.T) {
	srv := newCapsServer(t, []string{"root"}, []string{"root"})
	c := New(srv.URL, "token", "", "", false)
	if err := c.CheckSecretAccess(context.Background(), "orchestrator/"); err != nil {
		t.Fatalf("expected root token to satisfy every check, got: %v", err)
	}
}

func TestCheckSecretAccess_MissingWrite(t *testing.T) {
	srv := newCapsServer(t, []string{"read"}, []string{"delete"})
	c := New(srv.URL, "token", "", "", false)
	err := c.CheckSecretAccess(context.Background(), "orchestrator/")
	if err == nil || !strings.Contains(err.Error(), "create and update") {
		t.Fatalf("expected error naming missing create/update capability, got: %v", err)
	}
}

func TestCheckSecretAccess_MissingRead(t *testing.T) {
	srv := newCapsServer(t, []string{"create", "update"}, []string{"delete"})
	c := New(srv.URL, "token", "", "", false)
	err := c.CheckSecretAccess(context.Background(), "orchestrator/")
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("expected error naming missing read capability, got: %v", err)
	}
}

func TestCheckSecretAccess_MissingDelete(t *testing.T) {
	srv := newCapsServer(t, []string{"create", "update", "read"}, []string{"list"})
	c := New(srv.URL, "token", "", "", false)
	err := c.CheckSecretAccess(context.Background(), "orchestrator/")
	if err == nil || !strings.Contains(err.Error(), "delete") {
		t.Fatalf("expected error naming missing delete capability, got: %v", err)
	}
}

func TestCheckSecretAccess_Denied(t *testing.T) {
	srv := newCapsServer(t, []string{"deny"}, []string{"deny"})
	c := New(srv.URL, "token", "", "", false)
	if err := c.CheckSecretAccess(context.Background(), "orchestrator/"); err == nil {
		t.Fatal("expected error when the token's capabilities resolve to deny")
	}
}

func TestCheckSecretAccess_ProbesExpectedPaths(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Paths []string `json:"paths"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPaths = append(gotPaths, body.Paths...)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"root"}})
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "token", "", "", false) // default mount: "secret"
	if err := c.CheckSecretAccess(context.Background(), "orchestrator/"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"secret/data/orchestrator/.capability-check",
		"secret/metadata/orchestrator/.capability-check",
	}
	if len(gotPaths) != 2 || gotPaths[0] != want[0] || gotPaths[1] != want[1] {
		t.Fatalf("probed paths = %v, want %v", gotPaths, want)
	}
}
