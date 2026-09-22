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
INSTALL="${INSTALL:-1}" # INSTALL=0 builds without changing the installed scanner.
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
require clang
require python3
require patch
require tar

if [[ "$INSTALL" != 0 && "$INSTALL" != 1 ]]; then
    red "INSTALL must be 0 or 1"
    exit 1
fi

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
    curl -fL -o "hplip-${HPLIP_VER}.tar.gz.part" "$HPLIP_URL"
    mv "hplip-${HPLIP_VER}.tar.gz.part" "hplip-${HPLIP_VER}.tar.gz"
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
# macOS /usr/bin/patch prompts on /dev/tty when a hunk looks reversed or
# already applied. -N skips those hunks; -t never asks. Without both, a
# redirected dry-run looks frozen (hidden "Assume -R?" prompt).
for p in "$SCRIPT_DIR"/patches/01-darwin-headers.patch \
         "$SCRIPT_DIR"/patches/02-musb-macos.patch \
         "$SCRIPT_DIR"/patches/03-hpaio-uninit-fix.patch \
         "$SCRIPT_DIR"/patches/04-soapht-macos-plugin.patch; do
    if patch -p1 -N -t --dry-run -s < "$p" >/dev/null 2>&1; then
        patch -p1 -N -t < "$p"
    elif patch -p1 -R -t --dry-run -s < "$p" >/dev/null 2>&1; then
        blue "==> already applied: $(basename "$p")"
    else
        red "Patch does not match: $p"
        red "Use a fresh WORK directory; an older patched tree cannot be safely reused."
        exit 1
    fi
done

blue "==> configure (about a minute, please wait)"
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
    --disable-cups-ppd-install

blue "==> patching generated Makefile for macOS/Homebrew"

# HPLIP runtime configuration must live under the Homebrew prefix.
sed -i '' \
    "s|^hplip_confdir = .*|hplip_confdir = $PREFIX/etc/hp|" \
    Makefile

# libhpipp is empty because network build is disabled.
# macOS ar refuses to create an empty archive, so remove it from
# libsane-hpaio dependencies/link inputs.
python3 - <<'PYEOF'
from pathlib import Path

mk = Path("Makefile")
src = mk.read_text()

src = src.replace('\tlibhpipp.la \\\n', '')
src = src.replace(' libhpipp.la', '')

mk.write_text(src)
PYEOF

blue "==> building HPLIP scanner libraries"
# configure has already generated these files. Newer included .inc files in a
# reused source tree can otherwise make it invoke legacy Automake and
# config.status again, discarding the macOS edits above. Build the configured
# release tarball without re-running its maintainer rules (no timestamp hacks).
release_make=(make -o Makefile -o Makefile.in -o configure -o config.h.in)
# Compiler flags/CONFDIR are not tracked as object dependencies. A reused WORK
# may contain objects from a failed regeneration or a different PREFIX.
"${release_make[@]}" clean
"${release_make[@]}" libhpmud.la libhpip.la libsane-hpaio.la


shopt -s nullglob
hpaio_mod=(.libs/libsane-hpaio.1.so .libs/libsane-hpaio.so .libs/libsane-hpaio.1.dylib .libs/libsane-hpaio.dylib)
if [[ ! -e ${hpaio_mod[0]-} ]]; then
    red "build failed — libsane-hpaio module was not produced"
    ls -la .libs/libsane-hpaio* 2>/dev/null || true
    exit 1
fi
HPAIO_MOD="${hpaio_mod[0]}"

blue "==> building native macOS SOAPHT plugin"
SOAPHT_MOD="$PWD/.libs/bb_soapht.so"
HPLIP_SRC="$PWD" OUT="$SOAPHT_MOD" \
    "$SCRIPT_DIR/soapht-macos/build-plugin.sh"

# Build the optional local eSCL bridge before installation, including in
# INSTALL=0 mode, so its tested artifacts can be installed as a pair.
AIRSCAN_OUT="$PWD/.libs/airscan"
if command -v go >/dev/null 2>&1; then
    HPLIP_SRC="$PWD" PREFIX="$PREFIX" OUT_DIR="$AIRSCAN_OUT" \
        "$SCRIPT_DIR/airscan-bridge/build.sh"
