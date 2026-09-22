# Development Prompt History — hp-scan-macos-scan-type-5

This document records the development path rather than duplicating source code.

## Goal

Enable USB scanning from an HP LaserJet Pro MFP M127fn on macOS 26 Tahoe / Apple Silicon using HPLIP/SANE.

## Discovery

```text
HP LaserJet Pro MFP M127fn
USB VID:PID 03f0:322a
HPLIP scan-type=5
```

Linux plugin analysis showed eight `bb_*` entry points and four core HPMUD channel dependencies.

## Protocol

```text
HP-SOAP-SCAN
GetScannerElements
CreateScanJob
RetrieveImage
CancelJob
```

`RetrieveImage` returns chunked HTTP containing DIME records; correct reconstruction requires concatenating only the `image/jfif` data sections across continuation records.

## Native macOS implementation

```text
soapht-macos/bb_soapht_macos.c
soapht-macos/build-plugin.sh
```

The plugin is built as a native arm64 Mach-O dylib and loaded by the patched SOAPHT backend.

## Verified

```text
Gray: 150 / 300 / 600 dpi
Color: 300 / 600 dpi
```

## Next

```text
ADF
geometry/crop
regression tests
native macOS scan integration
```
