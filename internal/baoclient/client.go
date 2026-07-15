// Package baoclient is a minimal OpenBao (Vault-compatible) KV v2 client.
// It uses only net/http — no external SDK required.
package baoclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one OpenBao instance using token auth.
type Client struct {
	address string
	token   string
	mount   string
	http    *http.Client
}

// New returns a client for the given OpenBao instance. mount is the KV v2
// secret engine path (e.g. "secret"); an empty string defaults to "secret".
//
// caCert is an optional PEM CA bundle to trust in addition to the system
// roots — set it when OpenBao's certificate is signed by an internal CA.
// insecureSkipVerify disables TLS certificate verification entirely and
// takes precedence over caCert; it exists for testing only.
func New(address, token, mount, caCert string, insecureSkipVerify bool) *Client {
	if mount == "" {
		mount = "secret"
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	if insecureSkipVerify || caCert != "" {
		tlsConfig := &tls.Config{InsecureSkipVerify: insecureSkipVerify} //nolint:gosec
		if !insecureSkipVerify && caCert != "" {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM([]byte(caCert)) {
				tlsConfig.RootCAs = pool
			}
		}
		httpClient.Transport = &http.Transport{TLSClientConfig: tlsConfig}
	}
	return &Client{
		address: strings.TrimRight(address, "/"),
		token:   token,
		mount:   mount,
		http:    httpClient,
	}
}

// Health returns nil when the server is reachable and unsealed.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.address+"/v1/sys/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	// 200 = active leader, 429 = standby — both are reachable and functional
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusTooManyRequests {
		return fmt.Errorf("health check returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// Write stores value at path (relative to mount, e.g. "orchestrator/db-pass").
func (c *Client) Write(ctx context.Context, path, value string) error {
	body, _ := json.Marshal(map[string]any{"data": map[string]string{"value": value}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.kvDataURL(path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return c.apiError("write", resp)
	}
	return nil
}

// Read retrieves the value stored at path.
func (c *Client) Read(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.kvDataURL(path), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Vault-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("secret not found at path %q", path)
	}
	if resp.StatusCode != http.StatusOK {
		return "", c.apiError("read", resp)
	}
	var out struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	v, ok := out.Data.Data["value"]
	if !ok {
		return "", fmt.Errorf("key %q: no 'value' field in KV data", path)
	}
	return v, nil
}

// Delete permanently removes all versions of the secret at path.
func (c *Client) Delete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.kvMetaURL(path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return c.apiError("delete", resp)
	}
	return nil
}

func (c *Client) kvDataURL(path string) string {
	return fmt.Sprintf("%s/v1/%s/data/%s", c.address, c.mount, path)
}

func (c *Client) kvMetaURL(path string) string {
	return fmt.Sprintf("%s/v1/%s/metadata/%s", c.address, c.mount, path)
}

func (c *Client) apiError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("openbao %s: HTTP %d: %s", op, resp.StatusCode, strings.TrimSpace(string(body)))
}
