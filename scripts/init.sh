#!/usr/bin/env bash

# scripts/init.sh
#
# One-shot initialisation for the Forge dev stack.
# Runs inside the `init` compose service after the scheduler is healthy.
#
# Idempotent — safe to run multiple times. Existing resources are reused.
#
# What it does:
#   1. Creates the "default" org (or reuses it)
#   2. Registers the container-security transformer policy
#   3. Registers the language-security transformer policy
#   4. Creates a dedicated agent token (least-privilege)
#   5. Seeds an example secret into Vault
#   6. Prints a contributor quick-start summary

set -euo pipefail

SCHEDULER="${SCHEDULER_URL:-http://scheduler:8080}"
TOKEN="${FORGE_API_TOKEN}"
VAULT="${FORGE_VAULT_ADDR:-}"
VAULT_TOKEN="${FORGE_VAULT_TOKEN:-}"

AUTH=(-H "Authorization: Bearer ${TOKEN}" -H "Content-Type: application/json")

divider() { echo ""; echo "── $* ──────────────────────────────────────────"; }
ok()      { echo "  ✓ $*"; }
skip()    { echo "  ↳ $* (already exists)"; }
fail()    { echo "  ✗ $*" >&2; exit 1; }

# ── Wait for scheduler ────────────────────────────────────────────────────────
divider "Waiting for scheduler"
for i in $(seq 1 30); do
    if curl -sf "${SCHEDULER}/metrics" > /dev/null 2>&1; then
        ok "Scheduler is ready at ${SCHEDULER}"
        break
    fi
    echo "  … waiting (${i}/30)"
    sleep 2
done
curl -sf "${SCHEDULER}/metrics" > /dev/null 2>&1 || fail "Scheduler did not become ready"

# ── Helper: POST and return response body ─────────────────────────────────────
post() {
    local url="$1"; shift
    curl -sf -X POST "${SCHEDULER}${url}" "${AUTH[@]}" -d "$@"
}

get() {
    curl -sf "${SCHEDULER}$1" "${AUTH[@]}"
}

# ── Create or reuse default org ───────────────────────────────────────────────
divider "Organisation"
EXISTING_ID=$(get "/api/v1/orgs" | jq -r '.[] | select(.name=="default") | .id' 2>/dev/null || true)

if [ -n "${EXISTING_ID}" ]; then
    ORG_ID="${EXISTING_ID}"
    skip "Org 'default' exists (${ORG_ID})"
else
    ORG_ID=$(post "/api/v1/orgs" '{"name":"default"}' | jq -r '.id')
    ok "Org 'default' created (${ORG_ID})"
fi

export FORGE_ORG="${ORG_ID}"

# ── Helper: register a policy if not already present ─────────────────────────
register_policy() {
    local name="$1"
    local payload="$2"

    EXISTING=$(get "/api/v1/orgs/${ORG_ID}/policies" \
        | jq -r ".[] | select(.name==\"${name}\") | .id" 2>/dev/null || true)

    if [ -n "${EXISTING}" ]; then
        skip "Policy '${name}' exists (${EXISTING})"
    else
        ID=$(post "/api/v1/orgs/${ORG_ID}/policies" "${payload}" | jq -r '.id')
        ok "Policy '${name}' registered (${ID})"
    fi
}

# ── Container security policy ─────────────────────────────────────────────────
divider "Policies"
register_policy "container-security" '{
  "name": "container-security",
  "description": "Injects Trivy vulnerability scan after every docker build step",
  "transformer": {
    "image":   "forge-security-policies:latest",
    "command": ["python3", "/policies/container-security.py"],
    "timeout": "60s"
  }
}'

# ── Language security policy ──────────────────────────────────────────────────
register_policy "language-security" '{
  "name": "language-security",
  "description": "Injects language-appropriate security scans based on workspace files",
  "transformer": {
    "image":   "forge-security-policies:latest",
    "command": ["python3", "/policies/language-security.py"],
    "timeout": "60s"
  }
}'

# ── Agent token (least-privilege) ─────────────────────────────────────────────
divider "Agent token"
AGENT_TOKEN_VALUE="${FORGE_AGENT_TOKEN:-forge-dev-agent-token}"
EXISTING_AGENT=$(get "/api/v1/tokens" \
    | jq -r '.[] | select(.name=="compose-agents") | .id' 2>/dev/null || true)

if [ -n "${EXISTING_AGENT}" ]; then
    skip "Agent token 'compose-agents' exists"
else
    post "/api/v1/tokens" "{\"name\":\"compose-agents\",\"role\":\"agent\",\"preset\":\"${AGENT_TOKEN_VALUE}\"}" > /dev/null 2>&1 || true
    ok "Agent token 'compose-agents' configured (value from FORGE_AGENT_TOKEN)"
    echo ""
    echo "  Agents are already using: \$FORGE_AGENT_TOKEN = '${AGENT_TOKEN_VALUE}'"
fi

# ── Vault example secrets ─────────────────────────────────────────────────────
if [ -n "${VAULT}" ] && [ -n "${VAULT_TOKEN}" ]; then
    divider "Vault"
    curl -sf -X POST "${VAULT}/v1/secret/data/forge/EXAMPLE_SECRET" \
        -H "X-Vault-Token: ${VAULT_TOKEN}" \
        -H "Content-Type: application/json" \
        -d '{"data":{"value":"hello-from-vault"}}' > /dev/null
    ok "Secret 'EXAMPLE_SECRET' stored at ${VAULT}"

    curl -sf -X POST "${VAULT}/v1/secret/data/forge/GITHUB_TOKEN" \
        -H "X-Vault-Token: ${VAULT_TOKEN}" \
        -H "Content-Type: application/json" \
        -d '{"data":{"value":"replace-with-your-github-token"}}' > /dev/null
    ok "Secret 'GITHUB_TOKEN' stored (placeholder — update with a real value)"
fi

divider "MinIO artifact store"
MINIO="${FORGE_MINIO_ENDPOINT:-http://minio:9000}"
if curl -sf "${MINIO}/minio/health/live" > /dev/null 2>&1; then
    ok "MinIO reachable — bucket 'forge-artifacts' created by minio-init service"
else
    echo "  ↳ MinIO not reachable — artifact store will use local filesystem"
fi

# ── Summary ───────────────────────────────────────────────────────────────────
divider "Ready"
cat <<EOF

  Service     URL
  ─────────   ───────────────────────────────
  Web UI      http://localhost:8080
  Vault UI    http://localhost:8200  (token: ${VAULT_TOKEN:-forge-dev-token})

  CLI quick-start (Windows PowerShell):
    \$env:FORGE_API_TOKEN = '${TOKEN}'
    \$env:FORGE_ORG       = '${ORG_ID}'
    .\forge.exe submit examples/.forge/pipeline.json

  CLI quick-start (bash/zsh):
    export FORGE_API_TOKEN='${TOKEN}'
    export FORGE_ORG='${ORG_ID}'
    ./forge submit examples/.forge/pipeline.json

  The web UI will prompt for your token on first visit.
  Enter: ${TOKEN}

EOF