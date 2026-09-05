package importer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/registry/importer"
	"github.com/purser/purser/go/controlplane/server"
)

// mockSageMaker builds an httptest.Server that handles /ListModelPackages and
// /DescribeModelPackage with canned responses.
func mockSageMaker(t *testing.T, listResp, descResp string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ListModelPackages", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write([]byte(listResp))
	})
	mux.HandleFunc("/DescribeModelPackage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		// Return the appropriate describe response based on the ARN in the body.
		var req struct {
			ModelPackageName string `json:"ModelPackageName"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = w.Write([]byte(descResp))
	})
	return httptest.NewServer(mux)
}

// TestSageMakerClient_ListApprovedPackages verifies that ListApprovedModelPackages
// returns both packages and that the result is sorted newest-first even when the
// mock API returns them oldest-first.
func TestSageMakerClient_ListApprovedPackages(t *testing.T) {
	// Mock returns two packages with the OLDER one first to verify client-side sort.
	oldTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newTime := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	listBody := `{
		"ModelPackageSummaryList": [
			{
				"ModelPackageArn": "arn:aws:sagemaker:us-east-1:123:model-package/llama-models/1",
				"ModelPackageGroupName": "llama-models",
				"ModelPackageVersion": 1,
				"CreationTime": "` + oldTime.Format(time.RFC3339) + `",
				"ModelApprovalStatus": "Approved",
				"ModelPackageDescription": "LLaMA 3 8B v1"
			},
			{
				"ModelPackageArn": "arn:aws:sagemaker:us-east-1:123:model-package/llama-models/2",
				"ModelPackageGroupName": "llama-models",
				"ModelPackageVersion": 2,
				"CreationTime": "` + newTime.Format(time.RFC3339) + `",
				"ModelApprovalStatus": "Approved",
				"ModelPackageDescription": "LLaMA 3 8B v2"
			}
		]
	}`

	// The describe response just returns the ARN it was asked about.
	// We use a counter to return different responses for each call.
	callCount := 0
	descResponses := []string{
		`{
			"ModelPackageArn": "arn:aws:sagemaker:us-east-1:123:model-package/llama-models/1",
			"ModelPackageGroupName": "llama-models",
			"ModelPackageDescription": "LLaMA 3 8B v1",
			"InferenceSpecification": {
				"Containers": [{"ModelDataUrl": "s3://bucket/models/v1/model.tar.gz"}]
			}
		}`,
		`{
			"ModelPackageArn": "arn:aws:sagemaker:us-east-1:123:model-package/llama-models/2",
			"ModelPackageGroupName": "llama-models",
			"ModelPackageDescription": "LLaMA 3 8B v2",
			"InferenceSpecification": {
				"Containers": [{"ModelDataUrl": "s3://bucket/models/v2/model.tar.gz"}]
			}
		}`,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ListModelPackages", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write([]byte(listBody))
	})
	mux.HandleFunc("/DescribeModelPackage", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		idx := callCount
		if idx >= len(descResponses) {
			idx = len(descResponses) - 1
		}
		callCount++
		_, _ = w.Write([]byte(descResponses[idx]))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := &importer.SageMakerClient{
		Region:     "us-east-1",
		ModelGroup: "llama-models",
		BaseURL:    ts.URL,
	}

	pkgs, err := client.ListApprovedModelPackages(context.Background())
	if err != nil {
		t.Fatalf("ListApprovedModelPackages: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("len(packages) = %d, want 2", len(pkgs))
	}

	// Newest (version 2) must come first.
	if pkgs[0].ModelPackageVersion != 2 {
		t.Errorf("packages[0].Version = %d, want 2 (newest first)", pkgs[0].ModelPackageVersion)
	}
	if pkgs[1].ModelPackageVersion != 1 {
		t.Errorf("packages[1].Version = %d, want 1", pkgs[1].ModelPackageVersion)
	}
	if !pkgs[0].CreationTime.After(pkgs[1].CreationTime) {
		t.Errorf("packages[0].CreationTime %v not after packages[1].CreationTime %v",
			pkgs[0].CreationTime, pkgs[1].CreationTime)
	}
	if pkgs[0].ApprovalStatus != "Approved" {
		t.Errorf("ApprovalStatus = %q, want Approved", pkgs[0].ApprovalStatus)
	}
}

// TestSageMakerClient_ExtractsS3URI verifies that the client correctly reads the
// ModelDataUrl from InferenceSpecification.Containers[0].
func TestSageMakerClient_ExtractsS3URI(t *testing.T) {
	const wantS3URI = "s3://my-bucket/models/llama3-8b-gguf/model.tar.gz"

	listBody := `{
		"ModelPackageSummaryList": [
			{
				"ModelPackageArn": "arn:aws:sagemaker:us-east-1:999:model-package/my-group/3",
				"ModelPackageGroupName": "my-group",
				"ModelPackageVersion": 3,
				"CreationTime": "2024-06-01T00:00:00Z",
				"ModelApprovalStatus": "Approved"
			}
		]
	}`
	descBody := `{
		"ModelPackageArn": "arn:aws:sagemaker:us-east-1:999:model-package/my-group/3",
		"ModelPackageGroupName": "my-group",
		"ModelPackageDescription": "LLaMA 3 8B GGUF",
		"InferenceSpecification": {
			"Containers": [
				{"ModelDataUrl": "` + wantS3URI + `"}
			]
		}
	}`

	ts := mockSageMaker(t, listBody, descBody)
	defer ts.Close()

	client := &importer.SageMakerClient{
		Region:     "us-east-1",
		ModelGroup: "my-group",
		BaseURL:    ts.URL,
	}

	pkgs, err := client.ListApprovedModelPackages(context.Background())
	if err != nil {
		t.Fatalf("ListApprovedModelPackages: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("len(packages) = %d, want 1", len(pkgs))
	}
	if pkgs[0].ModelDataURL != wantS3URI {
		t.Errorf("ModelDataURL = %q, want %q", pkgs[0].ModelDataURL, wantS3URI)
	}
}

// TestHandleImport_SageMaker is a full server integration test: it starts a mock
// SageMaker API, configures the Purser server via env vars, POSTs to
// /api/v1/models/import, and verifies 201 with the correct Source JSON.
func TestHandleImport_SageMaker(t *testing.T) {
	const (
		group   = "my-llama-models"
		wantARN = "arn:aws:sagemaker:us-east-1:123:model-package/my-llama-models/5"
		wantS3  = "s3://prod-bucket/my-llama-models/v5/model.tar.gz"
	)

	listBody := `{
		"ModelPackageSummaryList": [
			{
				"ModelPackageArn": "` + wantARN + `",
				"ModelPackageGroupName": "` + group + `",
				"ModelPackageVersion": 5,
				"CreationTime": "2024-09-01T00:00:00Z",
				"ModelApprovalStatus": "Approved",
				"ModelPackageDescription": "LLaMA 3 70B fine-tune"
			}
		]
	}`
	descBody := `{
		"ModelPackageArn": "` + wantARN + `",
		"ModelPackageGroupName": "` + group + `",
		"ModelPackageDescription": "LLaMA 3 70B fine-tune",
		"InferenceSpecification": {
			"Containers": [{"ModelDataUrl": "` + wantS3 + `"}]
		}
	}`

	smSrv := mockSageMaker(t, listBody, descBody)
	defer smSrv.Close()

	// Point the SageMaker client at the mock server via env override.
	t.Setenv("PURSER_SAGEMAKER_BASE_URL", smSrv.URL)
	t.Setenv("PURSER_SAGEMAKER_REGION", "us-east-1")

	reg := newTestReg(t)
	srv := server.New(reg, server.Config{})

	body := `{"source":"sagemaker","model_group":"` + group + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ModelID string `json:"model_id"`
		Source  struct {
			Type    string `json:"type"`
			ARN     string `json:"arn"`
			S3URI   string `json:"s3_uri"`
			Version int    `json:"version"`
		} `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; raw = %s", err, rec.Body.String())
	}

	wantID := group + "-v5"
	if resp.ModelID != wantID {
		t.Errorf("model_id = %q, want %q", resp.ModelID, wantID)
	}
	if resp.Source.Type != "sagemaker" {
		t.Errorf("source.type = %q, want sagemaker", resp.Source.Type)
	}
	if resp.Source.ARN != wantARN {
		t.Errorf("source.arn = %q, want %q", resp.Source.ARN, wantARN)
	}
	if resp.Source.S3URI != wantS3 {
		t.Errorf("source.s3_uri = %q, want %q", resp.Source.S3URI, wantS3)
	}
	if resp.Source.Version != 5 {
		t.Errorf("source.version = %d, want 5", resp.Source.Version)
	}

	// Verify the model was persisted to the registry.
	m, err := reg.GetModel(context.Background(), wantID)
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if m.Family != "llama" {
		t.Errorf("family = %q, want llama", m.Family)
	}
}

// TestHandleImport_SageMaker_NoGroup verifies that the import endpoint returns
// 400 when no model_group is provided in the request and the
// PURSER_SAGEMAKER_MODEL_GROUP env var is also unset.
func TestHandleImport_SageMaker_NoGroup(t *testing.T) {
	// Ensure the env var is unset for this test.
	t.Setenv("PURSER_SAGEMAKER_MODEL_GROUP", "")
	t.Setenv("PURSER_SAGEMAKER_BASE_URL", "")

	reg := newTestReg(t)
	srv := server.New(reg, server.Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/import",
		strings.NewReader(`{"source":"sagemaker"}`))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if errCode, _ := resp["error"].(string); errCode != "missing_model_group" {
		t.Errorf("error code = %q, want missing_model_group", errCode)
	}
}
