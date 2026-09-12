#!/usr/bin/env bash
# tools/purser-status.sh — one-command health overview for the Purser stack.
#
# Usage:
#   ./tools/purser-status.sh
#   PURSER_CP_URL=http://myserver:8080 PURSER_API_KEY=sk-abc ./tools/purser-status.sh
#   ./tools/purser-status.sh --json

set -uo pipefail

CP_URL="${PURSER_CP_URL:-http://localhost:8080}"
API_KEY="${PURSER_API_KEY:-demo-key-12345}"
GATEWAY_URL="${PURSER_GATEWAY_URL:-http://localhost:3000}"
DASHBOARD_URL="${PURSER_DASHBOARD_URL:-http://localhost:3000}"
JSON_OUTPUT=false

for arg in "$@"; do
  [ "$arg" = "--json" ] && JSON_OUTPUT=true
done

# ── colour helpers ────────────────────────────────────────────────────────────
if [ -t 1 ] && [ "${NO_COLOR:-}" = "" ]; then
  RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
  BOLD='\033[1m'; NC='\033[0m'
else
  RED=''; GREEN=''; YELLOW=''; BOLD=''; NC=''
fi

ok()   { printf "${GREEN}✓${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}⚠${NC} %s\n" "$*"; }
err()  { printf "${RED}✗${NC} %s\n" "$*"; }
hdr()  {
  local title="$1"
  local pad
  pad=$(python3 -c "n=max(1,55-${#title});print('-'*n)" 2>/dev/null || printf '%s' '─────────────────────────────────────────────')
  printf "\n${BOLD}%s${NC} %s\n" "$title" "$pad"
}

# ── curl helpers ──────────────────────────────────────────────────────────────
# Returns body on stdout followed by a bare HTTP status line
api_get() {
  curl -s -w "\n%{http_code}" \
    "${CP_URL}/api/v1${1}" \
    -H "Authorization: Bearer ${API_KEY}" \
    --max-time 5 2>/dev/null
}

http_check() {
  curl -s -o /dev/null -w "%{http_code}" --max-time 5 "$1" 2>/dev/null || echo "000"
}

# Extract last line (status code) from api_get output
last_line()  { printf '%s' "$1" | tail -1; }
body_lines() { printf '%s' "$1" | head -n -1; }

# ── section: Services ─────────────────────────────────────────────────────────
CP_UP=false

