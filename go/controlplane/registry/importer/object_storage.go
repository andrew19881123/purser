// Package importer resolves object-storage URIs (s3://, gs://, az://) into
// HTTPS download URLs that the Purser agent can fetch at deploy time. Actual
// downloads happen on the agent; the control plane only computes the URL.
//
// Credentials are read from environment variables at call time. When
// credentials are absent the public URL for the object is returned (works for
// public-access buckets/containers).
//
// No heavy cloud SDKs are imported. S3 uses a minimal AWS SigV4 pre-signer,
// GCS uses the GOOG4-RSA-SHA256 signed-URL algorithm with the RSA key from
// a service-account JSON file, and Azure uses an HMAC-SHA256 Shared Access
// Signature over the account key.
package importer

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// ObjectSource is the result of parsing and resolving an object-storage URI.
type ObjectSource struct {
	// Type is "s3", "gcs", or "azure".
	Type string `json:"type"`
	// Bucket is the S3/GCS bucket name or Azure container name.
	Bucket string `json:"bucket"`
	// Key is the object path within the bucket/container.
	Key string `json:"key"`
	// Region is the AWS region (S3 only). Empty for GCS and Azure.
	Region string `json:"region,omitempty"`
	// DownloadURL is the pre-signed or public HTTPS URL the agent should GET.
	DownloadURL string `json:"download_url"`
}

// ParseObjectURI parses an object-storage URI and returns a resolved
// ObjectSource. When environment credentials are present a pre-signed URL
// valid for 1 hour is generated. When credentials are absent the public URL
// is returned (suitable for public-access objects).
//
// Supported schemes: s3://, gs://, az://
func ParseObjectURI(uri string) (*ObjectSource, error) {
	switch {
	case strings.HasPrefix(uri, "s3://"):
		return parseS3(uri)
	case strings.HasPrefix(uri, "gs://"):
		return parseGCS(uri)
	case strings.HasPrefix(uri, "az://"):
		return parseAzure(uri)
	default:
		return nil, fmt.Errorf("importer: unsupported URI scheme (want s3://, gs://, or az://): %q", uri)
	}
}

// splitBucketKey splits "bucket/key/path" → ("bucket", "key/path").
// When there is no '/', key is empty.
func splitBucketKey(body string) (bucket, key string) {
	if idx := strings.IndexByte(body, '/'); idx >= 0 {
		return body[:idx], body[idx+1:]
	}
	return body, ""
}

// ─── S3 ──────────────────────────────────────────────────────────────────────

func parseS3(uri string) (*ObjectSource, error) {
	bucket, key := splitBucketKey(strings.TrimPrefix(uri, "s3://"))
	if bucket == "" {
		return nil, fmt.Errorf("importer: s3 URI missing bucket: %q", uri)
	}
	region := os.Getenv("PURSER_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	src := &ObjectSource{Type: "s3", Bucket: bucket, Key: key, Region: region}
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if accessKey == "" || secretKey == "" {
		// No credentials: return the public virtual-hosted URL.
		src.DownloadURL = fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucket, region, key)
		return src, nil
	}
	src.DownloadURL = sigV4PresignS3(bucket, key, region, accessKey, secretKey)
	return src, nil
}

