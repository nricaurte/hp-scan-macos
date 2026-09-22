# Build and test guide

## Clean build without installation

Use a new work directory; keep the working installation available for comparison.

```bash
cd ~/hp-scan-macos
WORK="$(mktemp -d /tmp/hplip-v04.XXXXXX)" INSTALL=0 ./build.sh
```

`INSTALL=0` builds HPMUD, HPIP, HPAIO and the native arm64 SOAPHT plugin
under `$WORK/hplip-3.25.8/.libs`. It does not install scanner libraries, wrappers,
apps or the bridge. Missing Homebrew prerequisites may still be installed.
The build no longer overwrites the tracked `soapht-macos/bb_soapht.so`.

To reuse an existing download, copy `hplip-3.25.8.tar.gz` into the new work directory
before running the command. An already-applied patch is accepted; a conflicting
or inapplicable patch stops the build. For an old v0.3 patched tree, use a fresh
`WORK` directory instead of trying to layer v0.4 over it.

For a normal installation, run `./build.sh` with a fresh `WORK` and the default
`INSTALL=1`. Installing the plugin into the default root-owned directory needs
`sudo`. The installer checks administrator authentication before changing runtime
files, so a missing password cannot leave a new backend paired with the old plugin.
Run installation in an interactive terminal. Native runtime paths target `/opt/homebrew`.

## Automated regression (no device required)

```bash
HPLIP_SRC=/tmp/hplip-build/hplip-3.25.8 bash tests/run.sh
```

Uses actual HPLIP headers, mocked HPMUD, a deterministic monotonic clock,
`-Wall -Wextra -Werror`, AddressSanitizer and UndefinedBehaviorSanitizer.
Checks include fragmented HTTP/chunks, empty and malformed replies, allocation
failure, short writes, stopped writes, timeout, busy, cancellation, DIME/JPEG
reassembly and truncation, ADF JobId reuse, debounce, failed-cancel retry, close
cleanup, mid-page failure/restart, scan-ticket geometry and row sizes.
The suite also compiles the patched USB read adapter with mocked libusb to check
partial timeouts, disconnection and finite waits, and exercises the real wrapper
with a fake scanimage to check geometry argument routing. These tests do not establish
physical feed behavior or image quality.

## AirScan bridge regression

For the Go AirScan bridge (USB and optional WSD transport), run separately:

```bash
(cd airscan-bridge && go test -race ./... && go vet ./...)
```

WSD tests use an HTTP scanner simulator and the M127fn capability fixture. They
cover image decoding, batch termination, retained pages after failure, busy and
cancel recovery, ambiguous creation, and permission/offline startup retries.
They do not require HPLIP or feed paper. See [WSD_NETWORK.md](WSD_NETWORK.md) for
LAN installation and the separate physical/native-app validation boundary.

## Repeated build regression

The build explicitly runs configure, applies the macOS Makefile edits, then
suppresses make's automatic regeneration of the release tarball's generated
Makefile/configure inputs. Otherwise newer included `.inc` files can invoke
legacy Automake and `config.status`, restoring the empty `libhpipp` dependency
and `/etc/hp` at build time. Each invocation also cleans generated objects before
compiling, because make does not track changes to compiler flags or `CONFDIR`.
This permits reuse of an already patched `WORK` without mixing old objects.

An opt-in integration check performs a fresh extraction/build followed by a
second build in the same directory, with installation disabled:

```bash
bash tests/build_reentry.sh
# Or use a chosen directory with no extracted hplip source yet:
WORK="$PWD/build/reentry-check" bash tests/build_reentry.sh
```

The test retains both logs and checks the configured path, scanner link inputs,
and absence of an Automake regeneration or empty network-library link.

## Test the new build without installing it

Set `LIBS` to the `.libs` directory from the clean build:

```bash
LIBS='/absolute/path/to/work/hplip-3.25.8/.libs'
TEST_CONFIG="$(mktemp -d /tmp/sane-v04.XXXXXX)"
printf 'hpaio\n' > "$TEST_CONFIG/dll.conf"
export DYLD_LIBRARY_PATH="$LIBS"
export HPAIO_SOAPHT_PLUGIN="$LIBS/bb_soapht.so"
export SANE_CONFIG_DIR="$TEST_CONFIG"
scanimage -L
scanimage -A
```

`HPAIO_SOAPHT_PLUGIN` is an explicit macOS development override. When unset,
the existing `/opt/homebrew/share/hplip/scan/plugins/bb_soapht.so` path is used.
Verify the actual loaded files once, rather than relying only on SANE's printed
search path:

```bash
DYLD_PRINT_LIBRARIES=1 scanimage -A 2> /tmp/soapht-libraries.log
rg 'libsane-hpaio|libhpmud|libhpip|bb_soapht' /tmp/soapht-libraries.log
```

