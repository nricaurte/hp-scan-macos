# Troubleshooting

## No scanner in `scanimage -L`

```bash
grep '^hpaio$' /opt/homebrew/etc/sane.d/dll.conf
ioreg -p IOUSB -l -w 0 | grep -i -A20 'M127'
```

## Wrong HPLIP config path

```bash
strings /opt/homebrew/lib/libhpmud.0.dylib | grep hplip.conf
```

Correct:

```text
/opt/homebrew/etc/hp/hplip.conf
```

## `ar: no archive members specified`

Cause: empty `libhpipp.la` with network build disabled. Remove it from generated HPAIO dependencies before make.

## `clang: no such file or directory: bb_soapht_macos.c`

`build-plugin.sh` must compile `$SCRIPT_DIR/bb_soapht_macos.c`, not a path relative to the caller's current directory.

## Permission denied installing plugin

Run `./build.sh` from an interactive terminal and enter the administrator password
there. Its preflight checks authentication before changing runtime files.
Do not install the tracked prebuilt `soapht-macos/bb_soapht.so` after building v0.4;
`build.sh` produces its current plugin under the HPLIP work directory. Keep the
backend and plugin from the same build together. See [Build & Test](BUILD_AND_TEST.md).

## JPEG corruption

Do not copy bytes between JPEG SOI/EOI across the whole HTTP body. Parse DIME and concatenate only `image/jfif` record data.

## v0.4 debug logging

Normal SOAPHT scans no longer print every image-processing iteration to stderr.
Enable diagnostics for one command:

```bash
SANE_DEBUG_HPAIO=8 scanimage -d 'hpaio:/usb/HP_LaserJet_Pro_MFP_M127fn?serial=YOUR_SERIAL' \
  --source Flatbed --resolution 300 --mode Gray --format=jpeg \
  -o /tmp/debug-scan.jpg 2> /tmp/soapht-debug.log
```

Level 6 also enables native plugin transaction/fault diagnostics. Debug logs can
include the device URI and SOAP fault text; ordinary scans remain quiet.

## Scanner busy

The native plugin maps HPMUD busy, HTTP 409/503 and recognized SOAP busy faults
to `SANE_STATUS_DEVICE_BUSY`. Close other scanner clients and retry when the
device is idle. Unknown SOAP faults remain I/O errors. The driver does not
repeat CreateScanJob or RetrieveImage automatically: repeating either request
could duplicate a job or consume another ADF page.

## Timeout or cancellation

The request and response share one monotonic deadline: normally 45 seconds,
300 seconds for image retrieval, 5 seconds for cancellation. A stream of tiny
successful reads cannot keep resetting that deadline. Response reads poll every
one second, retaining partial data on timeout. A disconnected USB device returns
an I/O error. Blocking HPMUD open/close and integer timeout rounding still limit
the guarantee.

The macOS cancel callback only sets a signal-safe flag; read, next start or close
performs cleanup outside the signal handler. A request already sent is completed
and its response drained before cancellation is returned. Otherwise the M127fn
can return SOAP Error 32 on the next request because image bytes are still pending.
Cancellation can wait for the physical acquisition under its original deadline;
one-second read polling is not a one-second cancellation guarantee. HPMUD
open/close are still outside the deadline. If a hard transport failure prevents
draining, close/reopen and allow the device to settle; power cycling may be needed.

After an error, local image data is freed and a known job is cancelled. If
CancelJob fails, the session keeps its JobId and retries cleanup before a new
scan. Close also makes a best-effort cancel. If the device accepted a job but
its response was lost before the JobId arrived, the client cannot cancel that
unknown job. Let the scanner settle, close/reopen the client and retry; a
persistent device-side job may require a device restart.

ADF page EOF is different from ending the batch: it frees the page buffer and
retains the job/geometry for the next page. Empty ADF should report no documents,
not cause an extra RetrieveImage request.

## Clean build refuses an old patch

v0.4 no longer silently skips a patch that does not apply. Choose a fresh work
directory (keep the old build for comparison):

```bash
WORK="$(mktemp -d /tmp/hplip-v04.XXXXXX)" INSTALL=0 ./build.sh
```

See [Build & Test](BUILD_AND_TEST.md) for isolated runtime testing and plugin
installation. Merely rebuilding does not update the installed plugin.

## Legal preset produces an A4-length image

The old plugin converted a 14-inch request to 11.689 inches. The Legal follow-up
removes that conversion and moves the ordinary ADF default into backend option
initialization. Rebuild/install both the updated HPAIO backend and native plugin;
an older plugin can still shorten Legal requests, while an older backend with
the new plugin can default to the full Legal height. A matching pair preserves
the usual ADF default and honors explicit `--page-size Legal` or `-y 355.6`.

The software regression covers full-height SOAP requests, but physical Legal
paper with bottom-edge content has not yet been tested. See the validation record.

## ADF reports no documents after a short batch

On this M127fn, terminating a batch after one page with four sheets loaded
ejected the remaining sheets. Reload the feeder for the next job; for a
one-page test, load only one sheet. If the device still returns
`PaperInADF=false`, reseat the original and verify the feeder's paper detection
before retrying. The driver must not bypass the sensor and request another
image from an empty feeder.


## A batch fails after some successful pages

The wrapper reports the original nonzero scanimage status and prints a retained
scan directory. Previously its EXIT trap removed even successfully acquired
pages. It now leaves those files for inspection/recovery; the final file may be
incomplete. No partial PDF is silently published. Inspect the device/paper state
and diagnostic log before retrying; a timeout must not be treated as NO_DOCS
without a successful sensor response confirming an empty feeder.


During installed M127fn validation, one four-page Color A4 batch ended with I/O
error after all pages had been ejected. A subsequent sensor probe reported Idle
and an empty feeder; a two-page diagnostic retry completed normally. The cause
of that intermittent observation is not established. Capture `SANE_DEBUG_HPAIO=6`
output and preserve the batch files when reporting a recurrence; do not convert
a generic I/O failure into an empty-feeder success.


## ADF reports a paper jam

A captured M127fn response used `ScannerState=Stopped` and
`ScannerStateReason=MediaJam` while `PaperInADF=true`. The updated driver
propagates this exact reason as SANE JAMMED. A malformed completed ADF image
response also triggers one status query so a confirmed jam can be distinguished
from an unknown I/O failure. No page is automatically retried.

Follow the device instructions to clear the jam, then reload the originals and
start a new scan. Inspect any preserved batch files before deciding which sheets
to rescan: the last file may be missing or incomplete. Debug level 6 includes DIME
record/boundary diagnostics and sensor values; normal scans remain quiet.


## Rebuilding fails with `automake-1.11` and an empty `libhpipp.a`

A reused HPLIP tree previously let make regenerate its Makefile after the macOS
patching step. The resulting compile lines reverted to `/etc/hp`, and macOS ar
failed with `no archive members specified`. This is a build-stage failure; the
installer has not changed the installed runtime at that point.

The corrected build script keeps the explicitly configured release Makefile
and cleans stale objects before compiling. Re-run `./build.sh` with the same
`WORK` after updating the script. `tests/build_reentry.sh` covers a fresh build
and a second invocation in the same directory. See the validation record for
measured results.
