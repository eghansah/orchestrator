package webui

import (
	"crypto/tls"
	"fmt"
	"strings"

	ldap "github.com/go-ldap/ldap/v3"
)

// LDAPConfig holds connection and search parameters for an LDAP/AD server.
// When Addr is empty, LDAP auth is disabled and local password auth is used.
type LDAPConfig struct {
	Addr           string // host:port, e.g. "ldap.corp.com:389" or "ldap.corp.com:636"
	UseTLS         bool   // dial with implicit TLS (LDAPS on port 636)
	Insecure       bool   // skip TLS certificate verification (for self-signed AD certs)
	BindDNTemplate string // how to form the bind DN; %s is replaced with the username
	// e.g. "%s@corp.com" (AD UPN) or "uid=%s,ou=users,dc=corp,dc=com" (OpenLDAP)
	BaseDN     string // search base for the optional group membership check
	UserFilter string // search filter with one %s for the username; only used when GroupDN is set
	GroupDN    string // optional: user must be a direct member of this group DN
}

// ldapAuthenticate verifies username+password against the configured LDAP/AD server.
// The supplied credentials are used directly for the bind — no service account is needed.
// If GroupDN is set, a post-bind self-search checks the user's memberOf attribute.
func ldapAuthenticate(cfg LDAPConfig, username, password string) error {
	if password == "" {
		return fmt.Errorf("empty password rejected")
	}

	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.Insecure} //nolint:gosec

	var conn *ldap.Conn
	var err error
	if cfg.UseTLS {
		conn, err = ldap.DialTLS("tcp", cfg.Addr, tlsCfg)
	} else {
		conn, err = ldap.Dial("tcp", cfg.Addr)
		if err == nil {
			// StartTLS failure is always fatal regardless of Insecure — Insecure only
			// controls certificate verification, not whether TLS is used at all.
			if startErr := conn.StartTLS(tlsCfg); startErr != nil {
				conn.Close()
				return fmt.Errorf("starttls: %w", startErr)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", cfg.Addr, err)
	}
	defer conn.Close()

	// Build the bind DN by substituting %s with the raw username.
	// For UPN-style templates ("%s@corp.com") the username goes in verbatim.
	// For full-DN templates ("uid=%s,ou=users,...") callers must ensure usernames
	// do not contain DN special characters, or use a strict allowlist upstream.
	tmpl := cfg.BindDNTemplate
	if tmpl == "" {
		tmpl = "%s"
	}
	bindDN := strings.ReplaceAll(tmpl, "%s", username)

	// Return a generic error to avoid leaking whether the account exists.
	if err := conn.Bind(bindDN, password); err != nil {
		return fmt.Errorf("authentication failed")
	}

	// Optional group membership check using a post-bind self-search.
	// The user is already authenticated at this point; we search as them.
	if cfg.GroupDN != "" {
		if cfg.BaseDN == "" || cfg.UserFilter == "" {
			return fmt.Errorf("ldap-base-dn and ldap-user-filter are required when ldap-group-dn is set")
		}
		filter := strings.ReplaceAll(cfg.UserFilter, "%s", ldap.EscapeFilter(username))
		req := ldap.NewSearchRequest(
			cfg.BaseDN,
			ldap.ScopeWholeSubtree,
			ldap.NeverDerefAliases,
			2, 0, false,
			filter,
			[]string{"memberOf"},
			nil,
		)
		result, err := conn.Search(req)
		if err != nil {
			return fmt.Errorf("group membership search: %w", err)
		}
		if len(result.Entries) == 0 {
			return fmt.Errorf("user %q not found in directory", username)
		}
		member := false
		for _, g := range result.Entries[0].GetAttributeValues("memberOf") {
			if strings.EqualFold(g, cfg.GroupDN) {
				member = true
				break
			}
		}
		if !member {
			return fmt.Errorf("user %q is not a member of required group", username)
		}
	}

	return nil
}
