// Command purser-license is the maintainer-side tool for the Purser license
// gate. It never talks to the network; it only generates a signing keypair and
// mints signed license keys offline.
//
// # keygen — provision the trust root (run once)
//
//	purser-license keygen [-out license-signing.ed25519.key]
//
// Generates an ed25519 keypair. The PUBLIC key is printed to stdout — paste it
// into license.DevPublicKeyBase64 so stock builds trust your keys. The PRIVATE
// key is written to -out (default license-signing.ed25519.key), which the
// repo's .gitignore already excludes. NEVER commit the private key; store it in
// a secret manager.
//
// # sign — mint a license key
//
//	PURSER_LICENSE_SIGNING_KEY=license-signing.ed25519.key \
//	  purser-license sign -licensee "Acme Corp" -features audit,rbac -ttl 8760h
//
// Reads the private key from -key or $PURSER_LICENSE_SIGNING_KEY (a file path,
// or the base64 key material inline) and prints the signed key to stdout — hand
// it to the customer, who sets it as $PURSER_LICENSE_KEY.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/purser/purser/enterprise/license"
)

const signingKeyEnv = "PURSER_LICENSE_SIGNING_KEY"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "purser-license:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("expected a subcommand: keygen | sign")
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:])
	case "sign":
		return sign(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `purser-license — offline Purser license signer

Usage:
  purser-license keygen [-out FILE]
  purser-license sign -licensee NAME [-features a,b] (-ttl DUR | -expires RFC3339) [-issued RFC3339] [-key FILE]

Environment:
  PURSER_LICENSE_SIGNING_KEY  path to (or inline base64 of) the private key used by "sign"
`)
}

// keygen generates an ed25519 keypair, prints the public key, and writes the
// private key to a gitignored file with 0600 perms.
func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", "license-signing.ed25519.key", "path to write the PRIVATE key (gitignored; never commit)")
	force := fs.Bool("force", false, "overwrite the output file if it already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	if !*force {
		if _, err := os.Stat(*out); err == nil {
			return fmt.Errorf("refusing to overwrite existing %s (use -force)", *out)
		}
	}
	// base64(privateKey) so the file is a single copy-safe line.
	privB64 := base64.StdEncoding.EncodeToString(priv)
	if err := os.WriteFile(*out, []byte(privB64+"\n"), 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	pubB64 := base64.StdEncoding.EncodeToString(pub)
	fmt.Printf("public key (paste into license.DevPublicKeyBase64):\n\n    %s\n\n", pubB64)
	fmt.Printf("private key written to %s (0600) — NEVER commit this file.\n", *out)
	return nil
}

// sign mints a signed license key from flags, using the private key from -key
// or $PURSER_LICENSE_SIGNING_KEY.
func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	licensee := fs.String("licensee", "", "licensee / customer name (required)")
	features := fs.String("features", "", "comma-separated feature entitlements, e.g. audit,rbac")
	ttl := fs.Duration("ttl", 0, "validity duration from -issued, e.g. 8760h (mutually exclusive with -expires)")
	expiresStr := fs.String("expires", "", "explicit expiry as RFC3339 (mutually exclusive with -ttl)")
	issuedStr := fs.String("issued", "", "issue time as RFC3339 (default: now)")
	keyPath := fs.String("key", "", "path to the private key file (overrides $"+signingKeyEnv+")")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*licensee) == "" {
		return errors.New("-licensee is required")
	}

	issued := time.Now().UTC()
	if *issuedStr != "" {
		t, err := time.Parse(time.RFC3339, *issuedStr)
		if err != nil {
			return fmt.Errorf("parse -issued: %w", err)
		}
		issued = t.UTC()
	}

	var expires time.Time
	switch {
	case *expiresStr != "" && *ttl != 0:
		return errors.New("specify only one of -ttl or -expires")
	case *expiresStr != "":
		t, err := time.Parse(time.RFC3339, *expiresStr)
		if err != nil {
			return fmt.Errorf("parse -expires: %w", err)
		}
		expires = t.UTC()
	case *ttl > 0:
		expires = issued.Add(*ttl)
	default:
		return errors.New("provide -ttl or -expires")
	}

	priv, err := loadSigningKey(*keyPath)
	if err != nil {
		return err
	}

	var feats []string
	for _, f := range strings.Split(*features, ",") {
		if f = strings.TrimSpace(f); f != "" {
			feats = append(feats, f)
		}
	}

	key, err := license.Sign(priv, license.Payload{
		Licensee: *licensee,
		Features: feats,
		Issued:   issued,
		Expires:  expires,
	})
	if err != nil {
		return err
	}
	fmt.Println(key)
	return nil
}

// loadSigningKey resolves the ed25519 private key from an explicit path, or the
// $PURSER_LICENSE_SIGNING_KEY env var (a file path if it names a readable file,
// otherwise treated as inline base64 key material). The key material is
// standard-base64 of a 64-byte ed25519 private key.
func loadSigningKey(explicitPath string) (ed25519.PrivateKey, error) {
	source := explicitPath
	if source == "" {
		source = os.Getenv(signingKeyEnv)
	}
	if source == "" {
		return nil, fmt.Errorf("no signing key: pass -key or set $%s", signingKeyEnv)
	}

	material := source
	if info, err := os.Stat(source); err == nil && !info.IsDir() {
		raw, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("read signing key file: %w", err)
		}
		material = string(raw)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(material))
	if err != nil {
		return nil, fmt.Errorf("signing key is not valid base64: %w", err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signing key is %d bytes, want %d", len(decoded), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(decoded), nil
}
