#!/usr/bin/env bash
# Full build integration regression: fresh extraction, then the same WORK again.
# Reuses only the download between passes; never installs or needs a scanner.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${WORK:-$(mktemp -d /tmp/hplip-reentry.XXXXXX)}"
PREFIX="${PREFIX:-/opt/homebrew}"
HPLIP_VER="${HPLIP_VER:-3.25.8}"
mkdir -p "$WORK"
if [[ -d "$WORK/hplip-$HPLIP_VER" ]]; then
    echo "Use a WORK without an extracted source tree for this regression." >&2
    exit 2
fi
for pass in 1 2; do
    echo "Build reentry pass $pass: $WORK/pass-$pass.log"
    WORK="$WORK" PREFIX="$PREFIX" HPLIP_VER="$HPLIP_VER" INSTALL=0 \
        bash "$ROOT/build.sh" > "$WORK/pass-$pass.log" 2>&1
    python3 - "$WORK/pass-$pass.log" "$WORK/hplip-$HPLIP_VER/Makefile" "$PREFIX" <<'PY'
import re
import sys
from pathlib import Path

log = Path(sys.argv[1]).read_text()
makefile = Path(sys.argv[2]).read_text()
prefix = sys.argv[3]
assert "Build-only complete:" in log
assert "--run automake" not in log, "maintainer regeneration ran"
assert " -o libhpipp.la " not in log, "empty network library was linked"
assert 'hplip_confdir = ' + prefix + '/etc/hp' in makefile
assert not re.search(r'CONFDIR=.*?/etc/hp', log.replace(prefix + '/etc/hp', 'PREFIX_CONFIG')), "wrong runtime config path"
for variable in ('libsane_hpaio_la_DEPENDENCIES', 'libsane_hpaio_la_LIBADD'):
    unfolded = makefile.replace('\\\n', ' ')
    value = re.search(r'^' + variable + r'\s*=([^\n]*)', unfolded, re.M)
    assert value and 'libhpipp.la' not in value[1], variable
PY
done
echo "Fresh and repeated build passed; logs retained in $WORK"
