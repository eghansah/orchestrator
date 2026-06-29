// Package meshd implements the per-node mesh manager: it owns the node's
// WireGuard identity, brings up the userland WireGuard device, and keeps its
// peer set in sync with the cluster's Raft state. See docs/mesh-network.md.
package meshd

import (
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// keyLen is the byte length of a WireGuard Curve25519 key (private or public).
const keyLen = 32

// Key is a 32-byte WireGuard Curve25519 key.
type Key [keyLen]byte

// GenerateKey returns a fresh, clamped WireGuard private key.
func GenerateKey() (Key, error) {
	var k Key
	if _, err := crand.Read(k[:]); err != nil {
		return Key{}, fmt.Errorf("read random: %w", err)
	}
	clamp(&k)
	return k, nil
}

// clamp applies the Curve25519 bit-clamping WireGuard requires of private keys.
func clamp(k *Key) {
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
}

// PublicKey derives the public key corresponding to this private key.
func (k Key) PublicKey() Key {
	pub, err := curve25519.X25519(k[:], curve25519.Basepoint)
	if err != nil {
		// X25519 only errors on a low-order/zero result, which a clamped random
		// key effectively never produces; treat it as fatal programmer error.
		panic("meshd: deriving public key: " + err.Error())
	}
	var out Key
	copy(out[:], pub)
	return out
}

// String returns the standard base64 encoding WireGuard uses for keys (the form
// stored in cluster state and shown to operators).
func (k Key) String() string {
	return base64.StdEncoding.EncodeToString(k[:])
}

// Hex returns the lowercase hex encoding the wireguard-go UAPI expects.
func (k Key) Hex() string {
	return hex.EncodeToString(k[:])
}

// ParseKey decodes a standard-base64 WireGuard key.
func ParseKey(s string) (Key, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return Key{}, fmt.Errorf("decode base64 key: %w", err)
	}
	if len(raw) != keyLen {
		return Key{}, fmt.Errorf("key is %d bytes, want %d", len(raw), keyLen)
	}
	var k Key
	copy(k[:], raw)
	return k, nil
}

// keyPath is the on-disk location of this node's private key.
func keyPath(dataDir string) string {
	return filepath.Join(dataDir, "mesh", "private.key")
}

// LoadOrCreateKey returns the node's persistent private key, generating and
// saving one (0600) on first use. The private key never leaves the node.
func LoadOrCreateKey(dataDir string) (Key, error) {
	path := keyPath(dataDir)
	if raw, err := os.ReadFile(path); err == nil {
		k, err := ParseKey(string(raw))
		if err != nil {
			return Key{}, fmt.Errorf("parse %s: %w", path, err)
		}
		return k, nil
	}
	k, err := GenerateKey()
	if err != nil {
		return Key{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Key{}, fmt.Errorf("create mesh dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(k.String()+"\n"), 0o600); err != nil {
		return Key{}, fmt.Errorf("write %s: %w", path, err)
	}
	return k, nil
}
