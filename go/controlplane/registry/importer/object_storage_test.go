package importer_test

import (
	"strings"
	"testing"

	"github.com/purser/purser/go/controlplane/registry/importer"
)

// TestParseS3URI verifies bucket, key, and region extraction from an S3 URI.
// We clear AWS credential env vars so the public URL path is taken, giving
// deterministic output regardless of the test runner's environment.
func TestParseS3URI(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("PURSER_S3_REGION", "eu-west-1")

	src, err := importer.ParseObjectURI("s3://bucket/models/llama.gguf")
	if err != nil {
		t.Fatalf("ParseObjectURI: %v", err)
	}
	if src.Type != "s3" {
		t.Errorf("Type = %q, want s3", src.Type)
	}
	if src.Bucket != "bucket" {
		t.Errorf("Bucket = %q, want bucket", src.Bucket)
	}
	if src.Key != "models/llama.gguf" {
		t.Errorf("Key = %q, want models/llama.gguf", src.Key)
	}
	if src.Region != "eu-west-1" {
		t.Errorf("Region = %q, want eu-west-1", src.Region)
	}
}

// TestS3PublicURL verifies that when no AWS credentials are present the
// returned DownloadURL is the public virtual-hosted S3 URL.
func TestS3PublicURL(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("PURSER_S3_REGION", "")

	src, err := importer.ParseObjectURI("s3://my-bucket/models/llama.gguf")
	if err != nil {
		t.Fatalf("ParseObjectURI: %v", err)
	}
	want := "https://my-bucket.s3.us-east-1.amazonaws.com/models/llama.gguf"
	if src.DownloadURL != want {
		t.Errorf("DownloadURL = %q, want %q", src.DownloadURL, want)
	}
}

// TestS3PresignedURL verifies that when AWS credentials are present a
// pre-signed URL is generated (SigV4 query-string format).
func TestS3PresignedURL(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	t.Setenv("PURSER_S3_REGION", "us-east-1")

	src, err := importer.ParseObjectURI("s3://test-bucket/key.gguf")
	if err != nil {
		t.Fatalf("ParseObjectURI: %v", err)
	}
	if !strings.HasPrefix(src.DownloadURL, "https://test-bucket.s3.us-east-1.amazonaws.com/key.gguf?") {
		t.Errorf("pre-signed URL wrong prefix: %q", src.DownloadURL)
	}
	for _, param := range []string{
		"X-Amz-Algorithm=AWS4-HMAC-SHA256",
		"X-Amz-Credential=",
		"X-Amz-Date=",
		"X-Amz-Expires=3600",
		"X-Amz-SignedHeaders=host",
		"X-Amz-Signature=",
	} {
		if !strings.Contains(src.DownloadURL, param) {
			t.Errorf("pre-signed URL missing %q; got: %q", param, src.DownloadURL)
		}
	}
}

// TestParseGCSURI verifies bucket, key parsing and public-URL fallback for gs://.
func TestParseGCSURI(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	src, err := importer.ParseObjectURI("gs://my-gcs-bucket/weights/model.gguf")
	if err != nil {
		t.Fatalf("ParseObjectURI: %v", err)
	}
	if src.Type != "gcs" {
		t.Errorf("Type = %q, want gcs", src.Type)
	}
	if src.Bucket != "my-gcs-bucket" {
		t.Errorf("Bucket = %q, want my-gcs-bucket", src.Bucket)
	}
	if src.Key != "weights/model.gguf" {
		t.Errorf("Key = %q, want weights/model.gguf", src.Key)
	}
	wantURL := "https://storage.googleapis.com/my-gcs-bucket/weights/model.gguf"
	if src.DownloadURL != wantURL {
		t.Errorf("DownloadURL = %q, want %q", src.DownloadURL, wantURL)
	}
}

// TestParseAzureURI verifies container/key parsing and public-URL fallback
// for az:// when no account credentials are set.
func TestParseAzureURI(t *testing.T) {
	t.Setenv("AZURE_STORAGE_ACCOUNT", "")
	t.Setenv("AZURE_STORAGE_KEY", "")

	src, err := importer.ParseObjectURI("az://mycontainer/models/llama.gguf")
	if err != nil {
		t.Fatalf("ParseObjectURI: %v", err)
	}
	if src.Type != "azure" {
		t.Errorf("Type = %q, want azure", src.Type)
	}
	if src.Bucket != "mycontainer" {
		t.Errorf("Bucket (container) = %q, want mycontainer", src.Bucket)
	}
	if src.Key != "models/llama.gguf" {
		t.Errorf("Key = %q, want models/llama.gguf", src.Key)
	}
	if !strings.HasPrefix(src.DownloadURL, "https://") {
		t.Errorf("DownloadURL should start with https://, got %q", src.DownloadURL)
	}
	if !strings.Contains(src.DownloadURL, "mycontainer") {
		t.Errorf("DownloadURL should contain container name; got %q", src.DownloadURL)
	}
}

// TestParseUnsupportedScheme verifies that an unrecognized scheme returns an error.
func TestParseUnsupportedScheme(t *testing.T) {
	_, err := importer.ParseObjectURI("ftp://bucket/key")
	if err == nil {
		t.Error("expected error for ftp:// scheme, got nil")
	}
}
