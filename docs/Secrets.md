# Secrets Management

Forge integrates with HashiCorp Vault for secret storage. Secrets are **never stored in PostgreSQL** — only secret names travel through the scheduler and database. Agents fetch secret values from Vault at job execution time, immediately before injecting them as environment variables into the container.

---

## How Secrets Work

```
Pipeline YAML:                  What actually happens at runtime:

steps:                          1. Scheduler stores step with secret_names: ["API_KEY"]
  - id: deploy                     (only the name, never the value)
    secrets: [API_KEY]
    run: curl -H "Authorization: $API_KEY" ...
                                2. Agent leases job, sees secret_names: ["API_KEY"]

                                3. Agent calls Vault:
                                   GET /v1/secret/data/forge/global/API_KEY
                                   → "my-secret-value"

                                4. Agent injects into container env:
                                   API_KEY=my-secret-value

                                5. Container runs with the secret as an env var
                                   (value is also redacted from log output)
```

---

## Vault Setup

### Development (Docker)

```bash
docker run -d --name vault \
  -p 8200:8200 \
  -e VAULT_DEV_ROOT_TOKEN_ID=forge-dev-token \
  -e VAULT_DEV_LISTEN_ADDRESS=0.0.0.0:8200 \
  hashicorp/vault
```

Set environment variables for the CLI and agent:
```powershell
$env:FORGE_VAULT_ADDR  = "http://localhost:8200"
$env:FORGE_VAULT_TOKEN = "forge-dev-token"
```

### Production

Use a properly configured Vault cluster with appropriate auth backends. The agent needs a Vault token with read access to the paths it uses. For the compose stack, Vault configuration is in `compose.yml`.

---

## Secret Scoping

Secrets can be scoped to three levels. The agent walks the chain from most specific to least specific and returns the first match:

```
Project-scope  secret/data/forge/projects/{project_id}/{NAME}   highest priority
Org-scope      secret/data/forge/orgs/{org_id}/{NAME}
Global         secret/data/forge/global/{NAME}
Legacy         secret/data/forge/{NAME}                          backward compat
```

This lets you:
- Store most secrets at the global scope (shared across all runs)
- Override specific secrets for a whole team's projects at the org level
- Override for one specific project without affecting others

### Practical example

```bash
# Global: used by all runs unless overridden
forge secret set DATABASE_URL "postgres://global-db:5432/app"

# Org-level: staging org uses a different database
forge secret set DATABASE_URL "postgres://staging-db:5432/app" --org staging-org-id

# Project-level: this specific service uses yet another database
forge secret set DATABASE_URL "postgres://service-db:5432/myservice" --project proj-id
```

A run submitted under `proj-id` uses the project-level `DATABASE_URL`. A run under the staging org (but not `proj-id`) uses the org-level value. Everything else uses the global value.

---

## CLI Usage

### Setting Secrets

```bash
# Global scope (no flags)
forge secret set MY_SECRET "the-value"

# Org-scoped
forge secret set MY_SECRET "the-value" --org <org-id>

# Project-scoped
forge secret set MY_SECRET "the-value" --project <project-id>

# FORGE_ORG is used as default --org if set
$env:FORGE_ORG = "abc123"
forge secret set MY_SECRET "the-value"    # scoped to org abc123
```

### Getting Secrets

```bash
forge secret get MY_SECRET                # from global scope
forge secret get MY_SECRET --org abc123   # from org scope
```

### Listing Secrets

```bash
forge secret list                         # global scope
forge secret list --org abc123            # all secrets in this org
forge secret list --project xyz789        # all secrets for this project
```

---

## Using Secrets in Pipelines

Declare the secret names in the step definition. The agent fetches their values from Vault at runtime:

```yaml
steps:
  - id: deploy
    image: alpine:latest
    secrets: [GITHUB_TOKEN, DEPLOY_KEY, DATABASE_URL]
    run: |
      echo "Deploying..."
      curl -H "Authorization: token $GITHUB_TOKEN" https://api.github.com/repos/...
```

Secret values are:
- Injected as environment variables with the same name as the secret
- Automatically redacted from all log output (replaced with `***`)
- Never stored in the scheduler database
- Never visible in the Web UI

---

## Secret Rotation

To rotate a secret, overwrite it at the same scope:

```bash
forge secret set GITHUB_TOKEN "new-token-value"
```

Running jobs that have already fetched the secret will use the old value until they complete. New jobs will fetch the new value.

---

## Vault Authentication in Production

For production, instead of a long-lived root token, configure Vault with a more secure auth method:

**AppRole (recommended for agents):**
```bash
# Create a policy
vault policy write forge-agent - <<EOF
path "secret/data/forge/*" {
  capabilities = ["read", "list"]
}
EOF

# Create an AppRole
vault auth enable approle
vault write auth/approle/role/forge-agent \
  token_policies="forge-agent" \
  token_ttl=1h \
  token_max_ttl=4h
```

Set `FORGE_VAULT_TOKEN` on agents to a token obtained via AppRole login.

**Kubernetes (for agents running in Kubernetes):**
Use the Vault Kubernetes auth backend to grant agents service-account-based access without a static token.