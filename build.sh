#!/usr/bin/env bash
# Build & install HPLIP scanner backend (hpaio) for macOS (arm64 or Intel).
#
# Tested on:
#   macOS 26 (Tahoe), arm64, Homebrew sane-backends 1.4.0, libusb 1.0.29
#   macOS 15 (Sequoia), x86_64, MacPorts sane-backends 1.3.1, libusb 1.0.30
#   HPLIP 3.25.8
#
# What this does:
#   1. downloads HPLIP 3.25.8 source
#   2. applies patches/ to make the SANE backend build on Darwin
#   3. compiles libsane-hpaio.1.so + libhpmud + libhpip
#   4. installs to the package manager's prefix (Homebrew or MacPorts)
#   5. registers `hpaio` in SANE's dll.conf
#   6. installs $PREFIX/bin/hp-scan wrapper
#
# Run it as your normal user, not with sudo; it asks for your password only
# when the prefix is root-owned (MacPorts' /opt/local).
#
# Overrides:
#   PKG_MGR=brew|macports   pick the package manager when both are installed
#   PREFIX=/some/path       install somewhere else (implies no auto-install)
#
# After this, `scanimage -L` should list your HP printer and you can scan
# with `hp-scan` (or any SANE frontend).

set -euo pipefail

HPLIP_VER="${HPLIP_VER:-3.25.8}"
WORK="${WORK:-/tmp/hplip-build}"
HPLIP_URL="https://sourceforge.net/projects/hplip/files/hplip/${HPLIP_VER}/hplip-${HPLIP_VER}.tar.gz/download"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
blue()  { printf '\033[34m%s\033[0m\n' "$*"; }

require() { command -v "$1" >/dev/null 2>&1 || { red "Missing: $1"; exit 1; }; }

if [[ $EUID -eq 0 ]]; then
    red "Don't run build.sh with sudo; it asks for your password when it needs it."
    exit 1
fi

blue "==> checking prerequisites"
require curl
require gcc
require make
require python3

# Pick a package manager: explicit PKG_MGR, else whichever one already has
# sane-backends, else whichever one is installed (Homebrew first).
has_brew() { command -v brew >/dev/null 2>&1; }
has_port() { command -v port >/dev/null 2>&1; }
brew_has_sane() { has_brew && brew list --formula sane-backends >/dev/null 2>&1; }
port_has_sane() { has_port && port -q installed sane-backends 2>/dev/null | grep -q '(active)'; }

if [[ -z "${PKG_MGR:-}" ]]; then
    if   brew_has_sane; then PKG_MGR=brew
    elif port_has_sane; then PKG_MGR=macports
    elif has_brew;      then PKG_MGR=brew
    elif has_port;      then PKG_MGR=macports
    elif [[ -n "${PREFIX:-}" ]]; then PKG_MGR=none
    else
        red "Neither Homebrew nor MacPorts found. Install one of them, or set PREFIX"
        red "to wherever libusb-1.0 and sane-backends are installed."
        exit 1
    fi
fi

