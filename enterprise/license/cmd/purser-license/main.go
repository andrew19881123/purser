// Command purser-license is the maintainer-side tool for the Purser license
// gate. It never talks to the network; it only generates a signing keypair and
// mints / verifies signed license keys offline.
//
// # keygen — provision the trust root (run once)
//
//	purser-license keygen [--output-key] [-out FILE] [-force]
//
// Generates an ed25519 keypair and prints step-by-step onboarding instructions.
// With --output-key the private key is also written to purser-license-signing.key
// (already in .gitignore). Embed the printed public key as
// ProductionPublicKeyBase64 in enterprise/license/license.go before shipping.
//
// # sign — mint a license key
//
//	purser-license sign --licensee "Acme Corp" \
//	  --feature audit --feature billing \
//	  --expires 2027-01-01T00:00:00Z \
//	  --key purser-license-signing.key
//
// Reads the private key from --key or $PURSER_LICENSE_SIGNING_KEY and prints
// the signed key to stdout. Hand it to the customer who sets $PURSER_LICENSE_KEY.
//
// Every --feature / --features value is checked against the flags the control
// plane actually enforces (license.KnownFeatures) before anything is signed; an
// unrecognised string aborts the command with a suggestion. --allow-unknown-feature
// overrides the check for an unreleased feature and prints a loud warning.
// The authoritative flag list is website/docs/enterprise/license.md.
//
// # verify — inspect a license key
//
//	purser-license verify <key-string>
//	purser-license verify          # reads $PURSER_LICENSE_KEY
//	purser-license verify --dev <key-string>  # verify against the dev/test key
//
// Verifies the ed25519 signature and prints licensee, expiry, features, and
// whether the key is currently valid. Exits 0 for a valid signature, 1 for
// invalid (regardless of expiry — check "Valid now" in the output). Feature
// strings that match no enforced gate are flagged with a warning; this does not
// change the exit code, since the signature is genuinely valid.
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
		var vf verifyFailure
		if !errors.As(err, &vf) {
			fmt.Fprintln(os.Stderr, "purser-license:", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("expected a subcommand: keygen | sign | verify")
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:])
	case "sign":
		return sign(args[1:])
	case "verify":
		return verifyCmd(args[1:])
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
  purser-license keygen [--output-key] [-out FILE] [-force]
  purser-license sign --licensee NAME [--feature FEAT]... (--ttl DUR | --expires RFC3339) [--issued RFC3339] [--key FILE]
  purser-license verify [--dev] [<key-string>]

Subcommands:
  keygen   Generate a new ed25519 signing keypair (run once to provision trust root)
  sign     Mint and sign a license key for a customer
  verify   Verify a license key and print its contents

Environment:
  PURSER_LICENSE_SIGNING_KEY  path to (or inline base64 of) the private signing key
  PURSER_LICENSE_KEY          license key read by "verify" when no argument is given

Flags for sign:
  --licensee NAME    licensee / customer name (required)
  --feature FEAT     feature to grant; repeat for multiple (--feature audit --feature billing)
  --features a,b     comma-separated features (deprecated; use --feature)
  --expires RFC3339  expiry date, e.g. 2027-01-01T00:00:00Z
  --ttl DURATION     validity duration from issue time, e.g. 8760h
  --issued RFC3339   issue date (default: now)
  --key FILE         path to private signing key
  --allow-unknown-feature
                     sign a feature string that matches no enforced gate (for an
                     unreleased feature). Prints a loud warning; the resulting
                     key unlocks nothing for that string and cannot be corrected
                     without reissuing it.

Feature strings are validated against the gates the control plane enforces
before signing. The authoritative list, with the product name for each flag, is
website/docs/enterprise/license.md ("Feature gate reference").

Flags for verify:
  --dev   verify against the development key (DevPublicKeyBase64) instead of
          the production key (ProductionPublicKeyBase64)
