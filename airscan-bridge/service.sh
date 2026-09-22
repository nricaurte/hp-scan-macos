#!/usr/bin/env bash
# Per-user local-only scanner service; no root privileges or LAN listener.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LABEL="io.github.kimdongup.hp-soapht-airscan"
SERVICE="gui/$(id -u)/$LABEL"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
DEST="$HOME/Library/Application Support/HP SOAPHT AirScan"
LOG_DIR="$HOME/Library/Logs/HP SOAPHT AirScan"
APP="$HOME/Applications/HP AirScan Bridge.app"
case "${1:-status}" in
 install)
  OUT_DIR="${OUT_DIR:-$ROOT/build/airscan}"
  test -x "$OUT_DIR/airscan-bridge"
  if [[ -z "${AIRSCAN_WSD_URL:-}" ]]; then test -x "$OUT_DIR/hp-soapht-probe"; fi
  mkdir -p "$DEST" "$LOG_DIR" "$(dirname "$PLIST")"
  launchctl bootout "$SERVICE" >/dev/null 2>&1 || true
  # bootout may return before native CancelJob cleanup finishes. Refuse to
  # replace binaries if the old service still has a listener.
  if lsof -nP -iTCP:"${AIRSCAN_PORT:-8089}" -sTCP:LISTEN >/dev/null 2>&1; then
   echo "Port is still in use; stop the active bridge and wait for scan cleanup." >&2; exit 1
  fi
  mkdir -p "$APP/Contents/MacOS"
  install -m 0755 "$OUT_DIR/airscan-bridge" "$APP/Contents/MacOS/airscan-bridge"
  if [[ -z "${AIRSCAN_WSD_URL:-}" ]]; then install -m 0755 "$OUT_DIR/hp-soapht-probe" "$DEST/"; fi
  python3 - "$PLIST" "$DEST" "$LOG_DIR" "$APP" <<'PY'
import os,plistlib,sys
plist,dest,logs,app=sys.argv[1:]
bundle='io.github.kimdongup.hp-airscan-bridge'
info={'CFBundleIdentifier':bundle,'CFBundleName':'HP AirScan Bridge',
 'CFBundleDisplayName':'HP AirScan Bridge','CFBundleExecutable':'airscan-bridge',
 'CFBundlePackageType':'APPL','CFBundleVersion':'1','CFBundleShortVersionString':'0.5.0',
 'LSUIElement':True,
 'NSLocalNetworkUsageDescription':'Connect to your HP network scanner and make it available in Image Capture and Preview.'}
with open(app+'/Contents/Info.plist','wb') as f:plistlib.dump(info,f)
env={'PATH':'/opt/homebrew/bin:/usr/bin:/bin','AIRSCAN_PROBE':dest+'/hp-soapht-probe'}
for key in ('AIRSCAN_NAME','AIRSCAN_WSD_URL','AIRSCAN_DEVICE','AIRSCAN_PORT','AIRSCAN_SCANIMAGE','AIRSCAN_DEBUG'):
 if os.environ.get(key):env[key]=os.environ[key]
data={'Label':'io.github.kimdongup.hp-soapht-airscan','ProgramArguments':[app+'/Contents/MacOS/airscan-bridge'],
 'AssociatedBundleIdentifiers':[bundle],
 'RunAtLoad':True,'KeepAlive':True,'ThrottleInterval':15,'ExitTimeOut':400,
 'EnvironmentVariables':env,'StandardOutPath':logs+'/bridge.log','StandardErrorPath':logs+'/bridge.log'}
with open(plist,'wb') as f:plistlib.dump(data,f)
PY
  # Ad-hoc signing supports local builds. Distributors should supply their
  # Apple-issued identity so macOS tracks network permission across updates.
  codesign --force --sign "${AIRSCAN_SIGN_IDENTITY:--}" "$APP"
  codesign --verify --strict "$APP"
  /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP"
  launchctl bootstrap "gui/$(id -u)" "$PLIST"
  echo "Installed local scanner service. Log: $LOG_DIR/bridge.log"
  if [[ -n "${AIRSCAN_WSD_URL:-}" ]]; then
   echo 'Allow Local Network access for HP AirScan Bridge when macOS asks.'
  fi
  ;;
 stop) launchctl bootout "$SERVICE" ;;
 start) launchctl bootstrap "gui/$(id -u)" "$PLIST" ;;
 status) launchctl print "$SERVICE" ;;
 uninstall)
  launchctl bootout "$SERVICE" >/dev/null 2>&1 || true
  if [[ -f "$PLIST" ]]; then rm "$PLIST"; fi
  echo "LaunchAgent removed. App, helpers and logs retained in $APP, $DEST and $LOG_DIR."
  ;;
 *) echo "Usage: $0 install|start|stop|status|uninstall" >&2; exit 2 ;;
esac
