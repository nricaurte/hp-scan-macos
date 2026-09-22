# Architecture

## Overview

```text
scanimage / SANE frontend
        │
        ▼
libsane-hpaio
        │
        ▼
HPLIP soapht backend
        │
        ▼
bb_soapht.so
        │
        ▼
libhpmud
        │
        ▼
USB
        │
        ▼
HP LaserJet Pro MFP M127fn
```

## Native plugin API

```text
bb_open
bb_close
bb_get_parameters
bb_is_paper_in_adf
bb_start_scan
bb_get_image_data
bb_end_page
bb_end_scan
```

## HPMUD API

```text
hpmud_open_channel
hpmud_write_channel
hpmud_read_channel
hpmud_close_channel
```

SOAPHT channel:

```text
HP-SOAP-SCAN
```

## Lifecycle

```text
bb_open → GetScannerElements
bb_start_scan → CreateScanJob (first page)
bb_get_image_data → RetrieveImage → HTTP chunking → DIME → JPEG
bb_end_page → release JPEG; keep JobId and geometry for ADF
bb_is_paper_in_adf → sensor settling / next-page decision
bb_start_scan → reuse ADF JobId (later pages)
bb_end_scan / bb_close → CancelJob + local cleanup
```

## Current scope

Implemented: USB, Flatbed Gray 150/300/600 and Color 300/600; simplex ADF
Gray 150/300 and Color 300; multi-page batches; PaperInADF debounce; dynamic
geometry/crop/offset; wrapper page-size presets and multi-page PDF.

The native plugin keeps the HPLIP `soap_session` binary layout unchanged; on
macOS `user_cancel` is `volatile sig_atomic_t`, with matching size/alignment
checked by regression. Zero remains
success; native failures return SANE status values. The patched macOS backend
preserves busy/cancel/jammed/no-memory statuses, while Linux plugin errors retain the
legacy generic-I/O mapping. Paper detection returns 0/1 or a negative error.
An exact MediaJam device reason takes precedence over the paper sensor. After
a malformed completed ADF image response, one status query can identify an
explicit jam; unknown failures remain I/O errors and image requests are not replayed.

Each transaction owns and closes its HPMUD channel. Partial writes stop on the
first error. An absolute monotonic deadline spans request writes and response
reads. CreateScanJob/RetrieveImage are never blindly retried, because a retry
could create another job or advance the feeder. One-second response polling
retains partial timeout data within that deadline. The macOS USB adapter maps
hard libusb errors to I/O failure and never supplies an infinite read timeout.

The macOS cancel callback only sets the cancellation flag. Read, next start or
close performs cleanup outside the signal handler; failed cleanup prevents a
new job until cancellation can be completed. Once request bytes are sent, the
transaction finishes writing and drains the current response under its original
deadline, preventing leftover DIME bytes from contaminating the next request.
Cancelled CreateScanJob replies retain their JobId before cleanup. Cancellation
can wait for the current acquisition; it is not an immediate transport abort.

A known JobId is cancelled on image failure, error end-page, end-scan and close.
Failed cancellation retains the JobId and marks it pending; a subsequent start
must cancel it before creating/reusing a job. Local image buffers and geometry
are cleared at end-scan even if device cleanup fails. A normal ADF page end
retains the job geometry. If CreateScanJob fails before a JobId is received, the
client cannot identify or cancel an unknown device-side job.

ADF defaults are chosen during backend option initialization, not inferred
from the scan ticket height. Explicit Legal requests remain 14 inches; the
plugin only clamps requests above the ADF hardware maximum. This does not
change the Flatbed branch or its extents.

Pending: physical full-length Legal validation, additional scan-type=5 hardware,
completion of native-app ADF/recovery validation, and stronger interruption
guarantees for blocking HPMUD I/O.


## Capability negotiation

The native plugin uses system libxml2 to read source availability, geometry,
optical limits, color modes and JFIF support from GetScannerElements. Parsed data
lives in the private `bb_state`; no public ABI fields are added. The patched
backend refreshes source resolution lists and keeps the selected DPI valid.
Only implemented JPEG compression is advertised by the macOS SOAPHT backend.
See [Compatibility](COMPATIBILITY.md) for inheritance rules, bounds and limitations.


## Local eSCL adapter

Image Capture / Preview → Apple AirScan → loopback HTTP / Bonjour LocalOnly →
Go bridge → one scanimage batch process → installed HPAIO / SOAPHT / HPMUD → USB.
The bridge discovers source options without scanning and uses a read-only SOAPHT
probe for scanner/paper status. Completed scanimage files are delivered one per
NextDocument; errors are not sent as successful empty image bodies.
See [AIRSCAN.md](AIRSCAN.md) for lifecycle, bounds and service management.
