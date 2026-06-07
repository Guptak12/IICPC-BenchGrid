#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export PATH="$PATH:$HOME/go/bin:/opt/homebrew/bin"

if ! command -v protoc &> /dev/null; then
    echo "Error: protoc is not installed or not in PATH."
    exit 1
fi

if ! command -v protoc-gen-go &> /dev/null; then
    echo "Error: protoc-gen-go plugin is not installed."
    exit 1
fi

echo "Compiling trading.proto..."
protoc --go_out=. --go_opt=paths=source_relative pkg/protocol/trading.proto
echo "Proto compiled successfully ✓"
