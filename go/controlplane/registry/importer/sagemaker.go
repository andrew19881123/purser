// Package importer provides clients for importing models from external registries
// into the Purser control plane without adding heavy SDK dependencies.
package importer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// SageMakerClient calls the SageMaker REST JSON API using SigV4 signing.
// Credentials are read from the standard AWS environment variables
// (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY). When credentials are absent the
// requests are sent unsigned — useful in tests backed by a plain httptest.Server.
type SageMakerClient struct {
	// Region is the AWS region. Default: "us-east-1".
	Region string
	// ModelGroup is the SageMaker model package group name. Required before any call.
	ModelGroup string
	// BaseURL overrides the default endpoint (https://api.sagemaker.{region}.amazonaws.com).
	// Set to a test httptest.Server URL in unit tests.
	BaseURL string
	// HTTPClient is used for outbound calls; http.DefaultClient is used when nil.
	HTTPClient *http.Client
}

// ModelPackage is one approved version inside a SageMaker model package group.
type ModelPackage struct {
	ModelPackageArn         string
	ModelPackageGroupName   string
	ModelPackageVersion     int
	CreationTime            time.Time
	ApprovalStatus          string // "Approved"
	ModelPackageDescription string
	// ModelDataURL is the S3 URI of the model artifact,
	// taken from InferenceSpecification.Containers[0].ModelDataUrl.
	ModelDataURL string
}

// NewSageMakerClient builds a client from the runtime environment:
//
//   - PURSER_SAGEMAKER_REGION      — AWS region (default "us-east-1")
//   - PURSER_SAGEMAKER_MODEL_GROUP — default model package group
//   - PURSER_SAGEMAKER_BASE_URL    — endpoint override for tests
func NewSageMakerClient() *SageMakerClient {
	region := os.Getenv("PURSER_SAGEMAKER_REGION")
	if region == "" {
		region = "us-east-1"
	}
	return &SageMakerClient{
		Region:     region,
		ModelGroup: os.Getenv("PURSER_SAGEMAKER_MODEL_GROUP"),
		BaseURL:    os.Getenv("PURSER_SAGEMAKER_BASE_URL"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *SageMakerClient) endpoint() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.sagemaker." + c.Region + ".amazonaws.com"
}

// ListApprovedModelPackages returns all Approved packages in c.ModelGroup,
// sorted by creation time descending (newest first). Each package is fully
// described (ModelDataURL populated from DescribeModelPackage).
func (c *SageMakerClient) ListApprovedModelPackages(ctx context.Context) ([]ModelPackage, error) {
	reqBody, err := json.Marshal(struct {
		ModelPackageGroupName string `json:"ModelPackageGroupName"`
		ModelApprovalStatus   string `json:"ModelApprovalStatus"`
		SortBy                string `json:"SortBy"`
		SortOrder             string `json:"SortOrder"`
	}{
		ModelPackageGroupName: c.ModelGroup,
		ModelApprovalStatus:   "Approved",
		SortBy:                "CreationTime",
		SortOrder:             "Descending",
	})
	if err != nil {
		return nil, fmt.Errorf("sagemaker: encode ListModelPackages: %w", err)
	}

	raw, err := c.doRequest(ctx, c.endpoint()+"/ListModelPackages", reqBody)
	if err != nil {
		return nil, fmt.Errorf("sagemaker: ListModelPackages: %w", err)
	}

	var lr struct {
		ModelPackageSummaryList []struct {
			ModelPackageArn         string    `json:"ModelPackageArn"`
			ModelPackageGroupName   string    `json:"ModelPackageGroupName"`
			ModelPackageVersion     int       `json:"ModelPackageVersion"`
			CreationTime            time.Time `json:"CreationTime"`
			ModelApprovalStatus     string    `json:"ModelApprovalStatus"`
			ModelPackageDescription string    `json:"ModelPackageDescription"`
		} `json:"ModelPackageSummaryList"`
	}
	if err := json.Unmarshal(raw, &lr); err != nil {
		return nil, fmt.Errorf("sagemaker: decode ListModelPackages response: %w", err)
	}

	packages := make([]ModelPackage, 0, len(lr.ModelPackageSummaryList))
	for _, s := range lr.ModelPackageSummaryList {
		pkg, err := c.describeModelPackage(ctx, s.ModelPackageArn)
		if err != nil {
			return nil, err
		}
		pkg.ModelPackageVersion = s.ModelPackageVersion
		pkg.CreationTime = s.CreationTime
		pkg.ApprovalStatus = s.ModelApprovalStatus
		if pkg.ModelPackageDescription == "" {
			pkg.ModelPackageDescription = s.ModelPackageDescription
		}
		packages = append(packages, *pkg)
	}

	// Sort newest-first (defensive; we already request Descending from the API).
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].CreationTime.After(packages[j].CreationTime)
	})

	return packages, nil
}