`)
}

// verifyFailure is a sentinel error returned by verifyCmd when the license
// signature is invalid. main exits 1 but suppresses the redundant stderr
// message — the human-readable "License: INVALID" was already printed to
// stdout by verifyCmd.
type verifyFailure struct{ cause error }

func (verifyFailure) Error() string { return "" }

// verifyCmd implements "purser-license verify [--dev] [<key-string>]".
func verifyCmd(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dev := fs.Bool("dev", false, "verify against DevPublicKeyBase64 instead of ProductionPublicKeyBase64")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var keyStr string
	if fs.NArg() > 0 {
		keyStr = strings.TrimSpace(fs.Arg(0))
	} else {
		keyStr = strings.TrimSpace(os.Getenv(license.EnvVar))
	}
	if keyStr == "" {
		return fmt.Errorf("provide a license key as an argument or set $%s", license.EnvVar)
	}

	if *dev {
		// Temporarily override VerificationKey to the well-known dev key so
		// that callers can verify test/development-signed licenses against a
		// known-fixed trust root even when the binary was compiled with a
		// different production key.
		old := license.VerificationKey
		license.VerificationKey = license.DevVerificationKey
		defer func() { license.VerificationKey = old }()
	}

	lic, err := license.Verify(keyStr)
	if err != nil {
		var msg string
		switch {
		case errors.Is(err, license.ErrBadSignature):
			msg = "signature verification failed"
		case errors.Is(err, license.ErrMalformed):
			msg = "malformed license key"
		default:
			msg = err.Error()
		}
		fmt.Printf("License: INVALID\n  Error: %s\n", msg)
		return verifyFailure{err}
	}

	validNow := "yes"
	if !lic.ValidAt(time.Now()) {
		validNow = "no (expired or not yet valid)"
	}
	expires := "never"
	if !lic.Expires.IsZero() {
		expires = lic.Expires.Format("2006-01-02")
	}
	features := strings.Join(lic.Features, ", ")
	if features == "" {
		features = "(none)"
	}

	fmt.Printf("License: VALID\n")
	fmt.Printf("  Licensee:  %s\n", lic.Licensee)
	fmt.Printf("  Expires:   %s\n", expires)
	fmt.Printf("  Features:  %s\n", features)
	fmt.Printf("  Valid now: %s\n", validNow)

	// Keys minted before the signing guard existed can carry flags that no gate
	// checks. This is the path an operator reaches for when a feature they paid
	// for does nothing, so name the dead flags explicitly rather than leaving
	// them to diff the Features line against the docs by eye.
	//
	// The exit code deliberately stays 0: the signature IS valid, and the
	// documented contract is that 0 means a good signature. Scripts depend on
	// it, and an unlockable feature is not a forgery.
	if unknown := license.UnknownFeatures(lic.Features); len(unknown) > 0 {
		fmt.Println()
		fmt.Printf("  WARNING: %s\n", pluraliseFeatures(len(unknown)))
		for _, u := range unknown {
			line := fmt.Sprintf("    - %q", u)
			if hint := license.FeatureHint(u); hint != "" {
				line += " — " + hint
			}
			fmt.Println(line)
		}
		fmt.Println("  These flags unlock nothing. Because they are inside the signed payload,")
		fmt.Println("  the key must be reissued to fix them — it cannot be edited.")
		fmt.Println("  Authoritative flag list: website/docs/enterprise/license.md")
	}
	return nil
}

// pluraliseFeatures renders the warning headline for n dead feature flags.
func pluraliseFeatures(n int) string {
	if n == 1 {
		return "1 feature string matches no gate the control plane enforces:"
	}
	return fmt.Sprintf("%d feature strings match no gate the control plane enforces:", n)
}

// keygen generates an ed25519 keypair and prints step-by-step onboarding
// instructions for embedding the public key and distributing the private key.
// With --output-key the private key is written to purser-license-signing.key
// (the .gitignored standard filename).
func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", "", "custom path to write the private key (optional; omit to skip file write)")
	outputKey := fs.Bool("output-key", false, "write private key to purser-license-signing.key (gitignored)")
	force := fs.Bool("force", false, "overwrite the output file if it already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	privB64 := base64.StdEncoding.EncodeToString(priv)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	// Resolve output path: explicit -out wins over --output-key's default name.
	outPath := *out
	if outPath == "" && *outputKey {
		outPath = "purser-license-signing.key"
	}

	if outPath != "" {
		if !*force {
			if _, statErr := os.Stat(outPath); statErr == nil {
				return fmt.Errorf("refusing to overwrite existing %s (use -force)", outPath)
			}
		}
		if writeErr := os.WriteFile(outPath, []byte(privB64+"\n"), 0o600); writeErr != nil {
			return fmt.Errorf("write private key: %w", writeErr)
		}
	}

	fmt.Println("Step 1: Save this PRIVATE key securely (never commit or share it):")
	fmt.Println()
	fmt.Println("-----BEGIN PURSER LICENSE SIGNING KEY-----")
	fmt.Println(privB64)
	fmt.Println("-----END PURSER LICENSE SIGNING KEY-----")
	fmt.Println()
	if outPath != "" {
		fmt.Printf("  (also written to %s with mode 0600)\n", outPath)
		fmt.Println()
	}

	fmt.Println("Step 2: Embed this PUBLIC key in your Purser build by replacing")
	fmt.Println("        ProductionPublicKeyBase64 in enterprise/license/license.go:")
	fmt.Println()
	fmt.Printf("  const ProductionPublicKeyBase64 = \"%s\"\n", pubB64)
	fmt.Println()

	keyArg := "<path-to-signing.key>"
	if outPath != "" {
		keyArg = outPath
	}
	fmt.Printf("Step 3: Sign licenses with: purser-license sign --key %s \\\n", keyArg)
	fmt.Printf("          --licensee \"Acme Corp\" --expires 2027-01-01T00:00:00Z \\\n")
	fmt.Printf("          --feature audit --feature billing\n")
	fmt.Println()
	fmt.Println("        Valid --feature values are the gates the control plane enforces;")
	fmt.Println("        see website/docs/enterprise/license.md (\"Feature gate reference\").")
	fmt.Println("        sign rejects anything else, so a mistyped flag cannot reach a customer.")
	return nil
}

// multiFlag is a flag.Value that accumulates repeated --feature flags into a
// string slice (e.g. --feature audit --feature billing).
type multiFlag []string

func (f *multiFlag) String() string { return strings.Join(*f, ",") }
func (f *multiFlag) Set(v string) error {
	if v = strings.TrimSpace(v); v != "" {
		*f = append(*f, v)
	}
	return nil
}

// sign mints a signed license key from flags, using the private key from --key
// or $PURSER_LICENSE_SIGNING_KEY.
func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	licensee := fs.String("licensee", "", "licensee / customer name (required)")
	var featureMulti multiFlag
	fs.Var(&featureMulti, "feature", "feature to grant (repeatable: --feature audit --feature billing)")
	features := fs.String("features", "", "comma-separated feature entitlements (deprecated: use --feature)")
	ttl := fs.Duration("ttl", 0, "validity duration from --issued, e.g. 8760h (mutually exclusive with --expires)")
	expiresStr := fs.String("expires", "", "explicit expiry as RFC3339 (mutually exclusive with --ttl)")
	issuedStr := fs.String("issued", "", "issue time as RFC3339 (default: now)")
	keyPath := fs.String("key", "", "path to the private key file (overrides $"+signingKeyEnv+")")
	allowUnknown := fs.Bool("allow-unknown-feature", false,
		"sign feature strings that match no enforced gate (for unreleased features; prints a warning)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*licensee) == "" {
		return errors.New("--licensee is required")
	}

	issued := time.Now().UTC()
	if *issuedStr != "" {
		t, err := time.Parse(time.RFC3339, *issuedStr)
		if err != nil {
			return fmt.Errorf("parse --issued: %w", err)
		}
		issued = t.UTC()
	}

	var expires time.Time
	switch {
	case *expiresStr != "" && *ttl != 0:
		return errors.New("specify only one of --ttl or --expires")
	case *expiresStr != "":
		t, err := time.Parse(time.RFC3339, *expiresStr)
		if err != nil {
			return fmt.Errorf("parse --expires: %w", err)
		}
		expires = t.UTC()
	case *ttl > 0:
		expires = issued.Add(*ttl)
	default:
		return errors.New("provide --ttl or --expires")
	}

	priv, err := loadSigningKey(*keyPath)
	if err != nil {
		return err
	}

	// Merge --feature (repeatable) and --features (comma-separated, deprecated),
	// preserving order and deduplicating.
	seen := make(map[string]bool)
	var feats []string
	for _, f := range featureMulti {
		if f = strings.TrimSpace(f); f != "" && !seen[f] {
			seen[f] = true
			feats = append(feats, f)
		}
	}
	for _, f := range strings.Split(*features, ",") {
		if f = strings.TrimSpace(f); f != "" && !seen[f] {
			seen[f] = true
			feats = append(feats, f)
		}
	}

	// Guard: refuse to mint a key granting a flag no gate checks. Such a key
	// verifies, lists the flag in /api/v1/enterprise/status, and unlocks
	// nothing — and it cannot be patched, because the features array is inside
	// the signed payload. Failing silently here costs a key reissue at best and
	// a broken customer install at worst.
	if err := license.ValidateFeatures(feats); err != nil {
		if !*allowUnknown {
			return fmt.Errorf("%w\n\nNothing was signed. Correct the flag, or pass "+
				"--allow-unknown-feature to sign it deliberately (e.g. for an unreleased feature)", err)
		}
		warnUnvalidatedFeatures(feats)
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

// warnUnvalidatedFeatures prints a deliberately loud notice naming every flag
// that was signed without matching an enforced gate. It writes to STDERR so
// that stdout stays exactly one line — the key — and remains pipeable.
//
// The override exists so the guard can never become a reason to work around the
// tool, but a key minted this way is a commercial liability: it looks correct to
// everyone involved and grants nothing. Saying so at the moment of signing is
// the only cheap place to say it.
func warnUnvalidatedFeatures(feats []string) {
	unknown := license.UnknownFeatures(feats)
	if len(unknown) == 0 {
		return
	}
	quoted := make([]string, len(unknown))
	for i, u := range unknown {
		quoted[i] = fmt.Sprintf("%q", u)
	}
	w := os.Stderr
	fmt.Fprintln(w, "WARNING: ============================================================")
	fmt.Fprintf(w, "WARNING: --allow-unknown-feature was passed. Signed WITHOUT validation:\n")
	fmt.Fprintf(w, "WARNING:   %s\n", strings.Join(quoted, ", "))
	fmt.Fprintln(w, "WARNING:")
	fmt.Fprintln(w, "WARNING: No gate in the control plane checks these strings, so this key")
	fmt.Fprintln(w, "WARNING: will verify and appear correct in /api/v1/enterprise/status while")
	fmt.Fprintln(w, "WARNING: unlocking nothing. The features array is inside the ed25519-signed")
	fmt.Fprintln(w, "WARNING: payload: this key CANNOT be corrected in place: it must be reissued.")
	fmt.Fprintln(w, "WARNING: Do not send it to a customer unless you intend exactly this.")
	fmt.Fprintln(w, "WARNING: ============================================================")
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
		return nil, fmt.Errorf("no signing key: pass --key or set $%s", signingKeyEnv)
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
