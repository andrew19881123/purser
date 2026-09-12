package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/purser/purser/go/controlplane/registry"
)

// DefaultRouteReconcileInterval is how often the desired route set is re-pushed
// to the Gateway. Route sync is a cheap idempotent PUT per active model.
const DefaultRouteReconcileInterval = 30 * time.Second

// defaultRouteRetryDelay bounds how long the loop waits after a failed pass
// before trying again, so a Gateway that is briefly down converges in seconds
// rather than a whole interval.
const defaultRouteRetryDelay = 5 * time.Second

// RouteReconciler keeps the Gateway's routing table converged on the Control
// Plane's desired state: one route per ACTIVE deployment, and nothing else.
//
// Why it exists: the Gateway holds `model_id -> endpoint` in memory only and is
// populated exclusively by Control-Plane pushes. Before this loop, a Gateway
// restart (or a push lost while the Gateway was down) silently emptied the
// table — every inference request then failed with `503 model not available`
// until an operator re-deployed a model. Reconciling makes that self-healing:
// the desired set is re-pushed periodically and stale routes are removed, so a
// restarted Gateway recovers on the next tick without any operator action.
//
// Reconcile is ordered (push first, then delete) and idempotent, so it is safe
// to run concurrently with deployment-time pushes.
type RouteReconciler struct {
	reg      registry.Registry
	gw       GatewaySync
	interval time.Duration
	retry    time.Duration
	log      *slog.Logger

	mu sync.Mutex
	// pushed tracks the models a successful upsert was last sent for. It is
	// only used as a fallback for stale detection when the Gateway cannot be
	// listed (no [RouteLister]); observation is always preferred.
	pushed map[string]struct{}
}

// NewRouteReconciler builds a RouteReconciler. A nil logger falls back to the
// default logger; a non-positive interval falls back to
// [DefaultRouteReconcileInterval].
func NewRouteReconciler(reg registry.Registry, gw GatewaySync, interval time.Duration, log *slog.Logger) *RouteReconciler {
	if log == nil {
		log = slog.Default()
	}
	if gw == nil {
		gw = NopGatewaySync{}
	}
	if interval <= 0 {
		interval = DefaultRouteReconcileInterval
	}
	// Retry faster than the steady-state interval, but never slower than it
	// (a short interval in tests must not be stretched by the retry delay).
	retry := defaultRouteRetryDelay
	if retry > interval {
		retry = interval
	}
	return &RouteReconciler{
		reg:      reg,
		gw:       gw,
		interval: interval,
		retry:    retry,
		log:      log,
		pushed:   make(map[string]struct{}),
	}
}

// Run reconciles immediately (so a Gateway started alongside the control plane
// converges at boot) and then on every tick until ctx is cancelled.
//
// A Gateway that is unreachable never terminates the loop: the pass logs a
// warning and the next attempt is scheduled sooner ([RouteReconciler.retry]).
// The only error returned is ctx's, so the caller can ignore cancellation.
func (r *RouteReconciler) Run(ctx context.Context) error {
	// delay == 0 runs the first pass straight away.
	delay := time.Duration(0)
	for {
		if delay > 0 {
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		err := r.Reconcile(ctx)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			r.log.Warn("route reconcile failed; gateway routes may be stale, will retry",
				"retry_in", r.retry, "err", err)
			delay = r.retry
		default:
			delay = r.interval
		}
	}
}

// Reconcile performs a single convergence pass:
//
//  1. desired state = every ACTIVE deployment in the Registry that has an
//     endpoint (the deployment host the Gateway must forward to);
//  2. observed state = the Gateway's own table via `GET /api/v1/routes` when it
//     supports listing, otherwise the routes this reconciler last pushed;
//  3. PUT every desired route (idempotent upsert — this is what restores a
//     restarted Gateway);
//  4. DELETE every observed route that is not desired (stale cleanup).
//
// Failures on individual routes are collected so one unreachable route does not
// abort the pass; the joined error is returned so Run can retry sooner.
func (r *RouteReconciler) Reconcile(ctx context.Context) error {
	desired, err := r.desiredRoutes(ctx)
	if err != nil {
		return err
	}

	observed := r.observe(ctx)

	var errs []error

	for modelID, u := range desired {
		if err := r.gw.UpsertRoute(ctx, u); err != nil {
			errs = append(errs, fmt.Errorf("upsert %s: %w", modelID, err))
			continue
		}
		r.markPushed(modelID)
	}

	for _, modelID := range observed {
		if _, ok := desired[modelID]; ok {
			continue
		}
		if err := r.gw.DeleteRoute(ctx, modelID); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", modelID, err))
			continue
		}
		r.unmarkPushed(modelID)
		r.log.Info("stale gateway route removed", "model", modelID)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// desiredRoutes builds the route set the Gateway should hold: one entry per
// ACTIVE deployment, excluding anything without a usable endpoint (which the
// Gateway would reject anyway).
func (r *RouteReconciler) desiredRoutes(ctx context.Context) (map[string]RouteUpdate, error) {
	deps, err := r.reg.ListDeployments(ctx)
	if err != nil {
		return nil, fmt.Errorf("route reconcile: list deployments: %w", err)
	}
	out := make(map[string]RouteUpdate, len(deps))
	for _, dep := range deps {
		if dep.State != StateActive {
			continue
		}
		detail := &DeploymentDetail{}
		if len(dep.Detail) > 0 {
			_ = json.Unmarshal(dep.Detail, detail)
		}
		modelID := dep.ModelID
		if modelID == "" {
			modelID = detail.ModelID
		}
		if modelID == "" || detail.Endpoint == "" {
			r.log.Warn("active deployment has no routable endpoint; skipping",
				"deployment", dep.ID, "model", modelID)
			continue
		}
		out[modelID] = RouteUpdate{
			ModelID:      modelID,
			Endpoint:     detail.Endpoint,
			DeploymentID: dep.ID,
			Quantization: detail.Quantization,
			State:        "active",
		}
	}
	return out, nil
}

// observe returns the model ids the Gateway currently holds. It prefers the
// Gateway's own listing; if the Gateway cannot be listed (or is unreachable),
// it falls back to the routes this reconciler last pushed, which is the best
// available approximation and keeps stale cleanup working.
func (r *RouteReconciler) observe(ctx context.Context) []string {
	if lister, ok := r.gw.(RouteLister); ok {
		routes, err := lister.ListRoutes(ctx)
		if err == nil {
			ids := make([]string, 0, len(routes))
			for _, rt := range routes {
				ids = append(ids, rt.ModelID)
			}
			return ids
		}
		r.log.Warn("cannot list gateway routes; falling back to last-pushed set", "err", err)
	}
	return r.pushedIDs()
}

func (r *RouteReconciler) markPushed(modelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushed[modelID] = struct{}{}
}

func (r *RouteReconciler) unmarkPushed(modelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pushed, modelID)
}

func (r *RouteReconciler) pushedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.pushed))
	for id := range r.pushed {
		out = append(out, id)
	}
	return out
}
