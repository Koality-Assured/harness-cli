#!/usr/bin/env bash
set -euo pipefail

REPO="Koality-Assured/harness-cli"
INSTALL_DIR="${HOME}/.local/bin"

echo "=== Installing Harness CLI ==="

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "${ARCH}" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: ${ARCH}"; exit 1 ;;
esac

case "${OS}" in
  darwin|linux) ;;
  *) echo "Unsupported OS: ${OS}"; exit 1 ;;
esac

LATEST_RELEASE=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
VERSION="${LATEST_RELEASE#v}"

ARCHIVE_NAME="harness_${VERSION}_${OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_RELEASE}/${ARCHIVE_NAME}"

echo "Downloading ${DOWNLOAD_URL}..."
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

curl -fsSL "${DOWNLOAD_URL}" -o "${TMP_DIR}/${ARCHIVE_NAME}"
tar -xzf "${TMP_DIR}/${ARCHIVE_NAME}" -C "${TMP_DIR}"

mkdir -p "${INSTALL_DIR}"
mv "${TMP_DIR}/harness" "${INSTALL_DIR}/harness"
chmod +x "${INSTALL_DIR}/harness"

echo "Installed harness successfully to ${INSTALL_DIR}/harness"

if [[ ":$PATH:" != *":${INSTALL_DIR}:"* ]]; then
  echo "Notice: Add ${INSTALL_DIR} to your PATH to run 'harness' anywhere:"
  echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
fi
