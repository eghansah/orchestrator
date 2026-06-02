package registry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a Docker Registry HTTP API v2 client.
type Client struct {
	baseURL  string
	username string
	password string
	hc       *http.Client
}

// New creates a Client. url should be the registry root without trailing slash,
// e.g. "https://registry.example.com". Leave username/password empty for
// unauthenticated registries.
func New(registryURL, username, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(registryURL, "/"),
		username: username,
		password: password,
		hc: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
		},
	}
}

// bearerChallenge holds the parsed Www-Authenticate Bearer challenge.
type bearerChallenge struct {
	realm   string
	service string
}

func parseBearerChallenge(header string) (bearerChallenge, bool) {
	// Www-Authenticate: Bearer realm="https://auth.example.com/token",service="registry.example.com"
	if !strings.HasPrefix(header, "Bearer ") {
		return bearerChallenge{}, false
	}
	parts := strings.TrimPrefix(header, "Bearer ")
	c := bearerChallenge{}
	for _, kv := range strings.Split(parts, ",") {
		kv = strings.TrimSpace(kv)
		if eq := strings.Index(kv, "="); eq >= 0 {
			key := kv[:eq]
			val := strings.Trim(kv[eq+1:], `"`)
			switch key {
			case "realm":
				c.realm = val
			case "service":
				c.service = val
			}
		}
	}
	return c, c.realm != ""
}

// fetchToken exchanges credentials for a Bearer token at the given realm.
func (c *Client) fetchToken(ctx context.Context, challenge bearerChallenge, scope string) (string, error) {
	u, err := url.Parse(challenge.realm)
	if err != nil {
		return "", fmt.Errorf("parse realm: %w", err)
	}
	q := u.Query()
	if challenge.service != "" {
		q.Set("service", challenge.service)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.Token != "" {
		return tok.Token, nil
	}
	return tok.AccessToken, nil
}

// do performs an authenticated GET. It handles the 401→token→retry Bearer flow
// and falls back to Basic auth when the challenge is not Bearer.
func (c *Client) do(ctx context.Context, path, scope string, extraHeaders map[string]string) (*http.Response, error) {
	fullURL := c.baseURL + path

	newReq := func(authorization string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
		if err != nil {
			return nil, err
		}
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		return req, nil
	}

	// First attempt — no auth.
	req, err := newReq("")
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	resp.Body.Close()

	wwwAuth := resp.Header.Get("Www-Authenticate")

	// Try Bearer token flow.
	if challenge, ok := parseBearerChallenge(wwwAuth); ok {
		token, err := c.fetchToken(ctx, challenge, scope)
		if err != nil {
			return nil, fmt.Errorf("fetch token: %w", err)
		}
		req, err = newReq("Bearer " + token)
		if err != nil {
			return nil, err
		}
		return c.hc.Do(req)
	}

	// Fall back to Basic auth.
	if c.username != "" && strings.HasPrefix(wwwAuth, "Basic") {
		req, err = newReq("")
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(c.username, c.password)
		return c.hc.Do(req)
	}

	// No credentials configured — the registry may still serve public content.
	// Return the original 401 response so callers see the real HTTP status.
	return nil, fmt.Errorf("registry requires authentication but no credentials are configured")
}

// ListRepos returns all repository names by paginating GET /v2/_catalog.
func (c *Client) ListRepos(ctx context.Context) ([]string, error) {
	var all []string
	last := ""
	for {
		path := "/v2/_catalog?n=100"
		if last != "" {
			path += "&last=" + url.QueryEscape(last)
		}
		resp, err := c.do(ctx, path, "registry:catalog:*", nil)
		if err != nil {
			return nil, fmt.Errorf("catalog: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("catalog returned %d: %s", resp.StatusCode, body)
		}
		var page struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("parse catalog: %w", err)
		}
		all = append(all, page.Repositories...)
		if len(page.Repositories) < 100 {
			break
		}
		last = page.Repositories[len(page.Repositories)-1]
	}
	return all, nil
}

// ListTags returns all tags for the given repository.
func (c *Client) ListTags(ctx context.Context, repo string) ([]string, error) {
	scope := "repository:" + repo + ":pull"
	resp, err := c.do(ctx, "/v2/"+repo+"/tags/list", scope, nil)
	if err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tags returned %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse tags: %w", err)
	}
	return result.Tags, nil
}

// GetImageEnv returns the environment variables baked into an image tag.
// It fetches the manifest to find the config blob digest, then reads the config.
func (c *Client) GetImageEnv(ctx context.Context, repo, tag string) ([]string, error) {
	scope := "repository:" + repo + ":pull"
	manifestHeaders := map[string]string{
		"Accept": "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json",
	}

	// Fetch manifest.
	resp, err := c.do(ctx, "/v2/"+repo+"/manifests/"+tag, scope, manifestHeaders)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest returned %d: %s", resp.StatusCode, body)
	}

	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.Config.Digest == "" {
		return nil, fmt.Errorf("manifest has no config digest")
	}

	// Fetch config blob.
	resp, err = c.do(ctx, "/v2/"+repo+"/blobs/"+manifest.Config.Digest, scope, nil)
	if err != nil {
		return nil, fmt.Errorf("blob: %w", err)
	}
	body, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blob returned %d: %s", resp.StatusCode, body)
	}

	var config struct {
		Config struct {
			Env []string `json:"Env"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return config.Config.Env, nil
}
