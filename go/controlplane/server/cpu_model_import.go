package server

// CPU model import: POST /api/v1/models/import/cpu
//
// Registers a model from a local GGUF file for CPU-only inference.
// The handler auto-fills the ModelSpec from a built-in lookup table of
// known small models that are practical on commodity CPU hardware.
//
// Request body:
//
//	{
//	    "model_id":    "tinyllama-1b",
//	    "gguf_path":   "/home/user/.purser/models/tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf",
//	    "context_max": 2048
//	}
//
// Returns the full registry.Model so the operator can verify the spec before
// deploying.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/purser/purser/go/controlplane/registry"
)

// cpuModelImportRequest is the body of POST /api/v1/models/import/cpu.
type cpuModelImportRequest struct {
	ModelID    string `json:"model_id"`
	GGUFPath   string `json:"gguf_path"`
	ContextMax uint32 `json:"context_max"`
}

// cpuKnownModel holds the spec metadata for models known to work well on
// commodity CPU hardware. Keyed by the canonical model slug.
type cpuKnownModel struct {
	Family    string
	Layers    int
	HiddenDim int
	ParamsB   float64 // billions
	SizeGBQ4  float64 // Q4_K_M size in GB
}

// cpuModelLookup maps canonical slugs to their specs.
// Keys are lower-cased, hyphen-normalised — see normalizeCPUModelID.
var cpuModelLookup = map[string]cpuKnownModel{
	"tinyllama-1.1b": {Family: "llama", Layers: 22, HiddenDim: 2048, ParamsB: 1.1, SizeGBQ4: 0.7},
	"llama3-8b":      {Family: "llama", Layers: 32, HiddenDim: 4096, ParamsB: 8.0, SizeGBQ4: 4.7},
	"llama3-1b":      {Family: "llama", Layers: 16, HiddenDim: 2048, ParamsB: 1.0, SizeGBQ4: 0.7},
	"phi3-mini":      {Family: "phi", Layers: 32, HiddenDim: 3072, ParamsB: 3.8, SizeGBQ4: 2.3},
	"gemma2-2b":      {Family: "gemma", Layers: 26, HiddenDim: 2304, ParamsB: 2.6, SizeGBQ4: 1.6},
}

// normalizeCPUModelID normalises a caller-supplied model_id to one of the
// canonical slugs in cpuModelLookup. It lower-cases, strips common suffixes
// like chat/instruct/v1.0 and maps common name variants:
//
//   - "tinyllama", "tiny-llama-1b", "tinyllama-1.1b-chat" → "tinyllama-1.1b"
//   - "llama-3-8b", "llama-3.1-8b" → "llama3-8b"
//   - "llama-3-1b", "llama-3.1-1b" → "llama3-1b"
//   - "phi-3-mini", "phi3-mini" → "phi3-mini"
//   - "gemma-2-2b", "gemma2-2b" → "gemma2-2b"
func normalizeCPUModelID(id string) string {
	s := strings.ToLower(id)
	// Strip common suffixes/qualifiers that do not affect the architecture spec.
	for _, suffix := range []string{
		"-chat", "-instruct", "-it", "-hf",
		"-v1.0", "-v2.0", "-v3.0",
		".q4_k_m", ".q8_0", ".gguf",
	} {
		s = strings.TrimSuffix(s, suffix)
	}
	// Exact match first.
	if _, ok := cpuModelLookup[s]; ok {
		return s
	}
	// Common aliases.
	switch {
	case s == "tinyllama" || strings.HasPrefix(s, "tinyllama-1"):
		return "tinyllama-1.1b"
	case strings.Contains(s, "llama") && strings.Contains(s, "8b"):
		return "llama3-8b"
	case strings.Contains(s, "llama") && strings.Contains(s, "1b"):
		return "llama3-1b"
	case strings.Contains(s, "phi") && strings.Contains(s, "mini"):
		return "phi3-mini"
	case strings.Contains(s, "gemma") && strings.Contains(s, "2b"):
		return "gemma2-2b"
	}
	return s
}

// handleImportCPUModel implements POST /api/v1/models/import/cpu.
func (s *Server) handleImportCPUModel(w http.ResponseWriter, r *http.Request) {
	var body cpuModelImportRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}

	if body.ModelID == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "model_id is required")
		return
	}
	if body.GGUFPath == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", "gguf_path is required")
		return
	}

	slug := normalizeCPUModelID(body.ModelID)
	spec, ok := cpuModelLookup[slug]
	if !ok {
		s.writeError(w, http.StatusUnprocessableEntity, "unknown_model",
			"model_id "+body.ModelID+" is not in the CPU lookup table; "+
				"known models: tinyllama-1.1b, llama3-8b, llama3-1b, phi3-mini, gemma2-2b")
		return
	}

	contextMax := body.ContextMax
	if contextMax == 0 {
		contextMax = 2048
	}

	// Build the source provenance blob.
	type cpuSourceBlob struct {
		Type       string  `json:"type"`
		GGUFPath   string  `json:"gguf_path"`
		ContextMax uint32  `json:"context_max"`
		Layers     int     `json:"layers"`
		HiddenDim  int     `json:"hidden_dim"`
		SizeGBQ4   float64 `json:"size_gb_q4"`
	}
	sourceBlob := cpuSourceBlob{
		Type:       "cpu",
		GGUFPath:   body.GGUFPath,
		ContextMax: contextMax,
		Layers:     spec.Layers,
		HiddenDim:  spec.HiddenDim,
		SizeGBQ4:   spec.SizeGBQ4,
	}
	sourceJSON, err := json.Marshal(sourceBlob)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "encode_source_failed", err.Error())
		return
	}

	// Use the canonical slug as the model ID for consistency.
	modelID := slug
	if _, err := s.reg.GetModel(r.Context(), modelID); err == nil {
		s.writeError(w, http.StatusConflict, "model_exists", "model already exists: "+modelID)
		return
	}

	m := &registry.Model{
		ID:           modelID,
		Family:       spec.Family,
		Architecture: "llm",
		ParamsTotalB: spec.ParamsB,
		Engine:       "cpu",
		Type:         "llm",
		Source:       sourceJSON,
	}
	if err := s.reg.CreateModel(r.Context(), m); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			s.writeError(w, http.StatusConflict, "model_exists", "model already exists: "+modelID)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "create_model_failed", err.Error())
		return
	}

	_ = s.reg.AppendAudit(r.Context(), &registry.AuditEntry{
		Actor:  actorFromRequest(r),
		Action: "model.imported.cpu",
		Target: modelID,
	})

	// Return the full model record so the operator can verify before deploying.
	stored, err := s.reg.GetModel(r.Context(), modelID)
	if err != nil {
		// Fallback: return what we built (shouldn't happen right after create).
		s.writeJSON(w, http.StatusCreated, m)
		return
	}
	s.writeJSON(w, http.StatusCreated, stored)
}
