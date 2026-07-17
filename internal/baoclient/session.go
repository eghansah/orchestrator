package baoclient

import (
	"context"
	"sync"
	"time"

	"github.com/eghansah/orchestrator/pkg/types"
)

// renewalSkew triggers a proactive token renewal this long before the lease
// actually expires, so a slow renew-self call doesn't race a hard expiry.
const renewalSkew = 60 * time.Second

// Session owns a live, auto-renewing OpenBao client derived from AppRole
// credentials (or a static token, for legacy configs where RoleID is empty).
// One Session per node is enough: only the current Raft leader ever reaches
// an OpenBao-touching RPC (they're all leader-gated), so only the leader's
// Session ever performs a real login or renewal.
type Session struct {
	mu       sync.Mutex
	cfg      types.OpenBaoConfig
	caBundle string

	client      *Client
	tokenExpiry time.Time
	renewable   bool
}

// Client returns a Client with a valid, non-expired token for cfg, logging in
// (AppRole) or renewing as needed. Cheap to call on every OpenBao-touching
// RPC: past the first check it's a no-op unless renewal or re-login is
// actually due. A config change (e.g. via SetOpenBaoConfig) is picked up
// immediately, forcing a fresh login on the next call.
func (s *Session) Client(ctx context.Context, cfg types.OpenBaoConfig, caBundle string) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cfg.RoleID == "" {
		// Legacy static-token path — no session state to maintain, matches
		// today's per-call construction exactly.
		return New(cfg.Address, cfg.Token, cfg.Mount, caBundle, cfg.InsecureSkipVerify), nil
	}

	if cfg != s.cfg || caBundle != s.caBundle {
		s.client = nil
		s.cfg = cfg
		s.caBundle = caBundle
	}

	if s.client != nil && time.Now().Before(s.tokenExpiry.Add(-renewalSkew)) {
		return s.client, nil
	}

	if s.client != nil && s.renewable {
		if newExpiry, err := s.client.RenewSelf(ctx); err == nil {
			s.tokenExpiry = newExpiry
			return s.client, nil
		}
		// Renewal failed (lease/max_ttl hit) — fall through to a fresh login.
	}

	token, leaseDuration, renewable, err := LoginAppRole(ctx, cfg.Address, cfg.AuthMount, caBundle, cfg.InsecureSkipVerify, cfg.RoleID, cfg.SecretID)
	if err != nil {
		return nil, err
	}
	s.client = New(cfg.Address, token, cfg.Mount, caBundle, cfg.InsecureSkipVerify)
	s.tokenExpiry = time.Now().Add(leaseDuration)
	s.renewable = renewable
	return s.client, nil
}
