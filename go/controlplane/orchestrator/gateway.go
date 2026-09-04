package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// InternalTokenHeader is the shared-secret header the Gateway expects on
// control-plane→gateway route mutations. This value is part of the inter-team
// contract defined by the PM and must not change without coordination.
const InternalTokenHeader = "X-Purser-Internal-Token"

// RouteUpdate is the JSON body sent to the Gateway when a deployment becomes
// active. Field names are fixed by the inter-team contract:
//
//	PUT {gateway}/api/v1/routes
//	{"model_id":"...","endpoint":"http://<host_ip>:<port>",
//	 "deployment_id":"...","quantization":"...","state":"active"}
type RouteUpdate struct {
	ModelID      string `json:"model_id"`
	Endpoint     string `json:"endpoint"`
	DeploymentID string `json:"deployment_id"`
	Quantization string `json:"quantization"`
	State        string `json:"state"`
}

// GatewaySync notifies the Gateway of route changes. It is injectable/mockable
// for tests.
type GatewaySync interface {
	// UpsertRoute publishes (or updates) a model→endpoint route on the Gateway.
	UpsertRoute(ctx context.Context, u RouteUpdate) error
	// DeleteRoute removes the route for modelID from the Gateway.
	DeleteRoute(ctx context.Context, modelID string) error
}

// HTTPGatewaySync is the production GatewaySync: a small HTTP client that speaks
// the contract above with best-effort retries.
type HTTPGatewaySync struct {
	baseURL string
	token   string
	hc      *http.Client
	retries int
	delay   time.Duration
	log     *slog.Logger
}

var _ GatewaySync = (*HTTPGatewaySync)(nil)

// GatewayOptions configures HTTPGatewaySync.
type GatewayOptions struct {
	// Addr is the Gateway base URL, e.g. "http://gateway:9000".
	Addr string
	// Token is the shared secret sent in X-Purser-Internal-Token.
	Token string
	// Client overrides the HTTP client (timeouts); a default is used if nil.
	Client *http.Client
	// Retries is the number of extra attempts on failure (best-effort).
	Retries int
	// RetryDelay is the delay between attempts.
	RetryDelay time.Duration
	Logger     *slog.Logger
}

// NewHTTPGatewaySync builds an HTTPGatewaySync. addr must be non-empty.
func NewHTTPGatewaySync(opts GatewayOptions) *HTTPGatewaySync {
	hc := opts.Client
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	if opts.Retries <= 0 {
		opts.Retries = 3
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = 500 * time.Millisecond
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &HTTPGatewaySync{
		baseURL: strings.TrimRight(opts.Addr, "/"),
		token:   opts.Token,
		hc:      hc,
		retries: opts.Retries,
		delay:   opts.RetryDelay,
		log:     log,
	}
}

func (g *HTTPGatewaySync) UpsertRoute(ctx context.Context, u RouteUpdate) error {
	body, err := json.Marshal(u)
	if err != nil {
		return fmt.Errorf("gatewaysync: marshal route: %w", err)
	}
	return g.doWithRetry(ctx, http.MethodPut, g.baseURL+"/api/v1/routes", body)
}

func (g *HTTPGatewaySync) DeleteRoute(ctx context.Context, modelID string) error {
	url := g.baseURL + "/api/v1/routes/" + modelID
	return g.doWithRetry(ctx, http.MethodDelete, url, nil)
}

// doWithRetry issues the request, retrying transient failures up to g.retries
// times. The final error is returned so the caller can log it, but callers
// treat gateway sync as best-effort and never fail a deployment on it.
func (g *HTTPGatewaySync) doWithRetry(ctx context.Context, method, url string, body []byte) error {
	var lastErr error
	for attempt := 0; attempt <= g.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(g.delay):
			}
		}
		err := g.do(ctx, method, url, body)
		if err == nil {
			return nil
		}
		lastErr = err
		g.log.Warn("gateway sync attempt failed",
			"method", method, "url", url, "attempt", attempt+1, "err", err)
	}
	return fmt.Errorf("gatewaysync: %s %s failed after %d attempts: %w", method, url, g.retries+1, lastErr)
}

func (g *HTTPGatewaySync) do(ctx context.Context, method, url string, body []byte) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set(InternalTokenHeader, g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("gateway returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// NopGatewaySync is a GatewaySync that does nothing, used when no Gateway is
// configured.
type NopGatewaySync struct{}

var _ GatewaySync = NopGatewaySync{}

func (NopGatewaySync) UpsertRoute(context.Context, RouteUpdate) error { return nil }
func (NopGatewaySync) DeleteRoute(context.Context, string) error      { return nil }