// describeModelPackage fetches full details for a package by ARN or versioned name.
// The ModelDataURL is extracted from InferenceSpecification.Containers[0].ModelDataUrl.
func (c *SageMakerClient) describeModelPackage(ctx context.Context, arn string) (*ModelPackage, error) {
	reqBody, err := json.Marshal(struct {
		ModelPackageName string `json:"ModelPackageName"`
	}{ModelPackageName: arn})
	if err != nil {
		return nil, fmt.Errorf("sagemaker: encode DescribeModelPackage: %w", err)
	}

	raw, err := c.doRequest(ctx, c.endpoint()+"/DescribeModelPackage", reqBody)
	if err != nil {
		return nil, fmt.Errorf("sagemaker: DescribeModelPackage: %w", err)
	}

	var dr struct {
		ModelPackageArn         string `json:"ModelPackageArn"`
		ModelPackageGroupName   string `json:"ModelPackageGroupName"`
		ModelPackageDescription string `json:"ModelPackageDescription"`
		InferenceSpecification  struct {
			Containers []struct {
				ModelDataUrl string `json:"ModelDataUrl"`
			} `json:"Containers"`
		} `json:"InferenceSpecification"`
	}
	if err := json.Unmarshal(raw, &dr); err != nil {
		return nil, fmt.Errorf("sagemaker: decode DescribeModelPackage response: %w", err)
	}

	pkg := &ModelPackage{
		ModelPackageArn:         dr.ModelPackageArn,
		ModelPackageGroupName:   dr.ModelPackageGroupName,
		ModelPackageDescription: dr.ModelPackageDescription,
	}
	if len(dr.InferenceSpecification.Containers) > 0 {
		pkg.ModelDataURL = dr.InferenceSpecification.Containers[0].ModelDataUrl
	}
	return pkg, nil
}

// doRequest makes a signed POST request to the SageMaker REST JSON API.
// When AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY are absent the request is
// sent without a signature (sufficient for test servers).
func (c *SageMakerClient) doRequest(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")

	if ak := os.Getenv("AWS_ACCESS_KEY_ID"); ak != "" {
		if sk := os.Getenv("AWS_SECRET_ACCESS_KEY"); sk != "" {
			sigV4Sign(req, body, ak, sk, c.Region, "sagemaker")
		}
	}

	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, data)
	}
	return data, nil
}

// GuessFamilyFromName infers a model family from a name and/or description by
// scanning for known architecture keywords (case-insensitive). Returns
// "unknown" when no keyword matches.
func GuessFamilyFromName(name, description string) string {
	lower := strings.ToLower(name + " " + description)
	for _, fam := range []string{
		"llama", "mistral", "mixtral", "falcon", "gemma",
		"qwen", "phi", "gpt", "bloom", "mpt", "yi",
	} {
		if strings.Contains(lower, fam) {
			return fam
		}
	}
	return "unknown"
}

// sigV4Sign adds AWS Signature Version 4 authorization headers to req:
//
//   - x-amz-date
//   - x-amz-content-sha256
//   - Authorization (AWS4-HMAC-SHA256)
//
// Signed headers: content-type, host, x-amz-content-sha256, x-amz-date.
// Service name for SageMaker is "sagemaker".
func sigV4Sign(req *http.Request, payload []byte, accessKey, secretKey, region, service string) {
	t := time.Now().UTC()
	dateTime := t.Format("20060102T150405Z")
	date := t.Format("20060102")

	h := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(h[:])

	req.Header.Set("x-amz-date", dateTime)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	host := req.URL.Host

	// Canonical request — headers must be sorted alphabetically, which they
	// already are: content-type < host < x-amz-content-sha256 < x-amz-date.
	canonicalHeaders := "content-type:" + req.Header.Get("Content-Type") + "\n" +
		"host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + dateTime + "\n"
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"

	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	canonicalReq := req.Method + "\n" +
		canonicalURI + "\n" +
		req.URL.RawQuery + "\n" +
		canonicalHeaders + "\n" +
		signedHeaders + "\n" +
		payloadHash

	// String to sign
	credentialScope := date + "/" + region + "/" + service + "/aws4_request"
	reqHash := sha256.Sum256([]byte(canonicalReq))
	stringToSign := "AWS4-HMAC-SHA256\n" + dateTime + "\n" + credentialScope + "\n" + hex.EncodeToString(reqHash[:])

	// Derived signing key: HMAC(HMAC(HMAC(HMAC("AWS4"+secret, date), region), service), "aws4_request")
	sigKey := sigHMAC([]byte("AWS4"+secretKey), date)
	sigKey = sigHMAC(sigKey, region)
	sigKey = sigHMAC(sigKey, service)
	sigKey = sigHMAC(sigKey, "aws4_request")

	sig := hex.EncodeToString(sigHMAC(sigKey, stringToSign))

	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+accessKey+"/"+credentialScope+
			", SignedHeaders="+signedHeaders+
			", Signature="+sig)
}

// sigHMAC returns HMAC-SHA256(key, data).
func sigHMAC(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}
