# hplip-macos

A working SANE scanner backend for HP USB-only inkjet/AIO printers on macOS,
including Apple Silicon. Builds HPLIP's `hpaio` backend from source with the
patches needed to compile and run on Darwin.

## Why this exists

HP doesn't ship a macOS scanner driver for low-end USB-only models like the
**Smart Tank 500** (no Wi-Fi). HP Smart and HP Easy Scan both refuse to detect
these printers, and Apple's built-in Image Capture / Preview need a vendor ICA
driver that doesn't exist for them. The only working software on macOS is
VueScan, which is paid.

HPLIP — HP's official open-source driver — is GPL-licensed and supports these
printers fine, but it's Linux-only. This repo packages the Darwin port of
HPLIP's scanner backend so you get the same free scanning macOS users already
take for granted on Wi-Fi-capable HP models.

## Tested

| Component | Confirmed working |
|---|---|
| macOS 26 (Tahoe), arm64 (Apple Silicon) | ✅ |
| HPLIP 3.25.8 | ✅ |
| Homebrew `sane-backends` 1.4.0, `libusb` 1.0.29, `go` 1.26+ | ✅ |
| **HP Smart Tank 500 series (USB-only)** | ✅ |

That's the only model verified end-to-end so far. The rest of the table is what
*should* work given how the patches are written, but **untested — please open an
issue with your model if you try one**.

### Models that should work without further changes

Any HP printer that HPLIP marks as `scan-type=7` (LEDM) and is USB-only or
USB-attached. That includes (verify by greping `data/models/models.dat` in the
HPLIP source for `scan-type=7`):

- HP Smart Tank 500 series ← tested
- HP Smart Tank 510, 530, 550, 570, 580 series (USB variants)
- HP Deskjet 2700, 2300, 4100 series (USB)
- HP Envy 6000, 6020, 6030, 6400 series (USB-attached)
- HP DeskJet Ink Advantage 2300, 2700, 6400 series
- HP OfficeJet 3830, 4650, 5200 series (USB)
- HP LaserJet MFP M28w, M139w, M140 series (USB)

### Models that probably need extra patches

- Anything with `scan-type=4` or `8` (Marvell) — different USB scan protocol;
  the macOS device-id workaround in `02-musb-macos.patch` may need adapting.
- Anything that requires HPLIP's binary plugin (`hp-plugin`) — the build
  script will tell you if your model needs one. Smart Tank 500 doesn't.
- Anything with Wi-Fi where you'd rather just use AirPrint scan natively —
  if your printer already shows up in Image Capture without this repo, you
  don't need any of this.

### Will likely **not** work

- Older parallel-port HPs (we disable `--enable-pp-build`).
- HPs that use proprietary closed protocols (some old DesignJet plotters).
- Anything connected over network where AirScan/IPP is already supposed to work
  but doesn't — that's a different problem (firmware/Bonjour, not driver).

## Install

```bash
git clone https://github.com/nricaurte/hp-scan-macos.git
cd hp-scan-macos
./build.sh
```

Build downloads HPLIP source from SourceForge (~30 MB), applies patches,
compiles to `/opt/homebrew/lib/sane/libsane-hpaio.1.so`, registers `hpaio`
in `/opt/homebrew/etc/sane.d/dll.conf`, and installs `hp-scan` wrapper.

If you don't use Homebrew at the default location, set `PREFIX=/your/path`.

## Install with Claude Code

