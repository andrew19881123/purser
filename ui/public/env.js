// Purser UI — runtime configuration.
//
// This stub ships in the build and is served as-is during local dev, where an
// EMPTY override object means "fall back to Vite build-time env / same-origin
// defaults" (i.e. the REAL backend). In the container image it is REGENERATED
// at start-up from environment variables by
// deploy/docker/docker-entrypoint.d/40-purser-runtime-config.sh — that is how
// the API base URL is set per-deployment without rebuilding the bundle.
//
// Loaded synchronously BEFORE the app bundle (see index.html), and never fetches
// anything external — air-gap safe.
window.__PURSER_CONFIG__ = {};
