#!/bin/sh
set -e

REPO="moesaif/agentd"
BINARY="agentd"
INSTALL_DIR="/usr/local/bin"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case $ARCH in
  x86_64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
esac

VERSION=$(curl -s "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)

if [ -z "$VERSION" ]; then
  echo "Error: could not determine latest version"
  exit 1
fi

URL="https://github.com/$REPO/releases/download/$VERSION/${BINARY}-${OS}-${ARCH}"

echo "Installing agentd $VERSION ($OS/$ARCH)..."
curl -fsSL "$URL" -o "/tmp/$BINARY"
chmod +x "/tmp/$BINARY"

if [ -w "$INSTALL_DIR" ]; then
  mv "/tmp/$BINARY" "$INSTALL_DIR/$BINARY"
else
  echo "Need sudo to install to $INSTALL_DIR"
  sudo mv "/tmp/$BINARY" "$INSTALL_DIR/$BINARY"
fi

echo "agentd $VERSION installed to $INSTALL_DIR/$BINARY"
echo ""
echo "Run 'agentd init' to get started."
