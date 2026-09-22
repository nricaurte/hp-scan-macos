# AirScan integration validation

Environment: macOS Tahoe 26.6.2 arm64, HPLIP 3.25.8 / SANE 1.4.0, M127fn USB.
Only M127fn has been physically verified. Other scan-type=5 profiles remain
experimental; no physical Legal sheet was available.

## Current native-app results

- Bonjour LocalOnly advertisement and 127.0.0.1-only listener: passed.
- ImageCaptureCore discovery and Image Capture scanner list: passed.
- Image Capture repeated Flatbed scans: user confirmed two consecutive successes;
  logs also record Gray/RGB 300 DPI completion.
- Image Capture ADF Color 300/A4: two JPEGs, each fully decoded at 2480 × 3507.
  Normal final NextDocument 404, DELETE and return to an enabled Scan button.
- Image Capture ADF Color 300/Letter: two-page PDF, each page 612 × 792 points;
  both the app's result list and PDFKit report two pages.
- Preview File → Import from Scanner: scanner found. Close Image Capture's
  connection before opening Preview; otherwise macOS reports another app is
  using the scanner.
- Preview Flatbed Color 150/A4: document opened in Preview and saved locally as
  `~/Pictures/soapht-preview-flatbed.jpg`, fully decoded at 1240 × 1753.
- Preview Color 600 cancellation: with bounded NextDocument waits, Apple's
  DELETE arrived before acquisition ended; UI controls became available again.
  The native worker drained the current acquisition and ended Canceled with
  zero pages; helper recovery found zero pending bytes. Device returned Idle.
- Same Preview session after cancellation: Flatbed Color 150 completed and
  opened a new document, saved as `~/Pictures/soapht-preview-after-cancel.jpg`.
- Final ADF regression after the bounded-wait change: Color 300/Letter completed
  two pages in Image Capture, including temporary 503 retries between images,
  final 404 and DELETE. `~/Pictures/soapht-adf-final.pdf` contains two pages at
  612 × 792 points; the result window reports two pages and Scan is enabled.

Evidence: service log at `~/Library/Logs/HP SOAPHT AirScan/bridge.log`, output
validation in `build/airscan/native-output-validation.log`. Scan files are local
user artifacts, not repository fixtures.

## Software/build checks

- Go tests with the race detector and `go vet`: passed.
- Fixtures exercise source-specific sizes/defaults/DPI, alternate model profiles,
  XML escaping, minimum-size and offset validation, and PWG page-counter fields.
- HTTP/process tests cover batch delivery, empty feeder, partial failure, busy,
  cancel/restart, concurrent page requests, deletion cleanup, recovery exclusion
  and temporary 503 only while an image is still being acquired.
- Native sanitizer tests cover full consumption of fragmented HTTP 400/409/503
  bodies, unchanged error/busy mapping, and bounded read-only recovery.
- USB transport and wrapper regressions: passed.
- Integrated fresh build and repeated build including the bridge/helper: passed.

## Diagnosed failures and fixes

1. A Flatbed crop touching the bottom edge could exceed SANE's fixed-point bound
   by one unit. Downward 16.16-mm quantization fixes this without changing the
   native Flatbed path. All 3507 top offsets and eight real-backend option checks
   passed, followed by consecutive native scans.
2. JobInfo page counters used the wrong XML namespace and copied the requested
   batch limit. They now use PWG fields and actual completed/pending page counts.
3. ADF acquisition returned HTTP 400 during a user-confirmed jam. The native
   reader stopped at the error header, leaving its body ahead of CancelJob and
   later status replies. It now drains error bodies before returning their
   status. A separate bounded post-worker drain protects recovery without
   replaying a scan request. One observed recovery drained 634 bytes.
4. A later attempt returned an empty JFIF record while no original was loaded;
   this was not counted as successful acquisition. Physical jams require printer
   acknowledgement; numeric reason 11 remains unmapped.
5. Preview auto-selected a region smaller than the device's minimum. The bridge
   now reads source-specific SOAP minimums instead of advertising a one-unit
   minimum. M127's 1920/1000-inch minimum is advertised conservatively as 577
   eSCL units, satisfying the backend's strict minimum-height comparison.
6. An indefinitely held NextDocument delayed the Apple client's cancel request
   until a Color 600 acquisition completed. Pending requests now return temporary
   503 after two seconds, with Retry-After; completed failures return 500 and
   normal exhaustion/empty feeder returns 404. Native cancel/restart passed.
7. Immediately before the final successful ADF batch, RetrieveImage returned
   HTTP 400 / `ServerErrorTemporaryError` with zero pages. The error body was
   consumed, CancelJob received HTTP 202, and the helper found no queued bytes.
   Image Capture received a terminal error and deleted the job. After the user
   reported two sheets loaded, the next job completed without restarting the
   service or app. The device-side cause of that temporary error is unresolved;
   this sequence verifies recovery, not elimination of acquisition errors.

## Installation boundary and remaining limits

The hardware tests above used the candidate native plugin through an explicit
scanimage launcher (`build/airscan/scanimage-candidate`); loaded plugin path was
verified with lsof. After the user completed the normal installer, system-wide
promotion was verified on 2026-09-20: HPMUD, HPIP, HPAIO, the SOAPHT plugin,
hp-scan, bridge and probe hashes all match the integrated build in
`build/airscan/error-drain-clean`. The per-user service was reinstalled from
that build with no candidate launcher or debug override.

An override-free, read-only scanimage option query loaded the installed plugin
and libraries. HPAIO's Cellar path resolves to the same installed artifact.
The restarted service advertises Flatbed/ADF minimums of 577 × 577 eSCL units
and maximum heights of 3507/4200, listens only on 127.0.0.1:8089, and reports
Idle with no retained jobs. Bonjour LocalOnly registration succeeded.
Evidence: `build/airscan/installed-promotion-validation.log`,
`installed-runtime-libraries.log` and `installed-capabilities.xml` in the same
directory. Physical acquisitions above preceded promotion; post-install checks
were read-only and did not consume more originals.

The earlier native driver's intermittent Color end-of-batch failure is not
claimed universally resolved. Physical disconnect/timeout recovery and another
HP model still need validation. Cancellation can wait for the native response to
finish draining; the bridge does not promise immediate mechanical stopping.
