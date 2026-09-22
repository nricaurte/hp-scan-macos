# hp-scan-macos-scan-type-5

A macOS / Apple Silicon port that extends the HPLIP SANE backend with support for **HP `scan-type=5` SOAPHT scanners**, currently focused on the **HP LaserJet Pro MFP M127fn** over USB.

This repository is based on [`nricaurte/hp-scan-macos`](https://github.com/nricaurte/hp-scan-macos). The upstream project primarily targets LEDM (`scan-type=7`) devices; this fork adds a clean-room native macOS SOAPHT compatibility plugin.

## Background

This project began with my attempt to restore scanning on my **HP LaserJet Pro MFP M127fn** under macOS Tahoe on Apple Silicon.

I first tried the approach described in [HPScanner4MacOS](https://github.com/herb2k/HPScanner4MacOS): work around the installation restrictions of HP 5.1.1 Printer Software Update, add the Image Capture Support Apps, and try different printer drivers until the scanner becomes available in Image Capture. I could not get this working in my environment.

I then found [hp-printer-fix-macos](https://github.com/pavelbinar/hp-printer-fix-macos), which explains how to modify `HewlettPackardPrinterDrivers.pkg`, and [HewlettPackardPrinterDrivers-MacOS](https://github.com/gabrielllzs/HewlettPackardPrinterDrivers-MacOS), which provides a pre-patched package. These projects remove the macOS version restriction in the installer's `Distribution` file; they do not modify or re-sign the driver binaries. However, the legacy HP scanner-app approach requires Rosetta 2 on Apple Silicon, so I chose not to pursue it further.

With ChatGPT's help, I discovered the HPLIP/SANE approach in [nricaurte/hp-scan-macos](https://github.com/nricaurte/hp-scan-macos). At the time, that project targeted LEDM (`scan-type=7`) devices, while my M127fn used SOAPHT (`scan-type=5`) and needed additional support.

Using that project as a starting point, I ran repeated scanning and communication experiments on my own M127fn and developed a **native macOS arm64 compatibility plugin** through a clean-room reimplementation of SOAPHT behavior. This repository records that implementation and its validation, with the goal of providing a scanning path that does not depend on Rosetta 2.

## Verified

| Component | Status |
|---|---|
| macOS 26 Tahoe | ✅ |
| Apple Silicon / arm64 | ✅ |
| HPLIP 3.25.8 | ✅ |
| Homebrew SANE 1.4.x | ✅ |
| HP LaserJet Pro MFP M127fn via USB | ✅ |
| HPLIP `scan-type=5` | ✅ |
| Flatbed Gray 150/300/600 dpi | ✅ |
| Flatbed Color 300/600 dpi | ✅ |
| ADF simplex Gray 150/300, Color 300 | ✅ |
| Multi-page ADF, PaperInADF debounce | ✅ |
| Dynamic geometry / crop / offset | ✅ |
| Page-size presets / multi-page PDF wrapper | ✅ |

## Protocol flow

```text
GetScannerElements
        ↓
CreateScanJob
        ↓
RetrieveImage
        ↓
DIME records
        ↓
JPEG image
        ↓
CancelJob
```

SOAPHT uses the HPMUD channel:

```text
HP-SOAP-SCAN
```

## Install

```bash
git clone https://github.com/kimdongup/hp-scan-macos-scan-type-5.git
cd hp-scan-macos-scan-type-5
./build.sh
```

## Example scan

```bash
scanimage   -d 'hpaio:/usb/HP_LaserJet_Pro_MFP_M127fn?serial=YOUR_SERIAL'   --resolution 300   --mode Color   --source Flatbed   --format=jpeg   > ~/Desktop/m127-color-300.jpg
```

## Native SOAPHT plugin

```text
soapht-macos/bb_soapht_macos.c
→ $WORK/hplip-3.25.8/.libs/bb_soapht.so (build.sh)
→ /opt/homebrew/share/hplip/scan/plugins/bb_soapht.so
```

## Documentation

- [Architecture](docs/ARCHITECTURE.md)
- [Build & Test](docs/BUILD_AND_TEST.md)
- [Research History](docs/RESEARCH_HISTORY.md)
- [Troubleshooting](docs/TROUBLESHOOTING.md)
- [Roadmap](docs/ROADMAP.md)
- [Development Prompt](prompt.en.md)

## Upstream

```text
origin   → kimdongup/hp-scan-macos-scan-type-5
upstream → nricaurte/hp-scan-macos
```

## Status

Flatbed and simplex ADF scanning, multi-page batches, paper detection/debounce,
dynamic geometry and the PDF wrapper are implemented on the M127fn.

v0.4 adds controlled logging, error cleanup, total I/O deadlines, busy/cancel
status propagation, recovery tests and `INSTALL=0` clean builds. See the
[v0.4 validation record](docs/V0.4_VALIDATION.md) for measured results and limits.
macOS cancellation now sets a signal-safe flag and defers cleanup; one-second
response polling preserves partial reads and reports USB disconnection as I/O error.
An explicit MediaJam status maps to JAMMED, with one device-status check after a
malformed completed ADF image response.
Cancellation drains an already-started response under its original deadline, so it
can wait for the current physical acquisition before returning CANCELLED.
The ADF Legal-height fallback has been removed: explicit Legal requests retain
355.6 mm, while selecting ADF still defaults to approximately 296.9 mm. Flatbed
behavior is unchanged. SOAP ticket and compiled-backend option tests cover the
fix; physical full-length Legal paper validation is still pending.

```bash
hp-scan --source ADF --resolution 300 --mode Color \
  --page-size A4 --batch ~/Desktop/document.pdf
WORK="$(mktemp -d /tmp/hplip-v04.XXXXXX)" INSTALL=0 ./build.sh
HPLIP_SRC=/tmp/hplip-build/hplip-3.25.8 bash tests/run.sh
```

Use `SANE_DEBUG_HPAIO=8` for backend traces. The build/test guide explains how to
load a candidate backend and plugin without replacing the installed driver.


## Additional scan-type=5 models

The plugin now negotiates source presence, geometry, optical limits, color and
JFIF support from GetScannerElements rather than hardcoding M127fn capabilities.
Namespace-prefixed replies, flatbed-only and ADF-only configurations are supported
by software tests. M127fn remains the physical baseline; the 105 HPLIP 3.25.8
scan-type=5 identifiers are candidates, not 105 validated macOS models.
See [compatibility and capture instructions](docs/COMPATIBILITY.md) and the
[model inventory](docs/SOAPHT_MODELS.md). Use `hp-scan --device 'hpaio:...'` to
select a device explicitly.

## Image Capture / Preview

An optional WSD network path is available through `AIRSCAN_WSD_URL`, alongside
the existing USB mode. On the M127fn, Image Capture completed Flatbed scanning
and a Color 300 DPI two-page ADF PDF with automatic batch termination.
See [WSD network scanning](docs/WSD_NETWORK.md) for setup
and the exact physical/native-app validation status.

The local eSCL bridge negotiates the installed scanner's source, DPI and geometry
options and keeps one SANE process per multi-page job. Its listener and Bonjour
registration are local to this Mac. Build with `./airscan-bridge/build.sh`, then
start the per-user service with `./airscan-bridge/service.sh install`.
M127fn tests passed for Image Capture Flatbed and two-page ADF JPEG/PDF, and
Preview Flatbed with cancel/restart. Intermittent ADF device errors remain;
cancellation may wait for the current device response to finish draining.
See [setup and limits](docs/AIRSCAN.md) and [native validation](docs/AIRSCAN_VALIDATION.md).
Additional HP models remain experimental until physically tested.
