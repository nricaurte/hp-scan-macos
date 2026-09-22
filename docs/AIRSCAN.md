# Local AirScan / eSCL integration

The bridge exposes either an installed HPAIO SOAPHT USB scanner or an explicitly
configured WSD network scanner to macOS Image Capture and Preview. It listens only on **127.0.0.1**, with a Bonjour `_uscan._tcp` proxy
registered on the **LocalOnly** interface. It does not share the scanner with
other Macs, phones or the LAN. The USB driver and command-line wrapper remain
independent of this service.

For LAN/WSD setup and validation boundaries, see [WSD_NETWORK.md](WSD_NETWORK.md).

## Build and start (USB)

Install the native backend using `./build.sh` first. With Go installed, that build
also builds the bridge and its read-only `hp-soapht-probe` helper, including when
`INSTALL=0` is used. To rebuild only the bridge and helper:

```bash
HPLIP_SRC=/tmp/hplip-build/hplip-3.25.8 ./airscan-bridge/build.sh
./airscan-bridge/service.sh install
```

This installs `~/Applications/HP AirScan Bridge.app`, the USB probe helper under
`~/Library/Application Support/HP SOAPHT AirScan/`, and a LaunchAgent named
`io.github.kimdongup.hp-soapht-airscan`. It starts immediately and at login.
No administrator password is required for this per-user service. The old
`com.nricaurte.hp-airscan.plist` sample is superseded by `service.sh`.

With multiple HP USB devices, explicitly select the SOAPHT scanner:

```bash
AIRSCAN_DEVICE='hpaio:/usb/YOUR_MODEL?serial=YOUR_SERIAL' \
  ./airscan-bridge/service.sh install
```

Auto-selection requires exactly one HPAIO USB scanner. The probe verifies
HPLIP `scan-type=5` before advertising a scanner. If the device is disconnected
at login, launchd retries startup. The default port is 8089; `AIRSCAN_PORT` can
select another unprivileged port at installation. A port collision fails instead
of starting a second scanner service.

```bash
./airscan-bridge/service.sh status
./airscan-bridge/service.sh stop
./airscan-bridge/service.sh start
./airscan-bridge/service.sh uninstall
```

Uninstall removes the LaunchAgent; it retains the per-user binaries and logs.
Logs: `~/Library/Logs/HP SOAPHT AirScan/bridge.log`. `AIRSCAN_DEBUG=1` at service
installation enables HTTP method/path/status/byte-count traces and bounded
backend error diagnostics, useful for native client diagnosis.
Normal operation logs job completion/failure, without continuous status traces.
Do not start the older upstream bridge alongside this service.

## Use macOS apps

In **Image Capture**, select `HP … (SOAPHT)` in the shared/network scanner list.
Choose Flatbed or Document Feeder and the offered resolution/color settings.
The app can save its received images as JPEG or assemble them into PDF; the
bridge advertises and transfers JPEG only. In **Preview**, use File → Import
from Scanner and select the same device. Menu wording depends on macOS language. Close the scanner session in Image
Capture before opening it in Preview; macOS grants one app the device session.
Disable automatic item detection and choose a standard paper size for whole-page
scanning; regions below the reported minimum are rejected.

The device may take a few seconds to appear. A completed job has a 12-second
settling interval before the next acquisition. The next job can be queued during
that interval; the bridge does not replay a failed CreateScanJob/RetrieveImage.

## Actual capabilities and model scope

Capabilities come from each source's installed `scanimage -A` options, under the
C locale. Source minimum sizes additionally come from the read-only SOAP
capability probe, with a one-unit margin for SANE fixed-point and strict minimum
height checks. The bridge exposes only implemented Gray/RGB modes, reported discrete
DPI values and reported geometry bounds. It fails startup for unrecognized option
shapes instead of advertising guessed scanner properties. It does not enable
Lineart, duplex, HPRAW or arbitrary resolutions that the native plugin lacks.

