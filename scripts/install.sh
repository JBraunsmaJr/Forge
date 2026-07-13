#!/usr/bin/env bash

# Forge Binary Installer
# Usage: curl -sSL https://forge.dev/install.sh | bash

set -euo pipefail

# Configuration
INSTALL_DIR="/usr/local/bin"
BINARY_NAME="forge"
REPO="JBraunsmaJr/Forge"

# Detect OS and Arch
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "${ARCH}" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: ${ARCH}"; exit 1 ;;
esac

case "${OS}" in
    linux) OS="linux" ;;
    darwin) OS="darwin" ;;
    *) echo "Unsupported OS: ${OS}"; exit 1 ;;
esac

echo "--- Installing Forge for ${OS}/${ARCH} ---"

# Get latest release tag
LATEST_TAG=$(curl -s "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "${LATEST_TAG}" ]; then
    echo "Failed to fetch latest release tag. Falling back to 'main' artifact name template."
    LATEST_TAG="latest"
fi

DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_TAG}/forge-${OS}-${ARCH}"

echo "Downloading from: ${DOWNLOAD_URL}"

# Download to a temporary file
TMP_BIN=$(mktemp)
if ! curl -sSL -o "${TMP_BIN}" "${DOWNLOAD_URL}"; then
    echo "Error: Failed to download binary. It might not be released yet."
    echo "You can build it manually: go build ./cmd/forge"
    exit 1
fi

chmod +x "${TMP_BIN}"

# Install to system path
echo "Installing to ${INSTALL_DIR}/${BINARY_NAME} (requires sudo)..."
if [ -w "${INSTALL_DIR}" ]; then
    mv "${TMP_BIN}" "${INSTALL_DIR}/${BINARY_NAME}"
else
    sudo mv "${TMP_BIN}" "${INSTALL_DIR}/${BINARY_NAME}"
fi

echo "--- Forge installed successfully! ---"
echo "Run 'forge --help' to get started."
