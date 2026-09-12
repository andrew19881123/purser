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

// routeRe matches one row of the declarative route table in
// go/controlplane/server/openapi_registry.go, e.g.
//
//	{Method: "GET", Path: "/api/v1/nodes", Tag: "Nodes", ...}
//
// capturing the method and path so they can be recombined into the canonical
// "METHOD /path" literal the manifest uses. The table is the single source of
// truth for both mux registration and OpenAPI generation (it replaced the
// hand-written s.mux.HandleFunc("METHOD /path", ...) block in server.go), so
// the contract harness reads route registration from it.
var routeRe = regexp.MustCompile(`\bMethod:\s*"([A-Z]+)",\s*Path:\s*"(/[^"]+)"`)

// RegisteredRoutes extracts every route registered by the control plane from
// the declarative route table (openapi_registry.go). The argument is the path
// to that file. It historically pointed at server.go, where the routes used to
// be registered inline; the routes have since moved into the table.
func RegisteredRoutes(routeTablePath string) ([]string, error) {
	b, err := os.ReadFile(routeTablePath)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range routeRe.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1]+" "+m[2])
	}
	sort.Strings(out)
	return out, nil
}
