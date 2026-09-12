package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestGoldenPath exercises the full inference pipe end-to-end:
//
//	enroll agent → register + deploy model → chat completion through the gateway
//
// This is the "money shot" test: if this passes, every layer of the stack
// (CP registry, planner, orchestrator, gateway routing, mock agent) is wired
// together correctly.
func TestGoldenPath(t *testing.T) {
	s := Start(t)
	defer s.Stop()

	s.EnrollAgent(t)
	s.DeployModel(t, "tinyllama-1b")

	// POST a non-streaming chat completion through the gateway.
	body := `{"model":"tinyllama-1b","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req, err := http.NewRequest("POST", s.GatewayBase+"/v1/chat/completions", strings.NewReader(body)) //nolint:noctx
	if err != nil {
		t.Fatalf("TestGoldenPath: build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer testkey")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("TestGoldenPath: chat POST: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("TestGoldenPath: chat status %d: %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "choices") {
		t.Fatalf("TestGoldenPath: no 'choices' in response body: %s", b)
	}
	t.Logf("chat response: %s", b)
}
