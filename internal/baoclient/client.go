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
	"slices"
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

// SealStatus reports OpenBao's initialization and seal state, including
// Shamir unseal progress. Unlike other endpoints, /v1/sys/seal-status
// requires no token — it must be readable while sealed.
type SealStatus struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
	Threshold   int  `json:"t"`
	Shares      int  `json:"n"`
	Progress    int  `json:"progress"`
}

// SealStatus queries the current seal state.
func (c *Client) SealStatus(ctx context.Context) (SealStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.address+"/v1/sys/seal-status", nil)
	if err != nil {
		return SealStatus{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return SealStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SealStatus{}, c.apiError("seal-status", resp)
	}
	var out SealStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SealStatus{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// Unseal submits one Shamir unseal key share. OpenBao accumulates shares
// across calls until the configured threshold is reached, at which point
// Sealed becomes false in the returned status. Like seal-status, this
// endpoint requires no token — submitting the key share is the credential.
func (c *Client) Unseal(ctx context.Context, key string) (SealStatus, error) {
	body, _ := json.Marshal(map[string]string{"key": key})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.address+"/v1/sys/unseal", bytes.NewReader(body))
	if err != nil {
		return SealStatus{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return SealStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SealStatus{}, c.apiError("unseal", resp)
	}
	var out SealStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SealStatus{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// LoginAppRole exchanges an AppRole role_id/secret_id pair for a client
// token via POST /v1/auth/<authMount>/login. Unlike every other call in this
// package, no token is sent — the role_id/secret_id pair is itself the
// credential. authMount defaults to "approle" when empty.
func LoginAppRole(ctx context.Context, address, authMount, caCert string, insecureSkipVerify bool, roleID, secretID string) (token string, leaseDuration time.Duration, renewable bool, err error) {
	if authMount == "" {
		authMount = "approle"
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
	body, _ := json.Marshal(map[string]string{"role_id": roleID, "secret_id": secretID})
	url := strings.TrimRight(address, "/") + "/v1/auth/" + authMount + "/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", 0, false, fmt.Errorf("openbao approle login: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
			Renewable     bool   `json:"renewable"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, false, fmt.Errorf("decode response: %w", err)
	}
	if out.Auth.ClientToken == "" {
		return "", 0, false, fmt.Errorf("openbao approle login: no client_token in response")
	}
	return out.Auth.ClientToken, time.Duration(out.Auth.LeaseDuration) * time.Second, out.Auth.Renewable, nil
}

// RenewSelf extends the client's current token via POST /v1/auth/token/renew-self,
// returning the new expiry time.
func (c *Client) RenewSelf(ctx context.Context) (time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.address+"/v1/auth/token/renew-self", nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("X-Vault-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, c.apiError("renew-self", resp)
	}
	var out struct {
		Auth struct {
			LeaseDuration int `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return time.Time{}, fmt.Errorf("decode response: %w", err)
	}
	return time.Now().Add(time.Duration(out.Auth.LeaseDuration) * time.Second), nil
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

// CapabilitiesSelf reports the capabilities the client's own token holds on
// path (e.g. "secret/data/orchestrator/foo"), via the unprivileged
// sys/capabilities-self endpoint. Unlike a write/read probe, this requires
// no side effects and works even for a path that doesn't exist yet — OpenBao
// evaluates glob policies against the literal path regardless.
func (c *Client) CapabilitiesSelf(ctx context.Context, path string) ([]string, error) {
	body, _ := json.Marshal(map[string]any{"paths": []string{path}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.address+"/v1/sys/capabilities-self", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.apiError("capabilities-self", resp)
	}
	var out map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	// The response keys the result by the queried path; "capabilities" is a
	// convenience alias present when a single path was queried.
	raw, ok := out[path]
	if !ok {
		raw, ok = out["capabilities"]
	}
	if !ok {
		return nil, fmt.Errorf("capabilities-self: no capabilities returned for %q", path)
	}
	var caps []string
	if err := json.Unmarshal(raw, &caps); err != nil {
		return nil, fmt.Errorf("decode capabilities: %w", err)
	}
	return caps, nil
}

// CheckSecretAccess verifies the token has the capabilities the orchestrator
// needs to manage secrets under pathPrefix (e.g. "orchestrator/"): create and
// update on the KV data path (writes), read on the data path, and delete on
// the KV metadata path. It probes a synthetic, never-written path under the
// prefix — policies are evaluated by glob match, not existence — so this is
// safe to call before any real secret exists.
//
// On denial, the returned error names the missing capability and the policy
// path that needs to grant it, so the caller can act on it directly.
func (c *Client) CheckSecretAccess(ctx context.Context, pathPrefix string) error {
	probe := strings.TrimSuffix(pathPrefix, "/") + "/.capability-check"
	dataPath := fmt.Sprintf("%s/data/%s", c.mount, probe)
	metaPath := fmt.Sprintf("%s/metadata/%s", c.mount, probe)

	dataCaps, err := c.CapabilitiesSelf(ctx, dataPath)
	if err != nil {
		return fmt.Errorf("check write access: %w", err)
	}
	if !hasCapability(dataCaps, "create", "update") {
		return fmt.Errorf(
			"token cannot create/update secrets (checked %q, has %v) — add a policy granting"+
				" create and update on \"%s/data/%s*\"",
			dataPath, dataCaps, c.mount, pathPrefix)
	}
	if !hasCapability(dataCaps, "read") {
		return fmt.Errorf(
			"token cannot read secrets (checked %q, has %v) — add a policy granting"+
				" read on \"%s/data/%s*\"",
			dataPath, dataCaps, c.mount, pathPrefix)
	}

	metaCaps, err := c.CapabilitiesSelf(ctx, metaPath)
	if err != nil {
		return fmt.Errorf("check delete access: %w", err)
	}
	if !hasCapability(metaCaps, "delete") {
		return fmt.Errorf(
			"token cannot delete secrets (checked %q, has %v) — add a policy granting"+
				" delete on \"%s/metadata/%s*\"",
			metaPath, metaCaps, c.mount, pathPrefix)
	}
	return nil
}

// hasCapability reports whether caps contains any of want. Vault/OpenBao
// resolve a denied path to exactly ["deny"], and a root token to ["root"]
// (which trivially satisfies any want), so a plain membership check is
// sufficient — no special-casing of "deny" is needed.
func hasCapability(caps []string, want ...string) bool {
	if slices.Contains(caps, "root") {
		return true
	}
	for _, w := range want {
		if slices.Contains(caps, w) {
			return true
		}
	}
	return false
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
