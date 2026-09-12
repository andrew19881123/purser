package contract

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
)

type Feature struct {
	Name    string   `json:"name"`
	Routes  []string `json:"routes"`
	OpenAPI bool     `json:"openapi"`
	Client  []string `json:"client"`
	Page    *string  `json:"page"`
	Nav     *string  `json:"nav"`
	Docs    *string  `json:"docs"`
	Perm    *string  `json:"perm"`
	Gated   bool     `json:"gated"`
}

func Load(path string) ([]Feature, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fs []Feature
	if err := json.Unmarshal(b, &fs); err != nil {
		return nil, err
	}
	return fs, nil
}

// routeRe matches: s.mux.HandleFunc("GET /api/v1/x", ...) and
// s.mux.Handle("POST /api/v1/y", ...) — capturing the "METHOD /path" literal.
var routeRe = regexp.MustCompile(`\.(?:HandleFunc|Handle)\("([A-Z]+ /[^"]+)"`)

// RegisteredRoutes extracts every route literal registered in server.go.
func RegisteredRoutes(serverGoPath string) ([]string, error) {
	b, err := os.ReadFile(serverGoPath)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range routeRe.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out, nil
}