macOS protected shell executables can remove `DYLD_*` from their inherited
environment. To test the repository wrapper, export the variables **inside** its
shell, then source the wrapper (run this in a subshell):

```bash
(
  export DYLD_LIBRARY_PATH="$LIBS"
  export HPAIO_SOAPHT_PLUGIN="$LIBS/bb_soapht.so"
  export SANE_CONFIG_DIR="$TEST_CONFIG"
  source bin/hp-scan --source ADF --resolution 300 --mode Color \
    --page-size A4 --batch /tmp/m127-adf.pdf
)
```

After testing, use a fresh terminal or unset the three exported variables to
return to the installed driver.

## Hardware regression matrix

Always use a test document and inspect both dimensions and content. Reload the
ADF between batches. On the tested device, ending a one-page batch with more
sheets loaded ejected the remaining sheets without capturing them; load only
one sheet for single-page tests. Record source, mode, resolution, page size, page count,
exit status, elapsed time and whether the next scan succeeds.

| Source | Mode | DPI | Additional checks |
|---|---|---|---|
| Flatbed | Gray | 150, 300, 600 | Full page, repeated scan |
| Flatbed | Color | 300, 600 | Full page and color content |
| ADF simplex | Gray | 150, 300 | Single page, multiple pages, last-page stop |
| ADF simplex | Color | 300 | Multiple pages and PDF output |
| Both | Gray | 300 | Offset/crop, A4 and Letter |
| ADF | Gray | 300 | Legal: software regression passed; physical full-length check pending |

```bash
DEVICE='hpaio:/usb/HP_LaserJet_Pro_MFP_M127fn?serial=YOUR_SERIAL'
scanimage -d "$DEVICE" --source Flatbed --mode Gray --resolution 300 \
  --format=jpeg -o /tmp/flatbed-gray300.jpg
scanimage -d "$DEVICE" --source Flatbed --mode Gray --resolution 300 \
  -l 10 -t 20 -x 100 -y 150 --format=jpeg -o /tmp/crop.jpg
scanimage -d "$DEVICE" --source ADF --mode Gray --resolution 300 \
  --format=jpeg --batch='/tmp/adf-%03d.jpg'
```

Measured default flatbed raster sizes were 1275×1753 at 150 dpi,
2550×3507 at 300 dpi and 5100×7014 at 600 dpi. Explicit geometry may differ
by a pixel due to fixed-point/unit rounding.

ADF Legal height is now preserved in both `InputMediaSize/Height` and
`ScanRegionHeight`: 355.6 mm becomes 14000 thousandths of an inch. The backend
selects the existing 11.689-inch default when switching to ADF, before explicit
page-size options are applied. The hardware maximum remains 355.6 mm. Resetting
the ADF bottom to AUTO at zero top offset restores the default; with a nonzero
top offset it retains the previous maximum-bottom behavior. Flatbed is unchanged.

Run the compiled-backend option test alongside the mock transport suite:

```bash
HPLIP_SRC='/path/to/work/hplip-3.25.8' \
  HPAIO_MODULE='/path/to/work/hplip-3.25.8/.libs/libsane-hpaio.1.so' \
  bash tests/run.sh
```

This checks ADF defaults, explicit Legal, AUTO and source switching without
opening USB. Mock tests verify Legal, near-Legal, A4, Letter and default heights
at 150/300 dpi, maximum clamping and multi-page ticket reuse. Actual Legal paper
with a visible bottom-edge mark is still needed for physical end-to-end validation.
Expected ADF heights at 300 dpi (unit rounding can change a pixel):

| Geometry | Requested height | Approximate raster height |
|---|---|---|
| Default | 296.9006 mm | 3507 px |
| A4 | 297 mm | 3508 px |
| Letter | 279.4 mm | 3300 px |
| Legal | 355.6 mm | 4200 px |

Custom `scanimage -l/-t/-x/-y` values pass through the SANE geometry handler.
ADF batch pages retain the job's initial ticket/geometry; each new batch can use
a different preset or crop. The software suite checks reuse for every preset.
An unclipped full Legal image should be approximately 2550 × 4200 pixels;
checking dimensions alone does not prove the bottom content was captured.

## Recovery checks

- Empty ADF: `SANE_STATUS_NO_DOCS`, then load paper and scan again.
- Busy: a second client must fail without interrupting the first scan; retry
  after the first client has closed.
- Cancel a scan, then scan again. Also cancel after a completed ADF page.
- Timeout/disconnect and malformed responses: use the mock suite for repeatable
  fault injection. A real USB unplug/replug test is a separate hardware test.

