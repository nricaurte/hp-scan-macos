# WSD network scanning

The M127fn's Ethernet scan path differs from its USB SOAPHT path. This adapter
uses WS-Scan over HTTP and converts the returned uncompressed BMP/DIB to JPEG
for the existing local eSCL service. It does not require Rosetta, a proprietary
HP ICA driver, or rebuilding HPLIP with network support. USB mode remains the
default; select WSD explicitly with `AIRSCAN_WSD_URL`.

## Configure on this Mac

Use the scanner service URL returned by WS-Discovery metadata. The tested M127fn
advertised `http://<printer-ip>:3911/scanner`; do not assume that port/path for
other printers. Reserve the printer's IP address in the router so it stays stable.

```bash
mkdir -p build/wsd
(cd airscan-bridge && go build -trimpath -o ../build/wsd/airscan-bridge .)
AIRSCAN_WSD_URL='http://<printer-ip>:3911/scanner' \
AIRSCAN_NAME='HP LaserJet Pro MFP M127fn' \
OUT_DIR="$PWD/build/wsd" ./airscan-bridge/service.sh install
```

No sudo is required for this per-user service. In Image Capture, select
**HP LaserJet Pro MFP M127fn (WSD LAN)**. The service still listens only on
127.0.0.1 with LocalOnly Bonjour registration: this connects this Mac to the
network printer and does not expose a new scanner server to the LAN.
The probe helper is not needed for WSD mode. `AIRSCAN_NAME` is an optional display
name; without it, the device's reported scanner name is used.

The installer creates `~/Applications/HP AirScan Bridge.app` and associates the
LaunchAgent with its bundle identifier. Allow **HP AirScan Bridge** to access the
local network when macOS asks. If necessary, enable it under **System Settings →
Privacy & Security → Local Network**. The bridge keeps running and retries the
read-only capability request every 30 seconds while the printer is unreachable
or permission is unavailable; it advertises only after capabilities are loaded.
Stopping the service also stops this startup wait.

Terminal success does not establish LaunchAgent permission: macOS treats them
differently. An immediate `No route to host` from the agent, while the same URL
works in Terminal, can indicate this privacy restriction. Check permission and
the service log before changing router settings. See Apple's
[Local Network privacy guidance](https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy).
Local builds use ad-hoc signing; permission identity may change on rebuild.
Distributors can set `AIRSCAN_SIGN_IDENTITY` to an Apple-issued code-signing
identity during installation to support stable permission tracking across updates.
No privacy settings are modified by the installer.

The same LaunchAgent is used for both modes. To return to USB, reinstall the
service without `AIRSCAN_WSD_URL`, using a build directory containing both the
bridge and `hp-soapht-probe`. `AIRSCAN_DEVICE` and `AIRSCAN_WSD_URL` are mutually
exclusive. Existing USB backend libraries and settings are unchanged.

## Behavior and bounds

- Source bounds, color modes and symmetric DPI are read from WS-Scan capabilities.
  The adapter offers supported 75/150/300/600 DPI and Gray/RGB simplex sources.
  It requires the `dib` transfer format and rejects unsupported/compressed BMPs.
- One WSD JobId/JobToken covers an ADF batch. Only the explicit no-images/no-documents
  SOAP faults terminate an empty feeder normally; other faults remain errors.
  Completed pages stay available if a later page fails.
- eSCL geometry is converted to WS-Scan thousandths of an inch without exceeding
  bounds. Small firmware raster rounding differences are retained at their actual size;
  extra columns are cropped without stretching or filling missing pixels. Source bounds can differ from USB, including firmware-reported Flatbed
  height. Physical Legal bottom-edge coverage remains unverified.
- Other clients' active WSD jobs block a new scan. CreateScanJob is never replayed
  automatically after an ambiguous response. Recovery looks for the uniquely
  named job belonging to this bridge; it never cancels another client's job.
