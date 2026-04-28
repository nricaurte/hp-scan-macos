#!/usr/bin/env bash
# Build & install HPLIP scanner backend (hpaio) for macOS, Apple Silicon.
#
# Tested on:
#   macOS 26 (Tahoe), arm64
#   HPLIP 3.25.8
#   Homebrew sane-backends 1.4.0, libusb 1.0.29
#
# What this does:
#   1. downloads HPLIP 3.25.8 source
#   2. applies patches/ to make the SANE backend build on Darwin
#   3. compiles libsane-hpaio.1.so + libhpmud + libhpip
#   4. installs to /opt/homebrew (Homebrew prefix)
#   5. registers `hpaio` in SANE's dll.conf
#   6. installs /opt/homebrew/bin/hp-scan wrapper
#
# After this, `scanimage -L` should list your HP printer and you can scan
# with `hp-scan` (or any SANE frontend).

set -euo pipefail

HPLIP_VER="${HPLIP_VER:-3.25.8}"
PREFIX="${PREFIX:-/opt/homebrew}"
WORK="${WORK:-/tmp/hplip-build}"
HPLIP_URL="https://sourceforge.net/projects/hplip/files/hplip/${HPLIP_VER}/hplip-${HPLIP_VER}.tar.gz/download"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
blue()  { printf '\033[34m%s\033[0m\n' "$*"; }

require() { command -v "$1" >/dev/null 2>&1 || { red "Missing: $1"; exit 1; }; }

blue "==> checking prerequisites"
require brew
require curl
require gcc
require make

for pkg in libusb sane-backends; do
    if ! brew list --formula "$pkg" >/dev/null 2>&1; then
        blue "==> installing $pkg"
        brew install "$pkg"
    fi
done

mkdir -p "$WORK"
cd "$WORK"

if [[ ! -f "hplip-${HPLIP_VER}.tar.gz" ]]; then
    blue "==> downloading HPLIP ${HPLIP_VER}"
    curl -L -o "hplip-${HPLIP_VER}.tar.gz" "$HPLIP_URL"
fi

if [[ ! -d "hplip-${HPLIP_VER}" ]]; then
    blue "==> extracting"
    tar xzf "hplip-${HPLIP_VER}.tar.gz"
fi

cd "hplip-${HPLIP_VER}"

blue "==> applying patches"
# Replace orblite stub files outright (cleaner than diff for full-file replacement).
cp "$SCRIPT_DIR/stubs/orblitei.h" scan/sane/orblitei.h
cp "$SCRIPT_DIR/stubs/orblite.c"  scan/sane/orblite.c

# Apply remaining diffs only if they haven't been applied already.
for p in "$SCRIPT_DIR"/patches/01-darwin-headers.patch \
         "$SCRIPT_DIR"/patches/02-musb-macos.patch \
         "$SCRIPT_DIR"/patches/03-hpaio-uninit-fix.patch; do
    if patch -p1 --dry-run --silent < "$p" >/dev/null 2>&1; then
        patch -p1 < "$p"
    else
        # already applied; skip
        :
    fi
done

blue "==> configure"
PKG_CONFIG_PATH="$PREFIX/lib/pkgconfig" \
CFLAGS="-I$PREFIX/include -I$PREFIX/include/libusb-1.0 -I$PREFIX/include/sane -D_DARWIN_C_SOURCE -Wno-error -Wno-implicit-function-declaration -fcommon" \
CPPFLAGS="-I$PREFIX/include -I$PREFIX/include/libusb-1.0 -I$PREFIX/include/sane" \
LDFLAGS="-L$PREFIX/lib" \
./configure --prefix="$PREFIX" \
    --enable-lite-build \
    --disable-network-build \
    --disable-fax-build \
    --disable-gui-build \
    --disable-doc-build \
    --disable-dbus-build \
    --disable-pp-build \
    --disable-policykit \
    --disable-imageProcessor-build \
    --enable-scan-build \
    --disable-cups-drv-install \
    --disable-foomatic-drv-install \
    --disable-foomatic-ppd-install \
    --disable-cups-ppd-install >/dev/null

# Patch the generated Makefile: drop libhpipp dependency (we don't build it),
# and point hplip_confdir to our Homebrew prefix.
sed -i.bak \
    -e 's|^hplip_confdir = /etc/hp$|hplip_confdir = '"$PREFIX"'/etc/hp|' \
    Makefile

