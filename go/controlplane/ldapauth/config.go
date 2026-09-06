package ldapauth

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all LDAP connection and search parameters.
// Loaded from environment variables.
type Config struct {
	// URL is the LDAP server URL, e.g. "ldaps://ad.example.com:636".
	// Supports ldap:// (plain + optional StartTLS) and ldaps:// (TLS).
	URL string

	// BindDN is the service account distinguished name used to search.
	// e.g. "cn=purser-svc,ou=service-accounts,dc=example,dc=com"
	BindDN string

	// BindPassword is the service account password.
	BindPassword string

	// UserBaseDN is the base DN for user searches.
	// e.g. "ou=users,dc=example,dc=com"
	UserBaseDN string

	// UserFilter is the LDAP filter template for finding a user by username.
	// %s is replaced with the sanitized username.
	// Example: "(&(objectClass=user)(sAMAccountName=%s))"
	UserFilter string

	// GroupBaseDN is the base DN for group searches.
	// e.g. "ou=groups,dc=example,dc=com"
	GroupBaseDN string

	// GroupFilter is the LDAP filter template for finding groups by member DN.
	// %s is replaced with the user's full DN.
	// Example: "(&(objectClass=group)(member:1.2.840.113556.1.4.1941:=%s))"
	GroupFilter string

	// GroupAttribute is the attribute name used to extract the group name.
	// "cn" works for most LDAP servers; "sAMAccountName" for AD.
	GroupAttribute string

	// GroupMappings maps LDAP group names to Purser roles.
	// Example: {"Purser-Admins": "admin", "Purser-Viewers": "viewer"}
	GroupMappings map[string]string

	// CacheTTL is how long successful authentications are cached.
	// 0 disables caching (not recommended for production).
	CacheTTL time.Duration

	// TLSCAFile is the path to a PEM file with additional trusted CA certs.
	// Optional; if empty the system cert pool is used.
	TLSCAFile string

	// StartTLS enables upgrading an ldap:// connection to TLS via StartTLS.
	StartTLS bool

	// InsecureSkipVerify disables TLS certificate verification.
	// NEVER use in production.
	InsecureSkipVerify bool
}

// FromEnv loads LDAP configuration from environment variables.
// Returns nil if PURSER_LDAP_URL is not set (LDAP disabled).
func FromEnv() *Config {
	url := os.Getenv("PURSER_LDAP_URL")
	if url == "" {
		return nil // LDAP not configured
	}

	cfg := &Config{
		URL:                url,
		BindDN:             os.Getenv("PURSER_LDAP_BIND_DN"),
		BindPassword:       os.Getenv("PURSER_LDAP_BIND_PASSWORD"),
		UserBaseDN:         os.Getenv("PURSER_LDAP_USER_BASE_DN"),
		UserFilter:         getEnvOrDefault("PURSER_LDAP_USER_FILTER", "(&(objectClass=user)(sAMAccountName=%s))"),
		GroupBaseDN:        os.Getenv("PURSER_LDAP_GROUP_BASE_DN"),
		GroupFilter:        getEnvOrDefault("PURSER_LDAP_GROUP_FILTER", "(&(objectClass=group)(member:1.2.840.113556.1.4.1941:=%s))"),
		GroupAttribute:     getEnvOrDefault("PURSER_LDAP_GROUP_ATTRIBUTE", "cn"),
		TLSCAFile:          os.Getenv("PURSER_LDAP_TLS_CA_FILE"),
		StartTLS:           os.Getenv("PURSER_LDAP_STARTTLS") == "1",
		InsecureSkipVerify: os.Getenv("PURSER_LDAP_INSECURE_SKIP_VERIFY") == "1",
	}

	// Parse cache TTL
	if ttlStr := os.Getenv("PURSER_LDAP_CACHE_TTL_SECONDS"); ttlStr != "" {
		if n, err := strconv.Atoi(ttlStr); err == nil {
			cfg.CacheTTL = time.Duration(n) * time.Second
		}
	} else {
		cfg.CacheTTL = 5 * time.Minute // sensible default
	}

	// Parse group mappings: "GroupA=admin,GroupB=viewer"
	cfg.GroupMappings = make(map[string]string)
	if mappings := os.Getenv("PURSER_LDAP_GROUP_MAPPINGS"); mappings != "" {
		for _, m := range strings.Split(mappings, ",") {
			parts := strings.SplitN(strings.TrimSpace(m), "=", 2)
			if len(parts) == 2 {
				cfg.GroupMappings[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
	}

	return cfg
}

// Validate returns an error if required fields are missing.
func (c *Config) Validate() error {
	if c.URL == "" {
		return fmt.Errorf("ldap: URL is required")
	}
	if c.BindDN == "" {
		return fmt.Errorf("ldap: BindDN is required")
	}
	if c.UserBaseDN == "" {
		return fmt.Errorf("ldap: UserBaseDN is required")
	}
	return nil
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
