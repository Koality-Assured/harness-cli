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

VERSION_FLAG=""
while [[ $# -gt 0 ]]; do
  case $1 in
    -v|--version)
      VERSION_FLAG="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1"
      exit 1
      ;;
  esac
done

if [ -n "$VERSION_FLAG" ]; then
  if [[ $VERSION_FLAG == v* ]]; then
    LATEST_RELEASE="$VERSION_FLAG"
  else
    LATEST_RELEASE="v$VERSION_FLAG"
  fi
else
  LATEST_RELEASE=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
  if [ -z "$LATEST_RELEASE" ]; then
    echo "Error: Failed to fetch latest release from GitHub API."
    exit 1
  fi
fi

VERSION="${LATEST_RELEASE#v}"

ARCHIVE_NAME="harness_${VERSION}_${OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_RELEASE}/${ARCHIVE_NAME}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${LATEST_RELEASE}/checksums.txt"

echo "Downloading ${DOWNLOAD_URL}..."
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

curl -fsSL "${DOWNLOAD_URL}" -o "${TMP_DIR}/${ARCHIVE_NAME}"

echo "Downloading checksums.txt..."
curl -fsSL "${CHECKSUMS_URL}" -o "${TMP_DIR}/checksums.txt"

cd "${TMP_DIR}"
if grep "${ARCHIVE_NAME}" checksums.txt > expected.sha256; then
  if ! sha256sum -c expected.sha256; then
    echo "Checksum verification failed!"
    exit 1
  fi
  echo "Checksum verified."
else
  echo "Checksum for ${ARCHIVE_NAME} not found!"
  exit 1
fi
cd - > /dev/null

tar -xzf "${TMP_DIR}/${ARCHIVE_NAME}" -C "${TMP_DIR}"

mkdir -p "${INSTALL_DIR}"
mv "${TMP_DIR}/harness" "${INSTALL_DIR}/harness"
chmod +x "${INSTALL_DIR}/harness"

echo "Installed harness successfully to ${INSTALL_DIR}/harness"

if [[ ":$PATH:" != *":${INSTALL_DIR}:"* ]]; then
  echo "Notice: Add ${INSTALL_DIR} to your PATH to run 'harness' anywhere:"
  echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
fi
