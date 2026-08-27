#!/usr/bin/env bash

# Forge Self-Hosted Deployment Script
# Usage: curl -sSL https://raw.githubusercontent.com/JBraunsmaJr/Forge/main/scripts/deploy-self-hosted.sh | bash

set -euo pipefail

REPO="JBraunsmaJr/Forge"
BRANCH="main"
RAW_URL="https://raw.githubusercontent.com/${REPO}/${BRANCH}"

echo "--- Forge Self-Hosted Deployment ---"

# Check dependencies
for cmd in docker curl openssl jq; do
    if ! command -v $cmd &> /dev/null; then
        echo "Error: $cmd is not installed."
        exit 1
    fi
done

# Create deployment directory
mkdir -p forge-server
cd forge-server

# Download core files
echo "Downloading deployment files..."
curl -sSL -O "${RAW_URL}/deployments/scheduler/compose.yml"
mkdir -p scripts
curl -sSL -o scripts/init.sh "${RAW_URL}/scripts/init.sh"
chmod +x scripts/init.sh

# Generate Secrets if .env doesn't exist
if [ ! -f .env ]; then
    echo "Generating .env file..."
    ROOT_TOKEN=$(openssl rand -hex 24)
    AGENT_TOKEN=$(openssl rand -hex 24)
    DB_PASSWORD=$(openssl rand -hex 12)
    S3_SECRET=$(openssl rand -hex 16)
    
    cat <<EOF > .env
FORGE_ROOT_TOKEN=${ROOT_TOKEN}
FORGE_AGENT_TOKEN=${AGENT_TOKEN}
FORGE_DB_PASSWORD=${DB_PASSWORD}
FORGE_S3_SECRET_KEY=${S3_SECRET}
FORGE_VAULT_ADDR=http://localhost:8200
FORGE_VAULT_TOKEN=
FORGE_BASE_URL=http://localhost:8080
EOF
    echo "Secrets generated and saved to .env"
fi

# Launch core infrastructure
echo "Starting Forge infrastructure (Postgres, MinIO, Vault)..."
docker compose up -d postgres minio vault

# Wait for Vault to be ready
echo "Waiting for Vault..."
until docker compose  exec vault vault status -format=json > /dev/null 2>&1 || [ $? -eq 2 ]; do
    sleep 1
done

# Initialize Vault if needed
VAULT_STATUS=$(docker compose exec vault vault status -format=json)
INITIALIZED=$(echo "$VAULT_STATUS" | jq -r '.initialized')
JUST_INIT=false

if [ "$INITIALIZED" != "true" ]; then
    echo "Initializing Vault..."
    INIT_OUT=$(docker compose exec vault vault operator init -format=json -key-shares=1 -key-threshold=1)
    
    VAULT_TOKEN=$(echo "$INIT_OUT" | jq -r '.root_token')
    VAULT_KEY=$(echo "$INIT_OUT" | jq -r '.unseal_keys_b64[0]')
    
    # Save to .env
    sed -i "s/FORGE_VAULT_TOKEN=.*/FORGE_VAULT_TOKEN=${VAULT_TOKEN}/" .env
    echo "FORGE_VAULT_UNSEAL_KEY=${VAULT_KEY}" >> .env
    echo "Vault initialized and token saved to .env"
    JUST_INIT=true
fi

# Unseal Vault
VAULT_STATUS=$(docker compose  exec vault vault status -format=json)
SEALED=$(echo "$VAULT_STATUS" | jq -r '.sealed')

if [ "$SEALED" == "true" ]; then
    echo "Unsealing Vault..."
    VAULT_KEY=$(grep FORGE_VAULT_UNSEAL_KEY .env | cut -d= -f2)
    docker compose  exec vault vault operator unseal "$VAULT_KEY" > /dev/null
    echo "Vault unsealed."
fi

# Enable KV engine if just initialized
if [ "$JUST_INIT" == "true" ]; then
    echo "Configuring Vault KV engine..."
    VAULT_TOKEN=$(grep FORGE_VAULT_TOKEN .env | cut -d= -f2)
    docker compose  exec -e VAULT_TOKEN="${VAULT_TOKEN}" vault vault secrets enable -path=secret kv-v2
fi

# Start remaining services
echo "Starting Forge scheduler..."
docker compose  up -d

echo ""
echo "--- Deployment Complete! ---"
echo "Scheduler: http://localhost:8080"
echo "Vault UI:  http://localhost:8200"
echo ""
echo "Root Token:  $(grep FORGE_ROOT_TOKEN .env | cut -d= -f2)"
echo "Agent Token: $(grep FORGE_AGENT_TOKEN .env | cut -d= -f2)"
echo "Vault Token: $(grep FORGE_VAULT_TOKEN .env | cut -d= -f2)"
echo ""
echo "To connect an agent, use the Agent Token above."
