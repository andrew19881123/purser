package ldapauth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	ldap "github.com/go-ldap/ldap/v3"
)

// UserInfo is the result of a successful LDAP authentication.
type UserInfo struct {
	DN       string   // Full distinguished name, e.g. "CN=alice,OU=users,DC=example,DC=com"
	Email    string   // Mail or userPrincipalName attribute
	Groups   []string // Group attribute values (e.g. CN names)
	GroupDNs []string // Full group distinguished names
	Role     string   // Resolved Purser role from GroupMappings / DNGroupMappings
}

// ErrInvalidCredentials is returned when the username/password is incorrect.
var ErrInvalidCredentials = errors.New("ldap: invalid credentials")

// ErrLDAPUnavailable is returned when the LDAP server cannot be reached.
var ErrLDAPUnavailable = errors.New("ldap: server unreachable")

// ErrNoGroupMapping is returned when group enforcement is active (at least one
// mapping is configured) but none of the user's groups match any mapping and no
// DefaultRole is set.
var ErrNoGroupMapping = errors.New("ldap: no matching group mapping")

type cacheEntry struct {
	info      UserInfo
	expiresAt time.Time
}

// ldapGroupEntry holds a group's distinguished name and its name attribute value.
type ldapGroupEntry struct {
	DN   string
	Name string
}

// Connector authenticates users via LDAP and resolves their Purser role.
type Connector struct {
	cfg   *Config
	mu    sync.RWMutex
	cache map[string]cacheEntry // key = sha256(username+":"+password)
}

// New creates a Connector from the given config.
// Returns nil if cfg is nil (LDAP not configured).
func New(cfg *Config) *Connector {
	if cfg == nil {
		return nil
	}
	return &Connector{
		cfg:   cfg,
		cache: make(map[string]cacheEntry),
	}
}

// Authenticate verifies username+password via LDAP bind.
// Returns cached results within CacheTTL.
// Returns ErrLDAPUnavailable if the server cannot be reached (callers may
// fall back to existing sessions but should not issue new credentials).
// Returns ErrNoGroupMapping when group enforcement is active (at least one
// mapping is configured) and none of the user's groups match any mapping and
// DefaultRole is not set.
func (c *Connector) Authenticate(ctx context.Context, username, password string) (*UserInfo, error) {
	// Cache check
	if info := c.fromCache(username, password); info != nil {
		return info, nil
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLDAPUnavailable, err)
	}
	defer conn.Close()

	// Service account bind
	if err := conn.Bind(c.cfg.BindDN, c.cfg.BindPassword); err != nil {
		return nil, fmt.Errorf("%w: service bind: %v", ErrLDAPUnavailable, err)
	}

	// Find user DN and email
	safeName := ldap.EscapeFilter(username)
	filter := fmt.Sprintf(c.cfg.UserFilter, safeName)
	searchReq := ldap.NewSearchRequest(
		c.cfg.UserBaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 1, 0, false,
		filter, []string{"dn", "mail", "userPrincipalName"}, nil,
	)
	result, err := conn.Search(searchReq)
	if err != nil {
		return nil, fmt.Errorf("%w: user search: %v", ErrLDAPUnavailable, err)
	}
	if len(result.Entries) == 0 {
		return nil, ErrInvalidCredentials
	}
	userDN := result.Entries[0].DN
	email := result.Entries[0].GetAttributeValue("mail")
	if email == "" {
		email = result.Entries[0].GetAttributeValue("userPrincipalName")
	}

	// Verify password via user bind
	if err := conn.Bind(userDN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("%w: user bind: %v", ErrLDAPUnavailable, err)
	}

	// Re-bind as service account for group search
	if err := conn.Bind(c.cfg.BindDN, c.cfg.BindPassword); err != nil {
		return nil, fmt.Errorf("%w: re-bind: %v", ErrLDAPUnavailable, err)
	}

	// Search groups
	groups, err := c.searchGroups(conn, userDN)
	if err != nil {
		return nil, err
	}

	// Resolve role
	role, matched := c.resolveRole(groups)
	if !matched {
		// Group enforcement is active but no group matched and no default role set.
		return nil, ErrNoGroupMapping
	}

	// Build UserInfo
	names := make([]string, 0, len(groups))
	dns := make([]string, 0, len(groups))
	for _, g := range groups {
		if g.Name != "" {
			names = append(names, g.Name)
		}
		if g.DN != "" {
			dns = append(dns, g.DN)
		}
	}

	info := &UserInfo{DN: userDN, Email: email, Groups: names, GroupDNs: dns, Role: role}
	c.putCache(username, password, *info)
	return info, nil
}

// CheckCache returns a cached UserInfo for the given credentials without
// triggering a live LDAP lookup. Returns nil on cache miss or when caching is
// disabled (CacheTTL <= 0). Used by the POST /api/v1/ldap/test endpoint to
// populate the cache_hit field.
func (c *Connector) CheckCache(username, password string) *UserInfo {
	return c.fromCache(username, password)
}