- Cancel stops the local HTTP wait, then attempts CancelJob with an independent
  cleanup deadline. The local acquisition lock is held through cleanup/status.
  Mechanical stopping can lag; a failed cancellation is not proof the device
  stopped. A subsequent job checks device state and active jobs again.
- HTTP requests have bounded connection/response/overall timeouts, no proxy and
  no redirects. Control XML, multipart image size, raster allocation, JPEG spool
  and batch length are bounded. No external XML entities/DTDs are accepted.
- Automatic WS-Discovery, TLS endpoints, duplex, compressed BMP, arbitrary
  resolutions, and HPRAW are not implemented. Configure a trusted printer URL.
  Paper-loaded status is unknown when the WSD status omits it; acquisition and
  the explicit end-of-feeder fault determine batch completion.

## M127fn evidence (2026-09-21)

The printer's web server returned eSCL capability/status XML but rejected a
POST to `/eSCL/ScanJobs` with HTTP 404. Therefore read-only eSCL responses are
not evidence that Image Capture can scan directly. WS-Discovery advertised a
ScannerService at port 3911, and WS-Scan GetScannerElements succeeded there.

Direct protocol tests completed a Color 150 Flatbed page and a two-page ADF
batch. Each page decoded as 1280 × 1650 BMP; the next ADF retrieval returned
`ClientErrorNoImagesAvailable`, CancelJob succeeded, and status returned Idle.
The first broad default-ticket request timed out; the minimal scan ticket then
worked. No acquisition request was automatically replayed while a job was active.

The local eSCL → WSD bridge also completed a Color 300 Letter Flatbed scan after
a cancellation during acquisition. CancelJob succeeded, status returned Idle,
and the next scan succeeded without restarting the bridge. The JPEG fully decoded
as 2528 × 3300: this firmware returned fewer columns than the nominal 2550,
which are preserved without filling in missing pixels. Its WSD Flatbed maximum
height is 11 inches; this does not change USB Flatbed geometry.

`go test -race ./...` and `go vet ./...` passed, including existing USB bridge
tests and new WSD capability, geometry, multipart/BMP, empty feeder, batch,
partial failure, busy, cancellation, ambiguous-create recovery and startup retry
tests. The capability fixture is from the M127fn; document images are not stored
in the repository. Other WSD models remain unverified.

The installed background service initially encountered macOS Local Network
denial. After app-bundle installation and the user's permission grant, the same
running LaunchAgent connected on its next startup retry, registered the WSD LAN
Bonjour service, and reported Idle. An installed-service Color 300 Letter
Flatbed scan then fully decoded as 2528 × 3300 JPEG, ended normally, and left no
retained jobs. The listener was verified to bind only to 127.0.0.1:8089.
Image Capture then discovered **HP LaserJet Pro MFP M127fn (WSD LAN)**, completed
its automatic Flatbed overview, and saved a Gray 300 Letter JPEG. The result
appeared in its Scan Results window and the Scan button became enabled again.
The saved image fully decoded as 2528 × 3300.

After reloading two sheets, Image Capture completed a WSD ADF Color 300 Letter
batch with **Combine into single document** enabled. Both pages were saved in one
PDF; both 612 × 792 pt pages rendered successfully at 300 DPI (2550 × 3300), and
visual inspection confirmed two distinct, readable pages. This is the PDF page
render size, not a measurement of the device's original raster width. The feeder
ended automatically after the second page, the Scan button became enabled again,
and the bridge reported Idle with no retained jobs. No app/service restart or
manual cancellation was needed. Preview WSD acquisition, physical jam recovery
over WSD, and native-app WSD cancel/restart remain unverified.

Protocol references:
[Microsoft WS-Scan CreateScanJob](https://learn.microsoft.com/en-us/windows-hardware/drivers/image/createscanjobrequest),
[GetActiveJobs](https://learn.microsoft.com/en-us/windows-hardware/drivers/image/getactivejobsrequest),
and the [sane-airscan project](https://github.com/alexpevzner/sane-airscan), whose
hardware list records M127fn as WSD-capable without eSCL scan support.