// sigV4PresignS3 generates an AWS SigV4 pre-signed GET URL valid for 1 hour.
//
// Implementation follows the SigV4 query-string signing spec:
// https://docs.aws.amazon.com/general/latest/gr/sigv4-query-string-auth.html
func sigV4PresignS3(bucket, key, region, accessKey, secretKey string) string {
	now := time.Now().UTC()
	dateTime := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	host := bucket + ".s3." + region + ".amazonaws.com"
	credScope := date + "/" + region + "/s3/aws4_request"
	// Credential value: slashes must be percent-encoded per AWS rules.
	credValue := awsEncode(accessKey + "/" + credScope)

	// Canonical query string — parameters MUST appear in lexicographic order.
	canonicalQS := "X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=" + credValue +
		"&X-Amz-Date=" + dateTime +
		"&X-Amz-Expires=3600" +
		"&X-Amz-SignedHeaders=host"

	// Canonical request.  CanonicalHeaders ends with "\n"; the join adds
	// another "\n" before SignedHeaders, producing the required blank line.
	canonicalRequest := "GET\n" +
		"/" + awsEncodePath(key) + "\n" +
		canonicalQS + "\n" +
		"host:" + host + "\n" +
		"\n" +
		"host\n" +
		"UNSIGNED-PAYLOAD"

	// String to sign.
	stringToSign := "AWS4-HMAC-SHA256\n" +
		dateTime + "\n" +
		credScope + "\n" +
		sha256Hex([]byte(canonicalRequest))

	// Derived signing key: HMAC chain kSecret → kDate → kRegion → kService → kSigning.
	sigKey := hmacSHA256(
		hmacSHA256(
			hmacSHA256(
				hmacSHA256([]byte("AWS4"+secretKey), []byte(date)),
				[]byte(region),
			),
			[]byte("s3"),
		),
		[]byte("aws4_request"),
	)
	sig := hex.EncodeToString(hmacSHA256(sigKey, []byte(stringToSign)))
	return "https://" + host + "/" + awsEncodePath(key) + "?" + canonicalQS + "&X-Amz-Signature=" + sig
}

// ─── GCS ─────────────────────────────────────────────────────────────────────

func parseGCS(uri string) (*ObjectSource, error) {
	bucket, key := splitBucketKey(strings.TrimPrefix(uri, "gs://"))
	if bucket == "" {
		return nil, fmt.Errorf("importer: gs URI missing bucket: %q", uri)
	}
	src := &ObjectSource{Type: "gcs", Bucket: bucket, Key: key}
	credFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credFile == "" {
		// No credentials: return the public XML API URL.
		src.DownloadURL = fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucket, key)
		return src, nil
	}
	dl, err := gcsSignedURL(bucket, key, credFile)
	if err != nil {
		return nil, fmt.Errorf("importer: gcs sign: %w", err)
	}
	src.DownloadURL = dl
	return src, nil
}

// serviceAccountJSON is the subset of a Google service-account key file we
// need to produce a V4 signed URL.
type serviceAccountJSON struct {
	Type        string `json:"type"`
	PrivateKey  string `json:"private_key"`
	ClientEmail string `json:"client_email"`
}

// gcsSignedURL generates a GCS V4 signed URL (GOOG4-RSA-SHA256) valid for
// 1 hour using the RSA private key embedded in a service-account JSON file.
//
// Reference: https://cloud.google.com/storage/docs/access-control/signed-urls
func gcsSignedURL(bucket, key, credFile string) (string, error) {
	data, err := os.ReadFile(credFile)
	if err != nil {
		return "", fmt.Errorf("read credentials: %w", err)
	}
	var sa serviceAccountJSON
	if err := json.Unmarshal(data, &sa); err != nil {
		return "", fmt.Errorf("parse credentials: %w", err)
	}
	if sa.Type != "service_account" {
		return "", fmt.Errorf("unsupported credential type %q (want service_account)", sa.Type)
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("no PEM block found in private_key")
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}
	rsaKey, ok := raw.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("expected RSA private key, got %T", raw)
	}

	now := time.Now().UTC()
	dateTime := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	const host = "storage.googleapis.com"
	credScope := date + "/auto/storage/goog4_request"
	credValue := awsEncode(sa.ClientEmail + "/" + credScope)

	canonicalQS := "X-Goog-Algorithm=GOOG4-RSA-SHA256" +
		"&X-Goog-Credential=" + credValue +
		"&X-Goog-Date=" + dateTime +
		"&X-Goog-Expires=3600" +
		"&X-Goog-SignedHeaders=host"

	canonicalRequest := "GET\n" +
		"/" + bucket + "/" + awsEncodePath(key) + "\n" +
		canonicalQS + "\n" +
		"host:" + host + "\n" +
		"\n" +
		"host\n" +
		"UNSIGNED-PAYLOAD"

	stringToSign := "GOOG4-RSA-SHA256\n" +
		dateTime + "\n" +
		credScope + "\n" +
		sha256Hex([]byte(canonicalRequest))

	h := sha256.Sum256([]byte(stringToSign))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, h[:])
	if err != nil {
		return "", fmt.Errorf("rsa sign: %w", err)
	}
	return "https://" + host + "/" + bucket + "/" + awsEncodePath(key) +
		"?" + canonicalQS + "&X-Goog-Signature=" + hex.EncodeToString(sig), nil
}