check_services() {
  hdr "Purser Status"

  local raw status_code body
  raw=$(api_get "/cluster/health")
  status_code=$(last_line "$raw")
  body=$(body_lines "$raw")

  if [ "$status_code" = "200" ]; then
    local summary
    summary=$(python3 -c "
import json, sys
d=json.loads('$body')
print('{} nodes, {} ready'.format(d.get('total_nodes',0), d.get('ready_nodes',0)))
" 2>/dev/null || echo "")
    ok "Control Plane   ${CP_URL}   ${summary:+(${summary})}"
    CP_UP=true
  else
    err "Control Plane   ${CP_URL}   unreachable (HTTP ${status_code})"
  fi

  local gw_code
  gw_code=$(http_check "${GATEWAY_URL}/v1/models")
  if [ "$gw_code" = "200" ] || [ "$gw_code" = "401" ]; then
    ok "Gateway         ${GATEWAY_URL}/v1"
  else
    warn "Gateway         ${GATEWAY_URL}/v1   HTTP ${gw_code}"
  fi

  local dash_code
  dash_code=$(http_check "${DASHBOARD_URL}/")
  if [ "$dash_code" = "200" ]; then
    ok "Dashboard       ${DASHBOARD_URL}"
  else
    warn "Dashboard       ${DASHBOARD_URL}   HTTP ${dash_code}"
  fi
}

# ── section: Fleet ────────────────────────────────────────────────────────────
check_fleet() {
  hdr "Fleet"

  local raw status_code body
  raw=$(api_get "/nodes")
  status_code=$(last_line "$raw")
  body=$(body_lines "$raw")

  if [ "$status_code" != "200" ]; then
    err "Could not fetch nodes (HTTP ${status_code})"; return
  fi

  python3 - "$body" <<'PYEOF'
import sys, json, os

body = sys.argv[1]
data = json.loads(body)
nodes = data.get('nodes', [])
total = len(nodes)
ready = 0

use_color = os.isatty(1) and not os.environ.get('NO_COLOR')
RED    = '\033[0;31m'  if use_color else ''
GREEN  = '\033[0;32m'  if use_color else ''
YELLOW = '\033[1;33m'  if use_color else ''
NC     = '\033[0m'     if use_color else ''

for n in nodes:
    node_id  = n.get('id', '?')
    hostname = n.get('hostname', '?')
    state    = n.get('state', '?').replace('NODE_STATE_', '')
    ram_gb   = n.get('ram_gb', 0)
    vram_gb  = n.get('vram_gb', 0) or 0
    hp       = n.get('hardware_profile', {})
    backends = hp.get('backends', [])
    accel    = 'GPU' if any('GPU' in b or 'CUDA' in b for b in backends) else 'CPU only'
    if state == 'READY':
        ready += 1
        sym = GREEN + '✓' + NC
    elif state == 'RUNNING':
        ready += 1
        sym = YELLOW + '~' + NC
    else:
        sym = RED + '✗' + NC
    hw = accel
    if vram_gb > 0:
        hw += '  {:.1f}GB VRAM'.format(vram_gb)
    hw += '  · {:.1f}GB RAM'.format(ram_gb)
    print('  {}  {:<26}  {:<12}  {:<10}  {}'.format(sym, node_id, hostname, state, hw))

print('  Total: {} node{}, {} ready'.format(total, 's' if total != 1 else '', ready))
PYEOF
}

# ── section: Catalog ──────────────────────────────────────────────────────────
check_catalog() {
  hdr "Catalog"

  local raw status_code body
  raw=$(api_get "/models")
  status_code=$(last_line "$raw")
  body=$(body_lines "$raw")

  if [ "$status_code" != "200" ]; then
    err "Could not fetch models (HTTP ${status_code})"; return
  fi

  python3 - "$body" <<'PYEOF'
import sys, json, os

body = sys.argv[1]
data = json.loads(body)
models = data.get('models', [])
total = len(models)

use_color = os.isatty(1) and not os.environ.get('NO_COLOR')
GREEN = '\033[0;32m' if use_color else ''
RED   = '\033[0;31m' if use_color else ''
NC    = '\033[0m'    if use_color else ''

for m in models:
    mid    = m.get('id', '?')
    params = m.get('params_total_b', 0) or 0
    spec   = m.get('spec', {})
    quants = spec.get('quantizations', [])
    qname  = quants[0].get('name', '?').upper() if quants else '?'
    qsize  = quants[0].get('sizeGb', 0) if quants else 0
    fit    = m.get('fit', {})
    deployable = fit.get('deployable', False)
    sym = GREEN + '✓ fits' + NC if deployable else RED + '✗ no fit' + NC
    print('  {:<22}  {:>5.1f}B  {:<8}  {:>5.2f}GB  {}'.format(
        mid, params, qname, qsize, sym))

print('  Total: {} model{}'.format(total, 's' if total != 1 else ''))
PYEOF
}

# ── section: Deployments ──────────────────────────────────────────────────────
check_deployments() {
  hdr "Deployments"

  local raw status_code body
  raw=$(api_get "/deployments")
  status_code=$(last_line "$raw")
  body=$(body_lines "$raw")

  if [ "$status_code" != "200" ]; then
    err "Could not fetch deployments (HTTP ${status_code})"; return
  fi

  python3 - "$body" <<'PYEOF'
import sys, json, os

body = sys.argv[1]
data = json.loads(body)
deps = data.get('deployments', [])
total = len(deps)
active = 0

use_color = os.isatty(1) and not os.environ.get('NO_COLOR')
GREEN  = '\033[0;32m'  if use_color else ''
YELLOW = '\033[1;33m'  if use_color else ''
RED    = '\033[0;31m'  if use_color else ''
NC     = '\033[0m'     if use_color else ''

for d in deps:
    dep_id   = d.get('id', '?')
    model_id = d.get('model_id', '?')
    state    = d.get('state', '?').replace('DEPLOYMENT_STATE_', '')
    detail   = d.get('detail', {})
    engines  = detail.get('engines', [])
    roles    = ' '.join(
        '{node}({role})'.format(node=e.get('node_id','?'), role=e.get('role','?').upper())
        for e in engines)
    if state == 'ACTIVE':
        sym = GREEN + '✓' + NC
        active += 1
    elif state in ('PENDING', 'SCHEDULING'):
        sym = YELLOW + '~' + NC
    else:
        sym = RED + '✗' + NC
    print('  {}  {:<30}  {:<18}  {:<12}  {}'.format(
        sym, dep_id, model_id, state, roles))

print('  Total: {} active'.format(active))
PYEOF
}

# ── section: API Keys ─────────────────────────────────────────────────────────
check_apikeys() {
  hdr "API Keys"

  local raw status_code body
  raw=$(api_get "/apikeys")
  status_code=$(last_line "$raw")
  body=$(body_lines "$raw")

  if [ "$status_code" != "200" ]; then
    err "Could not fetch API keys (HTTP ${status_code})"; return
  fi

  python3 - "$body" <<'PYEOF'
import sys, json

body = sys.argv[1]
data = json.loads(body)
keys = data.get('apikeys', [])
total = len(keys)

for k in keys:
    kid    = k.get('id', '?')
    name   = k.get('name', '?')
    tenant = k.get('tenant', '?')
    print('  {:<22}  {:<22}  tenant={}'.format(kid, name, tenant))

if total == 0:
    print('  (no CP-registered keys — gateway built-in keys are not listed here)')
print('  Total: {} CP key{}'.format(total, 's' if total != 1 else ''))
PYEOF
}

# ── section: Quick test ───────────────────────────────────────────────────────
print_quick_test() {
  hdr "Quick test"
  printf "  curl %s/api/v1/cluster/health  →  " "$CP_URL"
  local raw sc
  raw=$(api_get "/cluster/health")
  sc=$(last_line "$raw")
  if [ "$sc" = "200" ]; then
    ok "ok"
  else
    err "HTTP $sc"
  fi
  printf '%s\n' "─────────────────────────────────────────────────────────"
}

# ── JSON output mode ──────────────────────────────────────────────────────────
json_output() {
  local h_raw h_code h_body
  local n_raw n_code n_body
  local m_raw m_code m_body
  local d_raw d_code d_body
  local k_raw k_code k_body
  local gw_code dash_code

  h_raw=$(api_get "/cluster/health"); h_code=$(last_line "$h_raw"); h_body=$(body_lines "$h_raw")
  n_raw=$(api_get "/nodes");          n_code=$(last_line "$n_raw"); n_body=$(body_lines "$n_raw")
  m_raw=$(api_get "/models");         m_code=$(last_line "$m_raw"); m_body=$(body_lines "$m_raw")
  d_raw=$(api_get "/deployments");    d_code=$(last_line "$d_raw"); d_body=$(body_lines "$d_raw")
  k_raw=$(api_get "/apikeys");        k_code=$(last_line "$k_raw"); k_body=$(body_lines "$k_raw")
  gw_code=$(http_check "${GATEWAY_URL}/v1/models")
  dash_code=$(http_check "${DASHBOARD_URL}/")

  python3 - \
    "$h_code" "$h_body" \
    "$n_code" "$n_body" \
    "$m_code" "$m_body" \
    "$d_code" "$d_body" \
    "$k_code" "$k_body" \
    "$gw_code" "$dash_code" \
    "$CP_URL" "$GATEWAY_URL" "$DASHBOARD_URL" \
    <<'PYEOF'
import sys, json

def safe(s):
    try: return json.loads(s)
    except: return None

h_code,h_body,n_code,n_body,m_code,m_body,d_code,d_body,k_code,k_body,gw_code,dash_code,cp_url,gw_url,dash_url = sys.argv[1:]

h    = safe(h_body)
nd   = safe(n_body)
md   = safe(m_body)
dd   = safe(d_body)
kd   = safe(k_body)

out = {
    "cp": {
        "status": "ok" if h_code == "200" else "unreachable",
        "url": cp_url,
        "http_code": int(h_code) if h_code.isdigit() else 0,
        "health": h,
    },
    "gateway": {
        "status": "ok" if gw_code in ("200","401") else "unreachable",
        "url": gw_url + "/v1",
        "http_code": int(gw_code) if gw_code.isdigit() else 0,
    },
    "dashboard": {
        "status": "ok" if dash_code == "200" else "unreachable",
        "url": dash_url,
        "http_code": int(dash_code) if dash_code.isdigit() else 0,
    },
    "nodes":       nd.get("nodes", [])        if nd else [],
    "models":      md.get("models", [])       if md else [],
    "deployments": dd.get("deployments", [])  if dd else [],
    "apikeys":     kd.get("apikeys", [])      if kd else [],
}
print(json.dumps(out, indent=2))
PYEOF
}

# ── main ──────────────────────────────────────────────────────────────────────
main() {
  if $JSON_OUTPUT; then
    json_output
    exit 0
  fi

  check_services
  if $CP_UP; then
    check_fleet
    check_catalog
    check_deployments
    check_apikeys
  else
    printf "\n${YELLOW}Skipping fleet/catalog/deployment checks — control plane unreachable.${NC}\n"
    printf "Is the stack running?  try: make demo   or: make dev\n\n"
  fi
  print_quick_test
}

main