M127fn: Flatbed 150/300/600 DPI, ADF 150/300 DPI; ADF explicit Legal is 4200 eSCL
units (355.6 mm), while omitted geometry keeps the source's approximately A4
height. eSCL dimensions and offsets are in 1/300 inch, independently of scan DPI.
Coordinates are quantized downward to SANE 16.16 mm before combining offsets
and extents; this avoids a one-unit overflow at a crop touching the bottom edge.
Out-of-bounds requests are rejected before paper movement. Native clients choose
their scan region; the bridge does not turn their explicit Legal request into A4.

A flatbed-only or ADF-only device advertises only that source. Other scan-type=5
HP models use the same negotiation path, without a hardcoded M127 model whitelist.
They remain **experimental until tested on actual hardware**. See
[COMPATIBILITY.md](COMPATIBILITY.md) and [SOAPHT_MODELS.md](SOAPHT_MODELS.md).

## Jobs, errors and cancellation

One eSCL job owns one `scanimage --batch --batch-print` process, preserving the
SANE handle and SOAPHT JobId across ADF pages. scanimage publishes a file name only
after closing and renaming a completed page; NextDocument serves those completed
JPEGs in order. Page counters use the PWG namespace; ImagesToTransfer reports completed pages
still waiting for delivery, independently of the requested batch limit.
A second acquisition is rejected as busy. Concurrent NextDocument
requests for the same job are rejected rather than delivering the same page twice.

While a page is being acquired, NextDocument waits up to two seconds and then
returns temporary HTTP 503 with Retry-After. This lets Apple AirScan handle cancel
between retries. Completed failures never use that temporary-busy response.
Normal exhaustion returns HTTP 404 from NextDocument. Failed acquisition returns
HTTP 500 and an Aborted job state (an empty feeder returns 404); valid preceding pages can still be retrieved.
ScannerStatus reflects the idle device's PaperInADF/MediaJam response through the
read-only probe, and reports Processing during acquisition. A failed or malformed
status query is not advertised as a healthy empty feeder.

DELETE, disconnected page requests and shutdown signal SIGINT to scanimage so the
native driver's CancelJob/draining cleanup runs. After an unsuccessful worker
exit, the bridge keeps the acquisition lock while the helper drains pending USB
replies (8-second / 32-MiB maximum, ending on a quiet read timeout) and reads fresh
status. This does not replay CreateScanJob or create a new scan. It cannot cancel
a device job whose JobId was never received, or clear a physical jam. Cleanup can wait for the current
acquisition. A 320-second grace period precedes forced termination. Each job has
a 15-minute acquisition limit, 100-page / 256-MiB spool limits and a 32-MiB limit
per completed JPEG. Abandoned jobs expire; completed spools are removed after two
minutes without activity or on DELETE/shutdown. These are bridge limits, not
statements of the scanner's physical capacity.

After a physical ADF jam, remove the jam and follow the printer display to clear
the error and close the feeder cover. Canceling in the Mac app ends its job but
does not acknowledge a physical device error. The observed M127fn remained
Stopped with numeric reason 11 until the printer returned to Idle; that numeric
code has no confirmed semantic mapping. Reselect the scanner if the app retains
an old progress display after the device is ready.

## Validation

```bash
(cd airscan-bridge && go test -race ./... && go vet ./...)
bash tests/build_reentry.sh
```

Regression includes real M127 option-text fixtures, alternate source/size/DPI
profiles, XML/geometry validation, a mock scanner process, page delivery,
mid-batch failure, empty feeder, concurrent jobs and cancel/restart. It does not
count synthetic profiles as physically verified HP models.

See [AIRSCAN_VALIDATION.md](AIRSCAN_VALIDATION.md) for the measured native-client
results and remaining limits. This is a tested eSCL subset, not a Mopria
certification claim.

Protocol/client references: [sane-airscan eSCL implementation](https://github.com/alexpevzner/sane-airscan/blob/master/airscan-escl.c),
[SANE scanimage implementation](https://gitlab.com/sane-project/backends/-/blob/master/frontend/scanimage.c),
[OpenPrinting JobInfo implementation](https://github.com/OpenPrinting/go-mfp/blob/master/proto/escl/jobinfo.go),
and [Apple Image Capture guide](https://support.apple.com/guide/image-capture/welcome/mac).
