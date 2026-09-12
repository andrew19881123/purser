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

func TestHTTPGatewaySync_ListRoutes(t *testing.T) {
	var gotMethod, gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get(orchestrator.InternalTokenHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[
			{"model_id":"m1","endpoint":"http://10.0.0.3:8000","deployment_id":"dep-1","quantization":"q4","state":"active"},
			{"model_id":"m2","endpoint":"http://10.0.0.4:8000","deployment_id":"dep-2","quantization":"q8","state":"draining"}
		]}`)
	}))
	defer srv.Close()

	gw := orchestrator.NewHTTPGatewaySync(orchestrator.GatewayOptions{Addr: srv.URL, Token: "s3cret"})
	var _ orchestrator.RouteLister = gw // the reconciler's observation path
	routes, err := gw.ListRoutes(context.Background())
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/api/v1/routes" {
		t.Errorf("path = %q, want /api/v1/routes", gotPath)
	}
	if gotToken != "s3cret" {
		t.Errorf("token header = %q, want s3cret", gotToken)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %+v, want 2 entries", routes)
	}
	if routes[0].ModelID != "m1" || routes[0].Endpoint != "http://10.0.0.3:8000" ||
		routes[0].DeploymentID != "dep-1" || routes[0].Quantization != "q4" || routes[0].State != "active" {
		t.Errorf("first route = %+v", routes[0])
	}
	if routes[1].State != "draining" {
		t.Errorf("second route state = %q, want draining", routes[1].State)
	}
}

func TestHTTPGatewaySync_ListRoutesEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer srv.Close()

	gw := orchestrator.NewHTTPGatewaySync(orchestrator.GatewayOptions{Addr: srv.URL, Token: "t"})
	routes, err := gw.ListRoutes(context.Background())
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if len(routes) != 0 {
		t.Errorf("routes = %+v, want none (a restarted gateway)", routes)
	}
}

func TestHTTPGatewaySync_ListRoutesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	gw := orchestrator.NewHTTPGatewaySync(orchestrator.GatewayOptions{
		Addr: srv.URL, Token: "t", Retries: 1, RetryDelay: time.Millisecond,
	})
	if _, err := gw.ListRoutes(context.Background()); err == nil {
		t.Fatal("expected an error when the gateway cannot be listed")
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