If you have [Claude Code](https://claude.com/claude-code) installed, you can
let it do the whole thing. Open a terminal in any directory you don't mind a
clone landing in, run `claude`, and paste:

> Install the HP scanner backend from `https://github.com/nricaurte/hp-scan-macos`
> for my HP Smart Tank 500 (USB). Clone it, read the README, run `./build.sh`,
> then run `scanimage -L` to confirm the printer appears, and finally test a
> scan with `hp-scan ~/Desktop/test.pdf 200`. The build needs `brew`, `gcc`,
> `make`, `libusb`, and `sane-backends` — install anything missing.

Claude will clone the repo, install Homebrew prerequisites, run the build,
and verify scanning works end-to-end. Approve any `Bash` permission prompts
as they come up. Replace "Smart Tank 500" with whatever HP USB printer you
have — the same backend handles every LEDM-over-USB model.

If something fails mid-build, paste the error back to Claude — the patches
in this repo are narrow, but other LEDM models may need an additional
quirk. Most failures are header/include issues fixable in one or two
edits.

## Use

**CLI:**
```bash
scanimage -L                                      # should list your HP
hp-scan                                           # scan to ~/Desktop/scan-<date>.pdf at 300 DPI color
hp-scan ~/Desktop/contract.pdf 300 gray           # custom path / DPI / mode
scanimage -d hpaio:/usb/Smart_Tank_500_series?serial=XXX --resolution 600 -o foo.jpg
```

**GUI (HP Scan.app):**
The build also installs a clickable `HP Scan.app` into `~/Applications/`. Double-click,
pick DPI (100/150/200/300/600/1200), pick mode (Color/Gray), pick a save path, scan.
The PDF opens in Preview automatically. Find it in Spotlight as "HP Scan".

**Image Capture / Preview / Notes / iPhone-iPad (via airscan-bridge):**
The build also installs a small Go service (`airscan-bridge`) that advertises
the scanner over Bonjour as an AirScan/eSCL network device on the loopback
interface. Apple's native scan stack (`Image Capture.app`, Preview's
*Import from Scanner*, Notes' document scan, iOS Files / Notes scan over
local network) all use AirScan, so the printer shows up under **Shared** in
Image Capture as `Smart Tank 500 series (USB-bridge)` without any ICA driver.

Run it ad-hoc:
```bash
airscan-bridge &
```
Or persist across reboots with the bundled LaunchAgent:
```bash
cp airscan-bridge/com.nricaurte.hp-airscan.plist ~/Library/LaunchAgents/
launchctl load -w ~/Library/LaunchAgents/com.nricaurte.hp-airscan.plist
```

> **Why all these layers?** macOS's native scan apps use Apple's **ICA**
> framework, which requires a vendor driver in `/Library/Image Capture/Devices/`.
> HPLIP/hpaio is a **SANE** backend — a parallel, Unix-style stack — so it
> doesn't show up in those apps directly. AirScan/eSCL is the third stack
> Apple uses for network scanners; we expose hpaio as an eSCL endpoint on
> localhost and Bonjour does the rest. End result: scanning works in every
> Apple scan app *except* HP Easy Scan, which is HP's own closed app and
> only trusts HP's own discovery channel.

Any SANE-compatible frontend (VueScan, XSane via XQuartz, etc.) will also
see the device through the SANE side once `hpaio` is registered.

## What the patches change

| File | Patch | Reason |
|------|-------|--------|
| `scan/sane/orblitei.h` | replaced with stub (`stubs/orblitei.h`) | Linux header pulled in `OrbliteScan/MacCommon.h`, which `#include`s `<CoreFoundation/CFPlugInCOM.h>`. CFPlugInCOM defines `ULONG = UInt32` (32-bit), conflicting with HPLIP's `hpip.h` which defines `ULONG = unsigned long` (64-bit on arm64). Stub provides only what `hpaio.c` references. |
| `scan/sane/orblite.c`  | replaced with stub (`stubs/orblite.c`) | Original implementation was a CFPlugIn for OS X 10.4-era macOS. We don't need orblite (Smart Tank uses LEDM, not orblite). Stub satisfies the linker. |
| `scan/sane/bb_ledm.c`, `http.c`, `sclpml.c` | `03-darwin-headers.patch` | Adds `<unistd.h>` / `<sys/time.h>` for `usleep`/`gettimeofday`; on Linux these were dragged in transitively by `<syslog.h>`, on macOS they're not. |
| `io/hpmud/musb.c` | `04-musb-macos.patch` | Two fixes: (1) some HP firmwares STALL on the IEEE 1284 `GET_DEVICE_ID` USB control transfer — synthesize a valid response on `LIBUSB_ERROR_PIPE` so the rest of hpmud accepts the device. The model info is already known from `iProduct` string descriptor read earlier. (2) `musb_probe_devices` had `if (!hd) libusb_close(hd)` (closing on NULL on success path) — flipped to `if (hd)`. |
| `scan/sane/hpaio.c` | `05-hpaio-uninit-fix.patch` | `sane_hpaio_get_devices` calls `orblite_get_devices(devList, ...)` with `devList` being an uninitialized `SANE_Device***`. On Linux, the orblite path is rarely entered so the bug is latent; on macOS our stub dereferences the pointer and segfaults. Skip the call on Apple. |

