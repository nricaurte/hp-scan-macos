# SOAPHT model compatibility

The native macOS plugin now reads `GetScannerElements` capabilities rather than
assigning M127fn capabilities to every HPLIP `scan-type=5` device. HPLIP's model
database still controls routing to SOAPHT; no other scan types are reclassified.

**Hardware baseline: HP LaserJet Pro MFP M127fn, USB.** Other models are experimental
candidates until their actual responses and scan results have been recorded.
A shared scan type is a protocol classification, not proof that a model works.
The [HP HPLIP supported-device list](https://developers.hp.com/hp-linux-imaging-and-printing/supported_devices/index)
describes upstream Linux support; it is not a macOS validation list.

## Capability negotiation

| Device response | macOS behavior |
|---|---|
| Platen / FlatbedSupported | Advertise Flatbed only when present and enabled |
| ADF / ADFSupported | Advertise simplex ADF only when present and enabled |
| Source minimum/maximum Width and Height | Set SANE bounds and SOAP scan-region limits in thousandths of an inch |
| Source OpticalResolution Width and Height | Offer the existing 150/300/600 DPI choices up to the lower optical axis limit |
| ColorSupported | Offer implemented GrayScale8 and RGB24 modes |
| FormatSupported | Require jfif; HPRAW-only responses return unsupported |
| ADFSupportsDuplex | Read capability, but do not advertise duplex yet |

For devices with both sources, the current HPLIP ABI exposes one color-mode list,
so the plugin advertises their supported intersection. If the ADF omits its color
list, it inherits the platen/device list, as observed in the M127fn response.
ADF-only devices must provide their own list or a device-wide list. Missing,
malformed or unsupported capabilities fail instead of inventing M127fn defaults.

M127fn retains Flatbed 150/300/600, ADF 150/300, its original Flatbed extents,
the approximately 296.9 mm ordinary ADF default and explicit 355.6 mm Legal height.
Other sources use their own size limits. The ordinary ADF default is at most
296.9 mm; explicit geometry can use the full advertised extent (up to the current
20-inch parser safety limit). ADF batches retain the initial job geometry.
Pre-start SANE raster estimates now use the requested geometry.

Optical maxima alone do not establish every intermediate resolution's firmware
support. This implementation conservatively uses the existing 150/300/600 choices
and never enables 1200 DPI merely because a scanner reports 1200 optical DPI.
SOAPHT request dialect differences, HPRAW, arbitrary resolutions, source-specific
mode switching and duplex acquisition need additional implementation/captures.
The existing HTTP transport requires chunked HTTP/1.1 and the existing DIME/JFIF
layout. USB is the validated transport; network discovery/scanning is not newly
claimed by this change. Camera/overhead models with another capability schema
may remain unsupported even when HPLIP marks them scan-type=5.

XML parsing uses macOS system libxml2 and local element names, accepting namespace
prefixes and whitespace. DTDs, entity declarations, external entities and network
access are disabled/rejected. The XML control-response limit is 256 KiB; the image
transport keeps its separate 16 MiB limit. Prefix-tolerant numeric and boolean
parsing is also used for job replies and PaperInADF.

## Candidate inventory

[SOAPHT_MODELS.md](SOAPHT_MODELS.md) contains 105 unique `scan-type=5` identifiers
from HPLIP 3.25.8, including M125/M126, M127/M128, M1522, M1536–M1539, M225/M226,
M425, CM1312 and CM2320 variants. Most are untested with this macOS plugin.
Generate the inventory from the exact HPLIP source used for a build:

```bash
python3 tools/list-soapht-models.py /tmp/hplip-build/hplip-3.25.8/data/models/models.dat
python3 tools/list-soapht-models.py /tmp/hplip-build/hplip-3.25.8/data/models/models.dat \
  --format markdown > docs/SOAPHT_MODELS.md
```

## Capture a new model

Connect one device and first identify its exact `hpaio:` URI using `scanimage -L`.
The read-only probe checks HPLIP's scan type and issues GetScannerElements without
creating a scan job or moving paper:

```bash
HPLIP_SRC=/tmp/hplip-build/hplip-3.25.8 \
  ./tools/probe-soapht.sh 'hpaio:/usb/YOUR_DEVICE?serial=YOUR_SERIAL' > capabilities.xml
```

Inspect captures for device identifiers before sharing. Keep raw captures separate
from synthetic test cases. The wrapper accepts `--device` when multiple scanners
are connected:

```bash
hp-scan --device 'hpaio:/usb/YOUR_DEVICE?serial=YOUR_SERIAL' \
  --source ADF --resolution 300 --mode Gray --page-size A4 --batch output.pdf
```

The wrapper queries source options before allowing ADF 600 or Flatbed Legal,
which were previously rejected for every model using M127fn limits. Unsupported
requests remain rejected on the M127fn. Ordinary scans retain their existing flow.
Full end-to-end validation requires images, dimensions/content, ADF termination,
PDF pages and recovery on that actual device; a successful capability probe is
not sufficient.

## Fixtures and regression

`tests/fixtures/m127fn-capabilities.xml` is a reduced real M127fn capture (only the
configuration, with device status/identifiers removed). The flatbed-only, ADF-only,
wide Gray/600 ADF and HPRAW-only fixtures are **synthetic schema tests**, not captures
or evidence that a named model works. Sanitizer tests exercise capability parsing,
source availability, dimensions, per-source DPI, namespace changes, unsupported
formats, exact boolean parsing and unchanged M127fn geometry/multi-page behavior.
Compiled-backend tests verify source changes update resolution lists/current DPI.


## Native macOS frontend

The [local AirScan adapter](AIRSCAN.md) uses each source's actual SANE options
for its eSCL capability advertisement. Alternate Flatbed-only, ADF-only, larger
extents and ADF-600 fixtures exercise the bridge, while the native SOAPHT plugin
still decides whether the real device is usable. No additional named HP model
is physically validated merely by adding the adapter.
