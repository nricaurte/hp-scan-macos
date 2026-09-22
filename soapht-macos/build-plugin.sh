#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Usage:
#   HPLIP_SRC="/tmp/hplip-build/hplip-3.25.8" ./build-plugin.sh
#
# Set HPLIP_SRC to the HPLIP source root used by hp-scan-macos.
HPLIP_SRC="${HPLIP_SRC:-${WORK:-/tmp/hplip-build}/hplip-${HPLIP_VER:-3.25.8}}"

# Always place the output next to this script unless OUT is explicitly given.
OUT="${OUT:-$SCRIPT_DIR/bb_soapht.so}"

if [[ ! -f "$HPLIP_SRC/scan/sane/soaphti.h" ]]; then
  echo "ERROR: soaphti.h not found under: $HPLIP_SRC" >&2
  echo "Set HPLIP_SRC to your HPLIP source root." >&2
  exit 1
fi

SDKROOT_PATH="$(xcrun --show-sdk-path)"

clang \
  -I"$SDKROOT_PATH/usr/include/libxml2" \
  -dynamiclib \
  -fPIC \
  -O2 \
  -Wall -Wextra \
  -arch arm64 \
  -I"$HPLIP_SRC/scan/sane" \
  -I"$HPLIP_SRC/io/hpmud" \
  -I"$HPLIP_SRC/ip" \
  -I"$HPLIP_SRC" \
  "$SCRIPT_DIR/bb_soapht_macos.c" \
  -lxml2 \
  -Wl,-undefined,dynamic_lookup \
  -o "$OUT"

echo "Built: $OUT"
file "$OUT"
nm -gU "$OUT" | grep ' _bb_'
