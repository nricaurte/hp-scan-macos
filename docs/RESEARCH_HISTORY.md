# Research History

## Problem

The M127fn is HPLIP `scan-type=5`. Linux uses a SOAPHT binary plugin that cannot be loaded natively on macOS.

## Linux plugin findings

Exports eight `bb_*` scanner functions and depends primarily on the HPMUD channel API.

## Traffic tracing

Observed:

```text
HP-SOAP-SCAN
GetScannerElements
CreateScanJob
RetrieveImage
CancelJob
```

Capabilities include JFIF/HPRAW, grayscale/RGB modes, Flatbed and ADF, with platen optical resolution up to 1200 dpi and ADF up to 300 dpi.

## RetrieveImage

```text
HTTP 200
Content-Type: application/dime
Transfer-Encoding: chunked
```

After HTTP dechunking, the payload is a DIME stream. JPEG data is carried in `image/jfif` records and may span continuation records.

## Critical parser fix

Naive JPEG SOI→EOI extraction included DIME continuation headers and corrupted the image. The corrected parser appends only DIME data fields for `image/jfif` records.

## Build fixes

- HPLIP config compiled for `/opt/homebrew/etc/hp/hplip.conf`
- `libhpipp.la` removed from scanner-only dependencies because macOS `ar` rejects the empty archive

## Verified result

```text
Gray 150 / 300 / 600
Color 300 / 600
```


## ADF and geometry (v0.3–v0.4)

M127fn scan-type=5 SOAPHT now supports Flatbed, simplex ADF, multi-page batches,
PaperInADF debounce, crop/offset, A4/Letter/Legal presets and wrapper PDF batches.
Physical tests established Gray 150/300 ADF and Color 300 four-page A4 PDF output,
including normal last-page termination. A one-page batch can eject additional
loaded sheets when its job is cancelled; use one sheet for single-page checks.

The earlier plugin rewrote a 14-inch ADF request to 11.689 inches. This conflated
the default geometry with an explicit Legal request. The backend now chooses the
ordinary ADF default during source initialization, while the plugin preserves
explicit Legal at 14000 thousandths of an inch (355.6 mm). Flatbed extents and
nonzero-offset AUTO behavior are unchanged. Mock SOAP tickets and compiled SANE
options cover default/A4/Letter/Legal/custom geometry and retained ADF job geometry.
No Legal original is available for the final physical test, so bottom-edge capture
remains unverified.

## Robustness work (2026-09-20)

Normal backend diagnostics use SANE debug levels; native plugin diagnostics use
`SANE_DEBUG_HPAIO=6` or higher. Failure diagnostics remain available. The plugin validates HTTP
chunks, DIME records and numeric fields, bounds allocations and uses monotonic
transaction deadlines: 45 seconds normally, 300 seconds for RetrieveImage and
5 seconds for CancelJob. Failed cancellation retains the known JobId and retries
cleanup before creating a new job. A lost CreateScanJob response without a JobId
remains a device-side recovery limitation.

A real M127fn HTTP 500 fault (`The service is temporarily blocked and can't accept
new scan job requests.`) was observed immediately after a completed job. It now
maps to DEVICE_BUSY. A 12-second frontend settling interval allowed subsequent
jobs; the driver does not blindly replay requests that might advance the feeder.