// searchGroups searches for all groups the given userDN belongs to.
// When cfg.NestedGroups is true it also searches for parent groups of the
// directly-found groups (up to 5 levels of recursion).
func (c *Connector) searchGroups(conn *ldap.Conn, userDN string) ([]ldapGroupEntry, error) {
	if c.cfg.GroupBaseDN == "" {
		return nil, nil
	}
	filter := fmt.Sprintf(c.cfg.GroupFilter, ldap.EscapeFilter(userDN))
	req := ldap.NewSearchRequest(
		c.cfg.GroupBaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		filter, []string{c.cfg.GroupAttribute}, nil,
	)
	result, err := conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("%w: group search: %v", ErrLDAPUnavailable, err)
	}

	seen := make(map[string]bool)
	groups := make([]ldapGroupEntry, 0, len(result.Entries))
	directDNs := make([]string, 0, len(result.Entries))
	for _, e := range result.Entries {
		if seen[e.DN] {
			continue
		}
		seen[e.DN] = true
		groups = append(groups, ldapGroupEntry{
			DN:   e.DN,
			Name: e.GetAttributeValue(c.cfg.GroupAttribute),
		})
		directDNs = append(directDNs, e.DN)
	}

	// Recursive nested-group lookup for non-AD servers.
	// (AD already resolves transitively via the LDAP_MATCHING_RULE_IN_CHAIN
	// OID in the default GroupFilter; NestedGroups is still safe to enable
	// for AD but will simply find no additional groups.)
	if c.cfg.NestedGroups && len(directDNs) > 0 {
		nested, err := c.searchParentGroups(conn, directDNs, seen, 0)
		if err != nil {
			return nil, err
		}
		groups = append(groups, nested...)
	}

	return groups, nil
}

// searchParentGroups recursively finds groups that contain any of the given
// group DNs as members. Recursion stops at depth 5 to prevent runaway queries
// on deeply-nested directory structures.
func (c *Connector) searchParentGroups(conn *ldap.Conn, groupDNs []string, seen map[string]bool, depth int) ([]ldapGroupEntry, error) {
	if depth >= 5 || len(groupDNs) == 0 {
		return nil, nil
	}
	var result []ldapGroupEntry
	var newDNs []string
	for _, dn := range groupDNs {
		filter := fmt.Sprintf("(member=%s)", ldap.EscapeFilter(dn))
		req := ldap.NewSearchRequest(
			c.cfg.GroupBaseDN,
			ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
			filter, []string{c.cfg.GroupAttribute}, nil,
		)
		sr, err := conn.Search(req)
		if err != nil {
			// Non-fatal: nested search may fail on servers without a member index.
			// Return partial results rather than failing the whole authentication.
			return result, nil
		}
		for _, e := range sr.Entries {
			if seen[e.DN] {
				continue
			}
			seen[e.DN] = true
			result = append(result, ldapGroupEntry{
				DN:   e.DN,
				Name: e.GetAttributeValue(c.cfg.GroupAttribute),
			})
			newDNs = append(newDNs, e.DN)
		}
	}
	deeper, err := c.searchParentGroups(conn, newDNs, seen, depth+1)
	if err != nil {
		return result, nil
	}
	return append(result, deeper...), nil
}

// resolveRole determines the highest-privilege Purser role for the given groups.
//
// Priority order: admin (3) > viewer (2) > inference (1).
//
// Lookup order:
//  1. DN-based mappings (DNGroupMappings) — most specific.
//  2. Name-based mappings (GroupMappings) — legacy / env-var-loaded.
//
// Returns (role, true) when a mapping is found, (DefaultRole, true) when no
// mapping matches but DefaultRole is set, or ("", false) when group enforcement
// is active and nothing matches.
//
// When neither GroupMappings nor DNGroupMappings are configured (both empty),
// group enforcement is considered inactive and ("", true) is returned — any
// authenticated user is allowed through without a role.
func (c *Connector) resolveRole(groups []ldapGroupEntry) (string, bool) {
	// If no mappings configured, group enforcement is inactive.
	if len(c.cfg.GroupMappings) == 0 && len(c.cfg.DNGroupMappings) == 0 {
		return "", true
	}

	priority := map[string]int{"admin": 3, "viewer": 2, "inference": 1}
	best := ""

	for _, g := range groups {
		// 1. DN-based mappings (full distinguished name match, case-insensitive).
		for _, dm := range c.cfg.DNGroupMappings {
			if strings.EqualFold(g.DN, dm.DN) {
				if priority[dm.Role] > priority[best] {
					best = dm.Role
				}
			}
		}
		// 2. Name-based mappings (group attribute value, case-sensitive).
		if role, ok := c.cfg.GroupMappings[g.Name]; ok {
			if priority[role] > priority[best] {
				best = role
			}
		}
	}

	if best != "" {
		return best, true
	}
	// No group matched any mapping.
	if c.cfg.DefaultRole != "" {
		return c.cfg.DefaultRole, true
	}
	return "", false
}

func (c *Connector) dial(_ context.Context) (*ldap.Conn, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: c.cfg.InsecureSkipVerify} //nolint:gosec
	if c.cfg.TLSCAFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(c.cfg.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool.AppendCertsFromPEM(pem)
		tlsCfg.RootCAs = pool
	}

	var conn *ldap.Conn
	var err error
	if c.cfg.StartTLS {
		conn, err = ldap.DialURL(c.cfg.URL)
		if err != nil {
			return nil, err
		}
		if err = conn.StartTLS(tlsCfg); err != nil {
			conn.Close()
			return nil, err
		}
	} else {
		conn, err = ldap.DialURL(c.cfg.URL, ldap.DialWithTLSConfig(tlsCfg))
		if err != nil {
			return nil, err
		}
	}
	return conn, nil
}

func cacheKey(username, password string) string {
	h := sha256.Sum256([]byte(username + ":" + password))
	return hex.EncodeToString(h[:])
}

func (c *Connector) fromCache(username, password string) *UserInfo {
	if c.cfg.CacheTTL <= 0 {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.cache[cacheKey(username, password)]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil
	}
	info := entry.info
	return &info
}

func (c *Connector) putCache(username, password string, info UserInfo) {
	if c.cfg.CacheTTL <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[cacheKey(username, password)] = cacheEntry{
		info:      info,
		expiresAt: time.Now().Add(c.cfg.CacheTTL),
	}
}
