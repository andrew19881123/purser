// Package importer provides clients for importing models from external ML
// registries into the Purser catalog.
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// AzureMLClient is a minimal, SDK-free client for the Azure ML model registry.
// It authenticates via OAuth2 client credentials flow (POST to
// https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token) and calls the
// Azure Resource Manager REST API to list model versions.
//
// No azure-sdk-for-go dependency is used; all HTTP calls use the standard
// library with a plain Bearer token.
type AzureMLClient struct {
	// SubscriptionID is the Azure subscription that owns the ML workspace.
	// Populated from PURSER_AZURE_SUBSCRIPTION_ID by NewAzureMLClient.
	SubscriptionID string
	// ResourceGroup is the resource group containing the ML workspace.
	// Populated from PURSER_AZURE_RESOURCE_GROUP by NewAzureMLClient.
	ResourceGroup string
	// Workspace is the Azure ML workspace name.
	// Populated from PURSER_AZURE_ML_WORKSPACE (or workspaceOverride) by
	// NewAzureMLClient.
	Workspace string
	// BaseURL is the Azure Resource Manager root URL.
	// Default: https://management.azure.com. Set PURSER_AZURE_ML_BASE_URL to
	// override (useful in tests).
	BaseURL string
	// TokenURL is the OAuth2 token endpoint.
	// Default: https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token.
	// Set PURSER_AZURE_TOKEN_URL to override (useful in tests).
	TokenURL string
	// HTTPClient is used for all outbound requests. Defaults to
	// http.DefaultClient; tests may swap it for a custom transport.
	HTTPClient *http.Client

	tenantID     string
	clientID     string
	clientSecret string
	accessToken  string
}

// AzureMLModelVersion is a single registered version of a model in an Azure ML
// workspace.
type AzureMLModelVersion struct {
	// Name is the version identifier as returned by the API (e.g. "1", "2").
	Name string
	// Version mirrors Name; callers may use either field.
	Version string
	// Description is the human-readable description attached to the version.
	Description string
	// ArtifactURI is the model artifact location — an azureml:// URI or an
	// https://... Blob URL as stored in properties.modelUri.
	ArtifactURI string
	// Stage is the deployment stage (e.g. "Production", "Staging").
	Stage string
}