# Remove libhpipp.la from libsane_hpaio_la link deps (it's empty when network/IPP
# is disabled, and `ar cr` errors on empty archives).
python3 - <<PYEOF
import re, pathlib
mk = pathlib.Path('Makefile')
src = mk.read_text()
src = re.sub(r'(libsane_hpaio_la_DEPENDENCIES = libhpip\.la \\\n\tlibhpmud\.la \\\n)\tlibhpipp\.la \\\n', r'\1', src)
src = re.sub(r'(libsane_hpaio_la_LIBADD = libhpip\.la \\\n\tlibhpmud\.la \\\n)\tlibhpipp\.la \\\n', r'\1', src)
mk.write_text(src)
PYEOF

blue "==> make libsane-hpaio.la"
make -k libhpmud.la libhpip.la libsane-hpaio.la 2>&1 | grep -E '(error:|Error)' | grep -v 'libhpipp' || true

if [[ ! -f .libs/libsane-hpaio.1.so ]]; then
    red "build failed — libsane-hpaio.1.so was not produced"
    exit 1
fi

blue "==> installing into $PREFIX"
mkdir -p "$PREFIX/lib/sane" "$PREFIX/etc/sane.d" "$PREFIX/etc/hp" "$PREFIX/share/hplip/data/models/unreleased"
cp .libs/libhpmud.0.dylib    "$PREFIX/lib/"
cp .libs/libhpip.0.dylib     "$PREFIX/lib/"
ln -sf libhpmud.0.dylib "$PREFIX/lib/libhpmud.dylib"
ln -sf libhpip.0.dylib  "$PREFIX/lib/libhpip.dylib"
cp .libs/libsane-hpaio.1.so "$PREFIX/lib/sane/"
ln -sf libsane-hpaio.1.so "$PREFIX/lib/sane/libsane-hpaio.so"
cp hplip.conf            "$PREFIX/etc/hp/"
cp data/models/models.dat "$PREFIX/share/hplip/data/models/"
[[ -f data/models/unreleased/unreleased.dat ]] && cp data/models/unreleased/unreleased.dat "$PREFIX/share/hplip/data/models/unreleased/" || \
    : > "$PREFIX/share/hplip/data/models/unreleased/unreleased.dat"

# enable hpaio in dll.conf if not already there
DLL_CONF="$PREFIX/etc/sane.d/dll.conf"
if [[ ! -f "$DLL_CONF" ]] || ! grep -q '^hpaio$' "$DLL_CONF"; then
    echo 'hpaio' >> "$DLL_CONF"
fi

# install hp-scan wrapper
install -m 0755 "$SCRIPT_DIR/bin/hp-scan" "$PREFIX/bin/hp-scan"

# install HP Scan.app (clickable GUI wrapper) into ~/Applications
APP="$HOME/Applications/HP Scan.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
install -m 0644 "$SCRIPT_DIR/app/Info.plist"   "$APP/Contents/Info.plist"
install -m 0755 "$SCRIPT_DIR/app/hp-scan-app"  "$APP/Contents/MacOS/hp-scan-app"
# refresh Launch Services so Spotlight / Dock find the new app
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP" >/dev/null 2>&1 || true

# build & install airscan-bridge (eSCL → SANE adapter so Apple's native scan
# apps -- Image Capture, Preview, HP Easy Scan, Notes, iPhone/iPad scan -- can
# discover and use the printer over Bonjour without needing an ICA driver).
if command -v go >/dev/null 2>&1; then
    blue "==> building airscan-bridge"
    (cd "$SCRIPT_DIR/airscan-bridge" && go build -o airscan-bridge .)
    install -m 0755 "$SCRIPT_DIR/airscan-bridge/airscan-bridge" "$PREFIX/bin/airscan-bridge"
    green ""
    green "Optional: install LaunchAgent so the bridge starts at login:"
    green "  cp $SCRIPT_DIR/airscan-bridge/com.nricaurte.hp-airscan.plist ~/Library/LaunchAgents/"
    green "  launchctl load -w ~/Library/LaunchAgents/com.nricaurte.hp-airscan.plist"
    green ""
    green "Or run it ad-hoc:  airscan-bridge &"
else
    blue "==> skipping airscan-bridge build (Go not installed)"
fi

green ""
green "✔ done."
green ""
green "Try:   scanimage -L                          # confirm scanner detected"
green "       hp-scan ~/Desktop/test.pdf 300        # CLI scan"
green "       open '$APP'                          # GUI scan (also in Spotlight as 'HP Scan')"
green ""