case "$PKG_MGR" in
    brew)
        require brew
        PREFIX="${PREFIX:-$(brew --prefix)}"
        for pkg in libusb sane-backends; do
            if ! brew list --formula "$pkg" >/dev/null 2>&1; then
                blue "==> installing $pkg"
                brew install "$pkg"
            fi
        done
        ;;
    macports)
        require port
        PREFIX="${PREFIX:-$(dirname "$(dirname "$(command -v port)")")}"
        missing=()
        for pkg in libusb sane-backends pkgconfig; do
            port -q installed "$pkg" 2>/dev/null | grep -q '(active)' || missing+=("$pkg")
        done
        if (( ${#missing[@]} )); then
            blue "==> installing ${missing[*]} (MacPorts needs your password)"
            sudo port install "${missing[@]}"
        fi
        ;;
    none) ;;
    *)
        red "Unknown PKG_MGR=$PKG_MGR (expected brew or macports)"
        exit 1
        ;;
esac
blue "==> using $PKG_MGR, prefix $PREFIX"

# Homebrew's prefix is user-writable; MacPorts' /opt/local is root-owned.
SUDO=""
if [[ ! -w "$PREFIX/lib" ]]; then
    SUDO="sudo"
fi

# An earlier `sudo ./build.sh` leaves the app root-owned; catch that before
# spending time on the build.
APP="$HOME/Applications/HP Scan.app"
if [[ -e "$APP" && ! -w "$APP/Contents/MacOS/hp-scan-app" ]]; then
    red "$APP is not writable (probably from an earlier sudo run). Fix with:"
    red "  sudo chown -R $(id -un) \"$APP\""
    exit 1
fi

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
    # -N: never auto-reverse. Without it, macOS patch silently answers "yes" to
    # "Reversed (or previously applied) patch detected! Assume -R?" and every
    # second run un-applies the patch.
    if patch -p1 -N --dry-run --silent < "$p" >/dev/null 2>&1; then
        patch -p1 -N < "$p"
    elif patch -p1 -R --dry-run --silent < "$p" >/dev/null 2>&1; then
        echo "already applied: $(basename "$p")"
    else
        red "patch does not apply: $p"
        patch -p1 -N --dry-run < "$p" || true
        exit 1
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
# and point hplip_confdir to our prefix.
sed -i.bak \
    -e 's|^hplip_confdir = /etc/hp$|hplip_confdir = '"$PREFIX"'/etc/hp|' \
    Makefile

# Remove libhpipp.la from libsane_hpaio_la link deps (it's empty when network/IPP
# is disabled, and `ar cr` errors on empty archives).
# Quoted heredoc: an unquoted one lets bash eat the regex backslashes.
python3 - <<'PYEOF'
import re, pathlib
mk = pathlib.Path('Makefile')
src = mk.read_text()
src = re.sub(r'(libsane_hpaio_la_DEPENDENCIES = libhpip\.la \\\n\tlibhpmud\.la \\\n)\tlibhpipp\.la \\\n', r'\1', src)
src = re.sub(r'(libsane_hpaio_la_LIBADD = libhpip\.la \\\n\tlibhpmud\.la \\\n)\tlibhpipp\.la \\\n', r'\1', src)
mk.write_text(src)
PYEOF

blue "==> make libsane-hpaio.la"
MAKE_LOG="$WORK/make.log"
make -k libhpmud.la libhpip.la libsane-hpaio.la > "$MAKE_LOG" 2>&1 || true
grep -E '(error:|Error)' "$MAKE_LOG" | grep -v 'libhpipp' || true

if [[ ! -f .libs/libsane-hpaio.1.so ]]; then
    red "build failed — libsane-hpaio.1.so was not produced (full log: $MAKE_LOG)"
    exit 1
fi

blue "==> installing into $PREFIX"
[[ -n "$SUDO" ]] && blue "    ($PREFIX is root-owned; sudo may ask for your password)"
$SUDO mkdir -p "$PREFIX/lib/sane" "$PREFIX/etc/sane.d" "$PREFIX/etc/hp" "$PREFIX/share/hplip/data/models/unreleased"
$SUDO cp .libs/libhpmud.0.dylib    "$PREFIX/lib/"
$SUDO cp .libs/libhpip.0.dylib     "$PREFIX/lib/"
$SUDO ln -sf libhpmud.0.dylib "$PREFIX/lib/libhpmud.dylib"
$SUDO ln -sf libhpip.0.dylib  "$PREFIX/lib/libhpip.dylib"
$SUDO cp .libs/libsane-hpaio.1.so "$PREFIX/lib/sane/"
$SUDO ln -sf libsane-hpaio.1.so "$PREFIX/lib/sane/libsane-hpaio.so"
$SUDO cp hplip.conf            "$PREFIX/etc/hp/"
$SUDO cp data/models/models.dat "$PREFIX/share/hplip/data/models/"
if [[ -f data/models/unreleased/unreleased.dat ]]; then
    $SUDO cp data/models/unreleased/unreleased.dat "$PREFIX/share/hplip/data/models/unreleased/"
else
    $SUDO touch "$PREFIX/share/hplip/data/models/unreleased/unreleased.dat"
fi

# enable hpaio in dll.conf if not already there
DLL_CONF="$PREFIX/etc/sane.d/dll.conf"
if [[ ! -f "$DLL_CONF" ]] || ! grep -q '^hpaio$' "$DLL_CONF"; then
    echo 'hpaio' | $SUDO tee -a "$DLL_CONF" >/dev/null
fi

# install hp-scan wrapper
$SUDO install -m 0755 "$SCRIPT_DIR/bin/hp-scan" "$PREFIX/bin/hp-scan"

# install HP Scan.app (clickable GUI wrapper) into ~/Applications
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
install -m 0644 "$SCRIPT_DIR/app/Info.plist"   "$APP/Contents/Info.plist"
# the app script hardcodes /opt/homebrew; point it at $PREFIX instead
sed "s|/opt/homebrew|$PREFIX|g" "$SCRIPT_DIR/app/hp-scan-app" > "$WORK/hp-scan-app"
install -m 0755 "$WORK/hp-scan-app"            "$APP/Contents/MacOS/hp-scan-app"
# refresh Launch Services so Spotlight / Dock find the new app
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP" >/dev/null 2>&1 || true

# build & install airscan-bridge (eSCL → SANE adapter so Apple's native scan
# apps -- Image Capture, Preview, HP Easy Scan, Notes, iPhone/iPad scan -- can
# discover and use the printer over Bonjour without needing an ICA driver).
if command -v go >/dev/null 2>&1; then
    blue "==> building airscan-bridge"
    (cd "$SCRIPT_DIR/airscan-bridge" && go build -o airscan-bridge .)
    $SUDO install -m 0755 "$SCRIPT_DIR/airscan-bridge/airscan-bridge" "$PREFIX/bin/airscan-bridge"
    # the plist hardcodes /opt/homebrew; point it at $PREFIX instead
    PLIST="$WORK/com.nricaurte.hp-airscan.plist"
    sed "s|/opt/homebrew|$PREFIX|g" "$SCRIPT_DIR/airscan-bridge/com.nricaurte.hp-airscan.plist" > "$PLIST"
    green ""
    green "Optional: install LaunchAgent so the bridge starts at login:"
    green "  cp $PLIST ~/Library/LaunchAgents/"
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
