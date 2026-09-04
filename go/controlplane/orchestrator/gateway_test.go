package orchestrator_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/purser/purser/go/controlplane/orchestrator"
)

func TestHTTPGatewaySync_UpsertRoute(t *testing.T) {
	var gotMethod, gotPath, gotToken, gotCT string
	var body orchestrator.RouteUpdate
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get(orchestrator.InternalTokenHeader)
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	gw := orchestrator.NewHTTPGatewaySync(orchestrator.GatewayOptions{Addr: srv.URL, Token: "s3cret"})
	err := gw.UpsertRoute(context.Background(), orchestrator.RouteUpdate{
		ModelID:      "m1",
		Endpoint:     "http://10.0.0.3:8000",
		DeploymentID: "dep-1",
		Quantization: "q4",
		State:        "active",
	})
	if err != nil {
		t.Fatalf("UpsertRoute: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/routes" {
		t.Errorf("path = %q, want /api/v1/routes", gotPath)
	}
	if gotToken != "s3cret" {
		t.Errorf("token header = %q, want s3cret", gotToken)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotCT)
	}
	if body.ModelID != "m1" || body.Endpoint != "http://10.0.0.3:8000" || body.DeploymentID != "dep-1" ||
		body.Quantization != "q4" || body.State != "active" {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestHTTPGatewaySync_DeleteRoute(t *testing.T) {
	var gotMethod, gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get(orchestrator.InternalTokenHeader)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	gw := orchestrator.NewHTTPGatewaySync(orchestrator.GatewayOptions{Addr: srv.URL, Token: "tok"})
	if err := gw.DeleteRoute(context.Background(), "m1"); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/api/v1/routes/m1" {
		t.Errorf("path = %q, want /api/v1/routes/m1", gotPath)
	}
	if gotToken != "tok" {
		t.Errorf("token header = %q, want tok", gotToken)
	}
}

func TestHTTPGatewaySync_RetryThenError(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	gw := orchestrator.NewHTTPGatewaySync(orchestrator.GatewayOptions{
		Addr: srv.URL, Token: "t", Retries: 2, RetryDelay: time.Millisecond,
	})
	err := gw.UpsertRoute(context.Background(), orchestrator.RouteUpdate{ModelID: "m1"})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	// initial attempt + 2 retries = 3.
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestHTTPGatewaySync_RecoversOnRetry(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	gw := orchestrator.NewHTTPGatewaySync(orchestrator.GatewayOptions{
		Addr: srv.URL, Token: "t", Retries: 3, RetryDelay: time.Millisecond,
	})
	if err := gw.UpsertRoute(context.Background(), orchestrator.RouteUpdate{ModelID: "m1"}); err != nil {
		t.Fatalf("expected success on retry, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}