// ─── Azure Blob Storage ───────────────────────────────────────────────────────

func parseAzure(uri string) (*ObjectSource, error) {
	container, key := splitBucketKey(strings.TrimPrefix(uri, "az://"))
	if container == "" {
		return nil, fmt.Errorf("importer: az URI missing container: %q", uri)
	}
	account := os.Getenv("AZURE_STORAGE_ACCOUNT")
	storageKey := os.Getenv("AZURE_STORAGE_KEY")
	src := &ObjectSource{Type: "azure", Bucket: container, Key: key}
	if account == "" || storageKey == "" {
		// No usable credentials: return a plain HTTPS URL (works for public access).
		if account == "" {
			account = "unknown"
		}
		src.DownloadURL = fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s",
			account, container, key)
		return src, nil
	}
	dl, err := azureSASURL(account, container, key, storageKey)
	if err != nil {
		return nil, fmt.Errorf("importer: azure sas: %w", err)
	}
	src.DownloadURL = dl
	return src, nil
}

// azureSASURL generates an Azure Blob Storage Shared Access Signature URL
// valid for 1 hour, signed with HMAC-SHA256 over the account key.
// Uses service version 2020-02-10.
//
// Reference: https://learn.microsoft.com/en-us/rest/api/storageservices/create-service-sas
func azureSASURL(account, container, key, storageKey string) (string, error) {
	now := time.Now().UTC()
	start := now.Format("2006-01-02T15:04:05Z")
	expiry := now.Add(time.Hour).Format("2006-01-02T15:04:05Z")
	const version = "2020-02-10"
	canonicalResource := "/blob/" + account + "/" + container + "/" + key
	// StringToSign for a service SAS on a blob (version 2020-02-10).
	// Fields in order: permissions, start, expiry, canonicalizedResource,
	// identifier, ip, protocol, version, signedResource, snapshotTime,
	// rscc, rscd, rsce, rscl, rsct.
	stringToSign := strings.Join([]string{
		"r",               // signedPermissions: read
		start,             // signedStart
		expiry,            // signedExpiry
		canonicalResource, // canonicalizedResource
		"",                // signedIdentifier
		"",                // signedIP
		"https",           // signedProtocol
		version,           // signedVersion
		"b",               // signedResource: blob
		"",                // signedSnapshotTime
		"",                // rscc (response content-cache-control)
		"",                // rscd (response content-disposition)
		"",                // rsce (response content-encoding)
		"",                // rscl (response content-language)
		"",                // rsct (response content-type)
	}, "\n")
	keyBytes, err := base64.StdEncoding.DecodeString(storageKey)
	if err != nil {
		return "", fmt.Errorf("decode storage key: %w", err)
	}
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf(
		"https://%s.blob.core.windows.net/%s/%s?sv=%s&st=%s&se=%s&sr=b&sp=r&spr=https&sig=%s",
		account, container, key,
		version,
		url.QueryEscape(start),
		url.QueryEscape(expiry),
		url.QueryEscape(sig),
	), nil
}

// ─── shared crypto / encoding helpers ────────────────────────────────────────

// awsEncode percent-encodes s using the AWS URI encoding rules: all characters
// except unreserved (A-Za-z0-9 - _ . ~) are encoded as %XX uppercase hex.
func awsEncode(s string) string {
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			buf.WriteByte(c)
		} else {
			fmt.Fprintf(&buf, "%%%02X", c)
		}
	}
	return buf.String()
}

// awsEncodePath encodes each path segment with awsEncode while preserving '/'.
func awsEncodePath(s string) string {
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = awsEncode(p)
	}
	return strings.Join(parts, "/")
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
