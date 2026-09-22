#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HPLIP_SRC="${HPLIP_SRC:-/tmp/hplip-build/hplip-3.25.8}"
TEST_OUT="${TEST_OUT:-$ROOT/build/tests}"
mkdir -p "$TEST_OUT"
SDKROOT_PATH="$(xcrun --show-sdk-path)"
clang -g -O1 -Wall -Wextra -Werror -fsanitize=address,undefined \
    -I"$HPLIP_SRC/scan/sane" -I"$HPLIP_SRC/io/hpmud" \
    -I"$HPLIP_SRC/ip" -I"$HPLIP_SRC" \
    -I"$SDKROOT_PATH/usr/include/libxml2" -lxml2 \
    "$ROOT/tests/soapht_test.c" -o "$TEST_OUT/soapht_test"
SOAPHT_FIXTURE_DIR="$ROOT/tests/fixtures" "$TEST_OUT/soapht_test"
HPLIP_SRC="$HPLIP_SRC" TEST_OUT="$TEST_OUT" python3 "$ROOT/tests/musb_transport_test.py"
python3 "$ROOT/tests/wrapper_test.py"

# Optional compiled-backend integration check. No physical scanner is opened.
if [[ -n "${HPAIO_MODULE:-}" ]]; then
    clang -g -O1 -Wall -Wextra -Werror \
        -I"$HPLIP_SRC/scan/sane" -I"$HPLIP_SRC/io/hpmud" \
        -I"$HPLIP_SRC/ip" -I"$HPLIP_SRC" \
        "$ROOT/tests/soapht_geometry_test.c" -o "$TEST_OUT/soapht_geometry_test"
    "$TEST_OUT/soapht_geometry_test" "$HPAIO_MODULE"
fi