Normal SOAP transactions have a 45-second total I/O budget; RetrieveImage has
300 seconds and CancelJob has 5 seconds. HPMUD accepts integer-second timeouts
(rounding can add under one second). Channel open/close calls have no timeout
argument and are outside the guarantee. Response reads poll at one-second
intervals within the same transaction/deadline; partial timeout bytes are retained.
USB disconnection is propagated as I/O error instead of an empty read.

On macOS, `soapht_cancel` only sets a `volatile sig_atomic_t` flag. Image/USB
cleanup is deferred to read, next start or close, avoiding unsafe cleanup from
a signal handler. Once SOAP request bytes have been sent, the driver finishes
that request and drains its response before returning CANCELLED, within the
original transaction deadline. This preserves the USB response boundary; cancel
can therefore wait for the current physical acquisition (up to the remaining
300-second image budget). One-second polling does **not** promise one-second
cancellation. HPMUD open/close remain outside that timeout guarantee.
A cancelled CreateScanJob reply is parsed to retain its JobId for CancelJob.
The compiled-backend test delivers a real signal and verifies deferred cleanup,
zero-byte CANCELLED reads and retry after failed cancellation.

## Plugin-only rebuild and install

```bash
mkdir -p build
HPLIP_SRC='/tmp/hplip-build/hplip-3.25.8' \
  OUT="$PWD/build/bb_soapht.so" ./soapht-macos/build-plugin.sh
sudo install -m 0755 build/bb_soapht.so \
  /opt/homebrew/share/hplip/scan/plugins/bb_soapht.so
```

Keep the backend patch and native plugin versions together to expose detailed
busy/cancel/allocation statuses and the correct ADF default/Legal behavior. Older backends still treat nonzero plugin
results as failure, but report generic I/O errors.

For an opt-in same-handle cancel/busy/restart test (flatbed document required):

```bash
mkdir -p build/tests
clang -Wall -Wextra -Werror -I/opt/homebrew/include -L/opt/homebrew/lib \
  tests/sane_recovery.c -lsane -o build/tests/sane_recovery
# Use the isolated runtime exports above before running:
build/tests/sane_recovery "$DEVICE"
```

This cancels after `sane_start` returns and before delivering the raster, checks
`CANCELLED` and zero bytes, then retries on the same handle after a busy response.
It does not exercise cancellation inside a blocked USB read.


To exercise asynchronous cancellation during a slow acquisition instead, keep a
Flatbed original loaded and run the same helper with `--signal`:

```bash
build/tests/sane_recovery "$DEVICE" --signal
```

It starts Color 600, delivers SIGALRM after eight seconds, calls `sane_cancel`
from the handler, expects CANCELLED, then retries Gray 150 on the same handle.
This test consumes no ADF sheets. It requires an idle scanner and remains opt-in.


## Additional SOAPHT models

Native plugin builds and sanitizer tests now use the macOS SDK's libxml2 headers
and system `libxml2`. No extra Homebrew XML library is required. Install Xcode
Command Line Tools/Xcode if `xcrun --show-sdk-path` is unavailable. Alternate
capability fixtures run in the ordinary test suite; only M127fn's fixture is a
real device capture. See [Compatibility](COMPATIBILITY.md) for the candidate
inventory, read-only probe and per-model physical regression requirements.


If an ADF batch fails after some pages were acquired, the wrapper returns the
original scanimage error and retains its temporary scan directory, printing the
path. Files there may include an incomplete last page; inspect them before reuse.
It does not replace the requested output PDF with a partial success. Remove the
retained directory after recovering the pages you need.


### Jam recovery follow-up build

`build/compatibility/jam-recovery-clean` is the newer clean candidate containing
explicit MediaJam propagation and DIME error diagnostics. See the validation
record for which checks used this build and whether it has been installed.
When testing with `DYLD_LIBRARY_PATH`, keep the candidate backend and plugin in
the same library directory. dyld can resolve an absolute plugin override by
basename from that search directory; verify the actual loaded paths with
`DYLD_PRINT_LIBRARIES=1` before attributing hardware results to a candidate.


## Native AirScan adapter

With Go available, both normal and `INSTALL=0` builds also produce
`.libs/airscan/airscan-bridge` and `.libs/airscan/hp-soapht-probe`.
`./airscan-bridge/build.sh` builds only those adapters against existing HPLIP
headers/runtime; it does not rebuild or install the scanner libraries.
`(cd airscan-bridge && go test -race ./... && go vet ./...)` exercises its
HTTP/process adapter. See [AIRSCAN.md](AIRSCAN.md) for per-user service setup
and [AIRSCAN_VALIDATION.md](AIRSCAN_VALIDATION.md) for native application results.
