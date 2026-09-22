#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="${PREFIX:-/opt/homebrew}"
HPLIP_SRC="${HPLIP_SRC:-/tmp/hplip-build/hplip-3.25.8}"
OUT_DIR="${OUT_DIR:-$ROOT/build/airscan}"
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
SDK="$(xcrun --show-sdk-path)"
clang -O2 -Wall -Wextra -Werror \
 -I"$HPLIP_SRC/scan/sane" -I"$HPLIP_SRC/io/hpmud" -I"$HPLIP_SRC/ip" -I"$HPLIP_SRC" \
 -I"$SDK/usr/include/libxml2" -L"$PREFIX/lib" \
 "$ROOT/tools/soapht-probe.c" -lxml2 -lhpmud -o "$OUT_DIR/hp-soapht-probe"
(cd "$ROOT/airscan-bridge" && go build -trimpath -o "$OUT_DIR/airscan-bridge" .)
echo "Bridge and read-only SOAPHT probe built in $OUT_DIR"
