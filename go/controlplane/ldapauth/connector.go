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
	"sync"
	"time"

	ldap "github.com/go-ldap/ldap/v3"
)

// UserInfo is the result of a successful LDAP authentication.
type UserInfo struct {
	DN     string   // Full distinguished name
	Email  string   // Mail attribute
	Groups []string // Group names matching GroupAttribute
	Role   string   // Resolved Purser role from GroupMappings
}

// ErrInvalidCredentials is returned when the username/password is incorrect.
var ErrInvalidCredentials = errors.New("ldap: invalid credentials")

// ErrLDAPUnavailable is returned when the LDAP server cannot be reached.
var ErrLDAPUnavailable = errors.New("ldap: server unreachable")

// ErrNoGroupMapping is returned when no group matches a known Purser role.
var ErrNoGroupMapping = errors.New("ldap: no matching group mapping")

type cacheEntry struct {
	info      UserInfo
	expiresAt time.Time
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

	role := c.resolveRole(groups)
	info := &UserInfo{DN: userDN, Email: email, Groups: groups, Role: role}
	c.putCache(username, password, *info)
	return info, nil
}

func (c *Connector) searchGroups(conn *ldap.Conn, userDN string) ([]string, error) {
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
	groups := make([]string, 0, len(result.Entries))
	for _, e := range result.Entries {
		if name := e.GetAttributeValue(c.cfg.GroupAttribute); name != "" {
			groups = append(groups, name)
		}
	}
	return groups, nil
}

func (c *Connector) resolveRole(groups []string) string {
	// Priority: admin > viewer > inference
	priority := map[string]int{"admin": 3, "viewer": 2, "inference": 1}
	best := ""
	for _, g := range groups {
		if role, ok := c.cfg.GroupMappings[g]; ok {
			if priority[role] > priority[best] {
				best = role
			}
		}
	}
	return best
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
