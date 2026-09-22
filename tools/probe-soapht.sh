#!/usr/bin/env bash
# Read-only raw capability capture: ./tools/probe-soapht.sh 'hpaio:/usb/...' > caps.xml
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HPLIP_SRC="${HPLIP_SRC:-/tmp/hplip-build/hplip-3.25.8}"
PROBE_LIBS="${PROBE_LIBS:-$HPLIP_SRC/.libs}"
if [[ $# != 1 ]]; then
    echo "Usage: $0 'hpaio:/usb/...' > capabilities.xml" >&2
    exit 2
fi
SDKROOT_PATH="$(xcrun --show-sdk-path)"
PROBE_WORK="$(mktemp -d /tmp/soapht-probe.XXXXXX)"
trap 'rm -r "$PROBE_WORK"' EXIT
clang -O2 -Wall -Wextra -Werror \
    -I"$HPLIP_SRC/scan/sane" -I"$HPLIP_SRC/io/hpmud" -I"$HPLIP_SRC/ip" -I"$HPLIP_SRC" \
    -I"$SDKROOT_PATH/usr/include/libxml2" -L"$PROBE_LIBS" -lxml2 -lhpmud \
    "$ROOT/tools/soapht-probe.c" -o "$PROBE_WORK/probe"
export DYLD_LIBRARY_PATH="$PROBE_LIBS"
"$PROBE_WORK/probe" "$1"