The [SANE cancellation contract](https://sane-project.gitlab.io/standard/master/api.html#sane-cancel)
allows asynchronous cancellation from a signal handler. The macOS backend now
sets only a `volatile sig_atomic_t` flag in its cancel callback and defers USB,
image and heap cleanup. One-second response polling does not restart the SOAP request. The patched HPMUD/libusb adapter preserves partial
timeout data and reports disconnect/hard failures as I/O errors. Open/close calls
and in-progress writes still limit cancellation latency.

Sanitizer tests cover malformed input, allocation errors, busy/timeout/cancel,
failed CancelJob recovery and a mid-page failure followed by a new scan. Tests
also exercise the actual compiled backend using a signal, the patched USB read
adapter with mocked libusb, and wrapper geometry arguments. Full physical fault
coverage is recorded separately in [the validation record](V0.4_VALIDATION.md).

The backend and header changes are persisted in patch 04; USB adapter changes
are in patch 02. A fresh `/tmp/hplip-build` was recreated after moving the old
build aside. All patches and the complete arm64 library/plugin build succeeded.
The installer now checks sudo authentication before modifying any runtime files.
A stable release tag requires the final installed regression results and a commit
containing the tested changes; the working tree alone is not a release target.


## Capability-driven scan-type=5 extension

Inspection found that the plugin required FlatbedSupported=true and assigned
M127fn source lists, geometry and resolutions to every device. HPLIP 3.25.8's
models.dat contains 105 unique scan-type=5 identifiers, including configurations
that cannot safely inherit those assumptions. The native plugin now parses
GetScannerElements with system libxml2, uses per-source geometry and optical
limits, and requires implemented JFIF/color capabilities. Namespace prefixes and
whitespace no longer prevent job metadata or PaperInADF parsing. The backend
refreshes resolution lists/defaults when changing sources and advertises JPEG only.

The M127fn fixture comes from the actual earlier capability capture. Alternate
fixtures are synthetic, and the candidate catalog is derived from the upstream
model database rather than hardware results. A read-only capture tool was added
for validating future devices. The wrapper gained explicit device selection and
checks source options before permitting ADF 600 or Flatbed Legal on other models.
Physical M127fn results and remaining validation limits are recorded in
[V0.4_VALIDATION.md](V0.4_VALIDATION.md); broader-model support remains experimental.


An asynchronous Color 600 cancellation test exposed a further boundary problem:
CANCELLED was returned, but immediate restart failed with I/O error and a subsequent
GetScannerElements returned SOAP Error 32. The device eventually settled without
a power cycle. The plugin now completes an already-started request and drains its
response before returning cancellation, using the original deadline. It also keeps
a cancelled CreateScanJob reply long enough to cancel its known JobId. This trades
immediate abort latency for preserving the response boundary; cancellation can
wait for the current physical acquisition. Fragmented-response and cancelled-job
creation regressions cover this behavior.


### Explicit ADF jam status

An installed Gray 150 two-sheet test saved the first page and failed during the
second. The device displayed an error, and a read-only capture confirmed
Stopped / MediaJam / PaperInADF=true. This justified mapping the exact status to
SANE JAMMED, including one status check after a malformed completed ADF image
response. Unknown errors still return I/O error. Added sanitizer/compiled-backend
coverage verifies precedence over empty, cleanup and restart; see
[V0.4_VALIDATION.md](V0.4_VALIDATION.md) for hardware and installation status.
The earlier four-sheet Color termination failure has no corresponding jam capture
and must not be assigned the same cause.


The fresh jam-recovery build subsequently completed a Color 300/A4 batch of
four pages in 60.4 seconds, with PaperInADF=false/NO_DOCS and a validated four-page
PDF. The earlier intermittent termination error did not recur; its original
cause remains unknown. This is a passing retest, not proof that the jam-status
change fixed that separate observation.


### Repeated build discarded macOS Makefile edits

The attempted jam-recovery installation stopped before installation: make
invoked missing Automake 1.11, regenerated Makefile through config.status,
restored `/etc/hp` and attempted an empty libhpipp archive. The build script now
uses make's old-file options for its already generated release inputs and cleans
objects before compiling so a failed or differently configured tree cannot leave
mixed compiler settings. An opt-in integration regression builds twice in the
same WORK. The repaired formerly failing tree produced the same four binary
hashes as the hardware-tested jam-recovery clean candidate; installed files
remained the earlier release candidate.


After the corrected installer completed, all installed library/plugin/wrapper
hashes matched and an override-free runtime trace confirmed Homebrew paths.
The latest installed seven-case Flatbed matrix and two-page Gray 150 ADF batch
passed; all JPEGs fully decoded, normal Flatbed logs stayed empty and the ADF
ended with NO_DOCS. The earlier intermittent Color termination failure remains
unexplained despite a successful four-page retest.


### Local native-app eSCL bridge

The earlier Flatbed-only bridge was replaced with source-specific capability
negotiation, bounded JPEG batch spooling, native cancellation, device status
probing, and Bonjour LocalOnly proxy registration. ImageCaptureCore discovered
the scanner and the user confirmed Image Capture Flatbed scanning. A subsequent
bottom-edge crop failed because decimal offset/height conversions exceeded the
SANE fixed-point bound by one unit. Quantizing each component downward before
passing it to scanimage fixed the reproduced case; all 3507 top-offset cases and
eight actual backend option checks passed, followed by two user-confirmed
consecutive native scans. Flatbed native backend code was unchanged.

The first native ADF Color/A4 attempt then encountered a user-reported jam.
The acquisition exited with I/O error and Apple's DELETE removed the job, but
the device still reported Stopped with numeric reason 11 after cancellation.
The bridge's empty job list and absent worker distinguish this from a retained
acquisition. Recovery validation remains open; code 11 is not assumed to mean
MediaJam. See [AIRSCAN_VALIDATION.md](AIRSCAN_VALIDATION.md) for current evidence.


A later native ADF diagnostic showed CreateScanJob receiving ScanElements
instead of a JobId. A read-only drain, with no new request, recovered a queued
status response. The bridge now drains bounded pending input after unsuccessful
workers, before releasing its acquisition lock, then queries fresh device status.
Native plugin code is unchanged. JobInfo page counters were also corrected to
the PWG namespace and actual pending-page count; these protocol corrections are
covered by namespace-aware tests. A retry after the manual drain reached the
image retrieval stage but returned an empty JFIF record; the user confirmed no
original was loaded, so this did not validate successful native ADF delivery.


### Native ADF completion and Preview cancellation

The error-response cause was subsequently captured as HTTP 400 during a physical
ADF jam. Returning at the header left data ahead of CancelJob; fragmented error
body regression now verifies full consumption for 400/409/503. A candidate native
plugin completed Image Capture A4 two-JPEG and Letter two-page-PDF batches. The
app returned to its enabled Scan button. Preview also acquired a Flatbed page and
saved it locally. Its automatically detected small region exposed a missing SOAP
minimum-size constraint in the adapter; source-specific minimums now drive both
advertisement and validation, without reducing native Flatbed constraints.

A physical Preview Color 600 cancel initially waited for the held image response.
Bounding each pending NextDocument wait to two seconds allowed Apple to send
DELETE before acquisition ended. The native worker completed its response drain,
ended Canceled, and the same Preview session then acquired and saved a new page.
See AIRSCAN_VALIDATION.md for the current installation boundary and open limits.

The final bounded-wait ADF check produced a two-page Letter PDF in Image Capture
and returned to an enabled Scan button. The preceding attempt had returned
RetrieveImage HTTP 400 / ServerErrorTemporaryError with zero pages; error-body
draining and CancelJob completed, and the next job succeeded after the user
reported two sheets loaded, without restarting the app or service. The device
error's cause remains unknown and is retained as a limitation.

After user-authenticated installation, all installed backend, plugin, wrapper,
bridge and probe hashes matched the integrated error-drain-clean build. The
per-user service was switched from its diagnostic launcher to the standard
scanimage with debug disabled. A read-only runtime trace confirmed the installed
libraries/plugin, and the restarted local-only service advertised the expected
Flatbed/ADF geometry and reported Idle with no retained jobs. No additional
physical acquisition was performed after this promotion.

### Ethernet WS-Scan transport (2026-09-21)

After switching the M127fn from USB to Ethernet, print discovery remained usable
but did not establish scan support. The web server returned eSCL capability and
status XML, yet rejected POST /eSCL/ScanJobs with HTTP 404. WS-Discovery metadata
instead exposed an operational WS-Scan ScannerService at port 3911.

A minimal WSD scan ticket produced a Color 150 Flatbed page and two ADF pages;
the next retrieval returned ClientErrorNoImagesAvailable and cleanup returned
the device to Idle. The Go bridge now has an explicit, optional WSD transport
with capability negotiation, BMP/DIB conversion, batch spooling and bounded
cancellation. A physical bridge cancellation followed by a new Color 300 Letter
Flatbed scan succeeded without restarting the process. Firmware returned a
2528 × 3300 raster for a nominal 2550-column request; actual pixels are retained.

The same executable initially failed from launchd with a kernel NECP denial,
despite connecting successfully when run interactively. Following Apple's Local
Network guidance, service installation now supplies a responsible app bundle,
usage description and AssociatedBundleIdentifiers. Startup remains alive while
retrying read-only capability queries so macOS can present its permission alert.
The user then granted permission. Without restarting, the LaunchAgent connected,
advertised the WSD scanner, and completed a Color 300 Letter Flatbed scan via
the local eSCL endpoint. The JPEG fully decoded and status returned Idle with no
retained jobs. Image Capture subsequently discovered the WSD service, completed
an overview and saved a Gray 300 Letter JPEG (2528 × 3300, fully decoded). Its
Scan button became enabled again after completion. A subsequent Image Capture
ADF Color 300 Letter batch saved two distinct readable pages into one PDF and
automatically ended after the second page. Both PDF pages rendered successfully;
the app re-enabled Scan and the service returned Idle with no jobs. Preview,
physical jam recovery and native-client cancellation over WSD remain open. See
[WSD_NETWORK.md](WSD_NETWORK.md) for evidence and current limitations.
