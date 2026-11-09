#!/usr/bin/env sh
set -eu
LATEST_URL=$(curl -Ls -o /dev/null -w "%{url_effective}\n" https://github.com/thezim/aicm/releases/latest)
FULL_VERSION="${LATEST_URL##*/}"
SEMANTIC_VERSION="${FULL_VERSION#v}"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)   ARCH="amd64" ;;
    aarch64|arm64)  ARCH="arm64" ;;
    *)              err "unsupported architecture: $ARCH" ;;
esac
FILENAME="aicm_${SEMANTIC_VERSION}_${OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/thezim/aicm/releases/latest/download/${FILENAME}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
DOWNLOAD_FILE="${TMP}/${FILENAME}"
HTTP_STATUS_CODE=$(curl -sL -w "%{http_code}" -o "${DOWNLOAD_FILE}" "${DOWNLOAD_URL}")
if [ "$HTTP_STATUS_CODE" -ne 200 ]; then
    echo "Error: Failed to download ${DOWNLOAD_URL} with HTTP status code ${HTTP_STATUS_CODE}."
    exit 1
fi
tar zxf "${DOWNLOAD_FILE}" -C "${TMP}"
SOURCE_BINARY=$(find "${TMP}" -name "aicm")
INSTALL_PATH="$HOME/.local/bin"
mkdir -p "${INSTALL_PATH}"
cp "${SOURCE_BINARY}" "${INSTALL_PATH}/"
chmod +x "${INSTALL_PATH}/aicm"