// NewAzureMLClient constructs an AzureMLClient from environment variables.
//
// workspaceOverride, when non-empty, takes precedence over
// PURSER_AZURE_ML_WORKSPACE.
//
// Required env vars:
//   - PURSER_AZURE_SUBSCRIPTION_ID
//   - PURSER_AZURE_RESOURCE_GROUP
//   - PURSER_AZURE_ML_WORKSPACE (or supply workspaceOverride)
//
// Auth env vars (OAuth2 client credentials):
//   - PURSER_AZURE_TENANT_ID
//   - PURSER_AZURE_CLIENT_ID
//   - PURSER_AZURE_CLIENT_SECRET
//
// URL-override env vars (for tests / custom endpoints):
//   - PURSER_AZURE_ML_BASE_URL  (default: https://management.azure.com)
//   - PURSER_AZURE_TOKEN_URL    (default: derived from tenant ID)
func NewAzureMLClient(workspaceOverride string) (*AzureMLClient, error) {
	subID := os.Getenv("PURSER_AZURE_SUBSCRIPTION_ID")
	if subID == "" {
		return nil, fmt.Errorf("PURSER_AZURE_SUBSCRIPTION_ID not set")
	}
	rg := os.Getenv("PURSER_AZURE_RESOURCE_GROUP")
	if rg == "" {
		return nil, fmt.Errorf("PURSER_AZURE_RESOURCE_GROUP not set")
	}
	workspace := os.Getenv("PURSER_AZURE_ML_WORKSPACE")
	if workspaceOverride != "" {
		workspace = workspaceOverride
	}
	if workspace == "" {
		return nil, fmt.Errorf("workspace not set: provide workspace in the request body or set PURSER_AZURE_ML_WORKSPACE")
	}

	tenantID := os.Getenv("PURSER_AZURE_TENANT_ID")
	clientID := os.Getenv("PURSER_AZURE_CLIENT_ID")
	clientSecret := os.Getenv("PURSER_AZURE_CLIENT_SECRET")

	baseURL := os.Getenv("PURSER_AZURE_ML_BASE_URL")
	if baseURL == "" {
		baseURL = "https://management.azure.com"
	}

	tokenURL := os.Getenv("PURSER_AZURE_TOKEN_URL")
	if tokenURL == "" && tenantID != "" {
		tokenURL = fmt.Sprintf(
			"https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)
	}

	return &AzureMLClient{
		SubscriptionID: subID,
		ResourceGroup:  rg,
		Workspace:      workspace,
		BaseURL:        baseURL,
		TokenURL:       tokenURL,
		HTTPClient:     http.DefaultClient,
		tenantID:       tenantID,
		clientID:       clientID,
		clientSecret:   clientSecret,
	}, nil
}

// getAccessToken performs the OAuth2 client credentials flow and stores the
// resulting bearer token in c.accessToken.
//
// Token endpoint: POST https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token
// with grant_type=client_credentials, scope=https://management.azure.com/.default.
func (c *AzureMLClient) getAccessToken(ctx context.Context) error {
	if c.tenantID == "" || c.clientID == "" || c.clientSecret == "" {
		return fmt.Errorf(
			"OAuth2 credentials not configured: set PURSER_AZURE_TENANT_ID, " +
				"PURSER_AZURE_CLIENT_ID, PURSER_AZURE_CLIENT_SECRET")
	}
	tokenURL := c.TokenURL
	if tokenURL == "" {
		tokenURL = fmt.Sprintf(
			"https://login.microsoftonline.com/%s/oauth2/v2.0/token", c.tenantID)
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"scope":         {"https://management.azure.com/.default"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	hc := c.httpClient()
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token endpoint returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("empty access_token in OAuth2 response")
	}
	c.accessToken = tr.AccessToken
	return nil
}

// ListModelVersions returns all registered versions of modelName from the Azure
// ML workspace. Versions are returned in the order the API provides them
// (Azure ML defaults to newest-version-first).
//
// API called:
//
//	GET https://management.azure.com/subscriptions/{sub}/resourceGroups/{rg}/
//	    providers/Microsoft.MachineLearningServices/workspaces/{ws}/
//	    models/{name}/versions?api-version=2023-04-01
func (c *AzureMLClient) ListModelVersions(ctx context.Context, modelName string) ([]AzureMLModelVersion, error) {
	if c.accessToken == "" {
		if err := c.getAccessToken(ctx); err != nil {
			return nil, fmt.Errorf("authenticate: %w", err)
		}
	}

	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = "https://management.azure.com"
	}
	apiURL := fmt.Sprintf(
		"%s/subscriptions/%s/resourceGroups/%s/providers/"+
			"Microsoft.MachineLearningServices/workspaces/%s/models/%s/versions",
		strings.TrimRight(baseURL, "/"),
		c.SubscriptionID, c.ResourceGroup, c.Workspace, modelName,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	q := req.URL.Query()
	q.Set("api-version", "2023-04-01")
	req.URL.RawQuery = q.Encode()

	hc := c.httpClient()
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list versions returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var result struct {
		Value []struct {
			Name       string `json:"name"`
			Properties struct {
				ModelUri    string `json:"modelUri"`
				Stage       string `json:"stage"`
				Description string `json:"description"`
			} `json:"properties"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode versions response: %w", err)
	}

	versions := make([]AzureMLModelVersion, 0, len(result.Value))
	for _, v := range result.Value {
		versions = append(versions, AzureMLModelVersion{
			Name:        v.Name,
			Version:     v.Name,
			Description: v.Properties.Description,
			ArtifactURI: v.Properties.ModelUri,
			Stage:       v.Properties.Stage,
		})
	}
	return versions, nil
}

// LatestVersion selects the best version from a list:
//   - the first Production-stage version, if any exists;
//   - otherwise, the first version overall (Azure ML returns newest-first).
//
// Returns false when versions is empty.
func LatestVersion(versions []AzureMLModelVersion) (AzureMLModelVersion, bool) {
	if len(versions) == 0 {
		return AzureMLModelVersion{}, false
	}
	for _, v := range versions {
		if v.Stage == "Production" {
			return v, true
		}
	}
	return versions[0], true
}

// httpClient returns the configured HTTP client or http.DefaultClient.
func (c *AzureMLClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}