fi

if [[ "$INSTALL" == 0 ]]; then
    green "Build-only complete: $PWD/.libs (runtime installation unchanged)"
    exit 0
fi

blue "==> installing into $PREFIX"
# Check the privileged plugin destination before changing any runtime files.
# This avoids leaving a new backend paired with an old plugin if sudo fails.
if [[ -t 0 ]]; then
    sudo -v
else
    sudo -n -v || {
        red "Build complete, installation not started: administrator authentication is required."
        red "Run ./build.sh from a terminal to install this backend/plugin pair."
        exit 1
    }
fi
mkdir -p "$PREFIX/lib/sane" "$PREFIX/etc/sane.d" "$PREFIX/etc/hp" "$PREFIX/share/hplip/data/models/unreleased"
cp .libs/libhpmud.0.dylib    "$PREFIX/lib/"
cp .libs/libhpip.0.dylib     "$PREFIX/lib/"
ln -sf libhpmud.0.dylib "$PREFIX/lib/libhpmud.dylib"
ln -sf libhpip.0.dylib  "$PREFIX/lib/libhpip.dylib"
cp "$HPAIO_MOD" "$PREFIX/lib/sane/$(basename "$HPAIO_MOD")"
ln -sf "$(basename "$HPAIO_MOD")" "$PREFIX/lib/sane/libsane-hpaio.so"
# SANE also probes the .1.so name on Darwin even when libtool emitted a dylib.
if [[ "$(basename "$HPAIO_MOD")" != libsane-hpaio.1.so ]]; then
    ln -sf "$(basename "$HPAIO_MOD")" "$PREFIX/lib/sane/libsane-hpaio.1.so"
fi
cp hplip.conf            "$PREFIX/etc/hp/"
cp data/models/models.dat "$PREFIX/share/hplip/data/models/"
[[ -f data/models/unreleased/unreleased.dat ]] && cp data/models/unreleased/unreleased.dat "$PREFIX/share/hplip/data/models/unreleased/" || \
    : > "$PREFIX/share/hplip/data/models/unreleased/unreleased.dat"

# enable hpaio in dll.conf if not already there
DLL_CONF="$PREFIX/etc/sane.d/dll.conf"
if [[ ! -f "$DLL_CONF" ]] || ! grep -q '^hpaio$' "$DLL_CONF"; then
    echo 'hpaio' >> "$DLL_CONF"
fi

blue "==> installing native macOS SOAPHT plugin"

sudo mkdir -p "$PREFIX/share/hplip/scan/plugins"

sudo install -m 0755 \
  "$SOAPHT_MOD" \
  "$PREFIX/share/hplip/scan/plugins/bb_soapht.so"

green "==> SOAPHT plugin installed"

# install hp-scan wrapper
install -m 0755 "$SCRIPT_DIR/bin/hp-scan" "$PREFIX/bin/hp-scan"

# install HP Scan.app (clickable GUI wrapper) into ~/Applications
APP="$HOME/Applications/HP Scan.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
install -m 0644 "$SCRIPT_DIR/app/Info.plist"   "$APP/Contents/Info.plist"
install -m 0755 "$SCRIPT_DIR/app/hp-scan-app"  "$APP/Contents/MacOS/hp-scan-app"
# refresh Launch Services so Spotlight / Dock find the new app
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP" >/dev/null 2>&1 || true

# Optional eSCL adapter for Image Capture and Preview on this Mac.
if [[ -x "$AIRSCAN_OUT/airscan-bridge" ]]; then
    install -m 0755 "$AIRSCAN_OUT/airscan-bridge" "$AIRSCAN_OUT/hp-soapht-probe" "$PREFIX/bin/"
    green "Local AirScan service (per-user):"
    green "  OUT_DIR='$AIRSCAN_OUT' '$SCRIPT_DIR/airscan-bridge/service.sh' install"
else
    blue "==> skipping AirScan bridge (Go not installed)"
fi

green ""
green "✔ done."
green ""
green "Try:   scanimage -L                          # confirm scanner detected"
green "       hp-scan ~/Desktop/test.pdf 300        # CLI scan"
green "       open '$APP'                          # GUI scan (also in Spotlight as 'HP Scan')"
green ""
