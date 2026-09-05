// Package importer provides thin clients for fetching model metadata from
// external registries so the control-plane can auto-populate a model spec on
// import. Currently only the HuggingFace Hub is supported.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
)

// HFClient is a thin HuggingFace Hub API client. It uses the standard
// net/http client — no external SDK is required.
type HFClient struct {
	// HTTPClient is the underlying HTTP client. Defaults to http.DefaultClient
	// when nil.
	HTTPClient *http.Client
	// Token is the HuggingFace API token (Bearer auth). Leave empty for public
	// model access.
	Token string
	// BaseURL overrides the HuggingFace API base URL. Defaults to
	// "https://huggingface.co". Overriding this is primarily useful in tests
	// that point the client at an httptest.Server.
	BaseURL string
}

// NewHFClient returns a new HFClient with the given token. Pass an empty
// string for unauthenticated (public-model) access.
func NewHFClient(token string) *HFClient {
	return &HFClient{
		HTTPClient: http.DefaultClient,
		Token:      token,
		BaseURL:    "https://huggingface.co",
	}
}

// hfModelResponse is the relevant subset of GET /api/models/{repo}?blobs=true.
type hfModelResponse struct {
	ModelID  string      `json:"modelId"`
	Siblings []hfSibling `json:"siblings"`
	CardData *hfCardData `json:"cardData"`
}

type hfSibling struct {
	RFilename string `json:"rfilename"`
	Size      int64  `json:"size"`
}

type hfCardData struct {
	License string `json:"license"`
}

// GGUFFile is a single GGUF file entry from the HuggingFace Hub.
type GGUFFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Metadata is the auto-populated model spec extracted from the HuggingFace Hub.
type Metadata struct {
	// ModelID is the full "owner/name" repo identifier,
	// e.g. "meta-llama/Llama-3.1-8B-Instruct".
	ModelID string
	// Name is the last path component of ModelID,
	// e.g. "Llama-3.1-8B-Instruct".
	Name string
	// Family is the guessed model family derived from Name by case-insensitive
	// substring match (llama, mistral, phi, gemma, qwen). Empty when unknown.
	Family string
	// License is the SPDX identifier from the card metadata, if present.
	License string
	// GGUFFiles contains the GGUF files that match the filename pattern
	// supplied to FetchMetadata, sorted by their rfilename.
	GGUFFiles []GGUFFile
}

// FetchMetadata fetches model metadata from the HuggingFace Hub API and
// returns the parsed Metadata. It calls:
//
//	GET {BaseURL}/api/models/{repo}?blobs=true
//
// The filenamePattern argument (a path.Match glob, e.g. "*.Q4_K_M.gguf")
// filters the returned GGUFFiles. An empty pattern defaults to "*.gguf".
// Only the basename of each sibling's rfilename is matched, so patterns like
// "*.gguf" work correctly even when files are nested under sub-directories.
//
// Non-200 responses are returned as *HFError so callers can distinguish 404
// (not_found) from 401/403 (auth_required) and network failures.
func (c *HFClient) FetchMetadata(ctx context.Context, repo, revision, filenamePattern string) (*Metadata, error) {
	base := c.baseURL()
	url := fmt.Sprintf("%s/api/models/%s?blobs=true", base, repo)
	if revision != "" && revision != "main" {
		url += "&revision=" + revision
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("importer: build request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("importer: fetch HuggingFace metadata: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, &HFError{Code: http.StatusNotFound, Repo: repo}
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, &HFError{Code: resp.StatusCode, Repo: repo}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("importer: HuggingFace API returned %d for %q", resp.StatusCode, repo)
	}

	var hfResp hfModelResponse
	if err := json.NewDecoder(resp.Body).Decode(&hfResp); err != nil {
		return nil, fmt.Errorf("importer: decode HuggingFace response: %w", err)
	}

	if filenamePattern == "" {
		filenamePattern = "*.gguf"
	}

	var ggufFiles []GGUFFile
	for _, s := range hfResp.Siblings {
		// Match against the basename so patterns like "*.gguf" work for both
		// flat filenames and files nested under sub-directories.
		if matched, _ := path.Match(filenamePattern, path.Base(s.RFilename)); matched {
			ggufFiles = append(ggufFiles, GGUFFile{Name: s.RFilename, Size: s.Size})
		}
	}

	modelID := hfResp.ModelID
	if modelID == "" {
		modelID = repo
	}

	name := path.Base(modelID)
	family := guessFamily(name)

	var license string
	if hfResp.CardData != nil {
		license = hfResp.CardData.License
	}

	return &Metadata{
		ModelID:   modelID,
		Name:      name,
		Family:    family,
		License:   license,
		GGUFFiles: ggufFiles,
	}, nil
}

// ListGGUFFiles returns the GGUF files matching pattern for the given repo. It
// is a convenience wrapper around FetchMetadata for callers that only need the
// file list.
func (c *HFClient) ListGGUFFiles(ctx context.Context, repo, revision, filenamePattern string) ([]GGUFFile, error) {
	meta, err := c.FetchMetadata(ctx, repo, revision, filenamePattern)
	if err != nil {
		return nil, err
	}
	return meta.GGUFFiles, nil
}

func (c *HFClient) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://huggingface.co"
}

func (c *HFClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// guessFamily returns a well-known family name by case-insensitive substring
// match against the model name. Returns "" when no known family is found.
func guessFamily(name string) string {
	lower := strings.ToLower(name)
	for _, f := range []string{"llama", "mistral", "phi", "gemma", "qwen"} {
		if strings.Contains(lower, f) {
			return f
		}
	}
	return ""
}

// HFError represents a non-success HTTP response from the HuggingFace API.
// Callers inspect it via IsNotFound or IsAuthRequired rather than switching
// on the Code directly.
type HFError struct {
	Code int
	Repo string
}

func (e *HFError) Error() string {
	switch e.Code {
	case http.StatusNotFound:
		return fmt.Sprintf("HuggingFace repo not found: %s", e.Repo)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("private/gated model requires PURSER_HF_TOKEN: %s", e.Repo)
	default:
		return fmt.Sprintf("HuggingFace API error %d for repo %s", e.Code, e.Repo)
	}
}

// IsNotFound reports whether err is an *HFError with a 404 status.
func IsNotFound(err error) bool {
	var e *HFError
	return errors.As(err, &e) && e.Code == http.StatusNotFound
}

// IsAuthRequired reports whether err is an *HFError with a 401 or 403 status.
func IsAuthRequired(err error) bool {
	var e *HFError
	return errors.As(err, &e) && (e.Code == http.StatusUnauthorized || e.Code == http.StatusForbidden)
}
