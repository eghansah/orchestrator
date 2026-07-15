package baoclient

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
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