The `Makefile` is also patched at build time (in `build.sh`) to:
- set `hplip_confdir = $PREFIX/etc/hp` instead of `/etc/hp` (so install doesn't need sudo);
- drop `libhpipp.la` from `libsane-hpaio.la` link deps (it's an empty archive when network/IPP build is disabled, and `ar cr` errors on empty archives on macOS).

## Troubleshooting

If you got here from a search engine, here are the literal symptoms this repo fixes:

- **"HP Smart Tank 500 not detected as scanner on macOS"** → install this.
- **"HP Smart isn't finding my printer Mac"** → HP Smart needs Wi-Fi, your model is USB. Install this.
- **"Image Capture doesn't see HP USB printer"** → Apple ICA stack has no driver for your model. Install this + run `airscan-bridge`.
- **"Preview Import from Scanner missing on Mac"** → No driver in `/Library/Image Capture/Devices/`. Install this + run `airscan-bridge`.
- **"HP Easy Scan: select your scanner / no scanner found"** → HP Easy Scan filters by HP's closed channel, won't see this driver. Use Image Capture or Preview instead, both work via `airscan-bridge`.
- **"HP scanner support drop macOS Sonoma / Sequoia / Tahoe"** → Apple deprecating CUPS drivers and ICA. AirScan/eSCL is the surviving path; this repo bridges it.
- **"VueScan is paid, HP Smart doesn't work, what now"** → this. Free, GPL, no watermark.
- **`scanimage: no SANE devices found`** → SANE backend not registered. Run `./build.sh` or check `/opt/homebrew/etc/sane.d/dll.conf` contains `hpaio`.
- **`hpaio: ledm get_device_id failed: LIBUSB_ERROR_PIPE`** → known Smart Tank firmware quirk. The patch in `02-musb-macos.patch` synthesizes a fake IEEE 1284 device id to work around it. If you see this AFTER applying patches, your model has a different quirk — open an issue.
- **`Error during device I/O`** → USB interface is being held by another process. Common culprits: VueScan trial running in background, HP Smart, an existing CUPS print job. Quit those and retry.
- **`No IPP over USB devices found`** (from `ipp-usb`) — that tool is unrelated to this repo; this repo specifically handles printers that *don't* speak IPP-over-USB.

## Caveats / known issues

- **Print queue conflict**: macOS's CUPS driver claims the USB printer interface for printing. Scanning works because `hpaio` claims a different interface (7/1/2) for control. If you hit `LIBUSB_ERROR_BUSY`, pause the print queue: `cupsdisable HP_<your_model>`, scan, then `cupsenable`.
- **No ADF**: the Smart Tank 500 has only a flatbed, so the wrapper assumes single-page. For ADF models, `scanimage --batch` works.
- **Resolution limits**: Smart Tank 500 supports 100/150/200/300/600/1200 DPI. Other LEDM models may differ.
- **Plugin firmware**: HPLIP's `hp-plugin` (which fetches a binary blob for some printer models) is not packaged here. Smart Tank 500 doesn't need it. If your model does, you'll need to install the plugin manually — the build script will tell you.
- **Not Notarized**: this is unsigned community code. macOS will let you run it because it's invoked from your shell, but Apple Silicon Macs won't load it as a system extension.

## License

GPL-2.0-or-later, matching HPLIP. The patches and the build script in this
repo are derivative of HPLIP source and inherit its license. See `LICENSE`.

HPLIP itself is Copyright © Hewlett-Packard Development Company, L.P. and is
distributed under GPL-2.0/MIT/BSD (mixed by file). This repo does NOT bundle
HPLIP source — `build.sh` downloads it directly from the official SourceForge
mirror at build time.

## Credits

- HP for releasing HPLIP under GPL.
- The OpenPrinting / SANE project for `sane-backends`.
- The `libusb` developers for cross-platform USB.
- Built by porting HPLIP 3.25.8 to macOS 26 in a single Claude Code session.
