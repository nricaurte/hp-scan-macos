# Roadmap

## v0.2 — Flatbed SOAPHT baseline

- [x] macOS arm64 HPAIO/HPMUD/HPIP integration and native SOAPHT plugin
- [x] USB discovery, SOAP requests, HTTP chunks, DIME and JPEG reconstruction
- [x] Flatbed Gray 150/300/600 and Color 300/600

## v0.3 — ADF and geometry

- [x] ADF simplex Gray 150/300 and Color 300
- [x] PaperInADF detection and between-page debounce
- [x] One JobId across multi-page ADF batches
- [x] Dynamic SANE geometry, crop and offset
- [x] Wrapper page-size presets and multi-page PDF
- [x] Preserve explicit Legal height while retaining the ordinary ADF default
- [ ] Validate bottom-edge capture using a physical Legal sheet

## v0.4 — Robustness

- [x] Replace unconditional backend debug output with SANE-controlled logging
- [x] Error cleanup, bounded parser inputs and strict numeric validation
- [x] Explicit MediaJam to SANE JAMMED mapping and malformed-ADF status check
- [x] Total transaction timeout budget and busy/cancel status propagation
- [x] Cancel/close cleanup; retain failed-cancel JobId for recovery
- [x] Signal-safe macOS cancellation flag and deferred cleanup
- [x] One-second response polling and hard USB error propagation
- [x] Deterministic transport/parser/lifecycle regression with sanitizers
- [x] Build-only mode and fail-fast patch application
- [x] Repeated-build Makefile preservation and fresh/reentry integration regression
- [x] README, architecture, build/test, research history and troubleshooting updates
- [x] Fresh default-workdir build reproduces backend/header/USB patches
- [x] Verify latest jam-recovery installation hashes/runtime paths, Flatbed matrix and ADF Gray 150 batch
- [x] Installed Color 300/A4 two-page PDF and concurrent-client busy check
- [x] Fresh jam-recovery candidate Color 300/A4 four-page PDF and normal termination
- [ ] Establish the cause of the earlier intermittent Color termination error; retest passed, release gate remains
- [x] M127fn flatbed matrix, ADF Gray 150/300, Color 300 four-page PDF
- [x] Physical cancel-after-start and same-handle busy/restart recovery
- [x] Physical mid-acquisition signal cancellation and same-handle/new-process restart
- [x] Drain the in-flight SOAP response before returning cancellation
- [ ] Physical timeout/disconnect and mid-page failure recovery

## v0.5 — Wider compatibility

- [x] Parse per-device SOAPHT sources, size limits, optical DPI, color and JFIF support
- [x] Namespace-aware bounded XML parsing and source-specific resolution refresh
- [x] Candidate compatibility inventory and read-only capability capture tool
- [x] Synthetic alternate-capability regression fixtures
- [ ] Physical validation of additional HPLIP scan-type=5 devices
- [ ] Broader HTTP framing and SOAP fault interoperability

## v1.0 — macOS integration

- [ ] Polished installer/uninstaller and release artifacts
- [x] Capability-driven local eSCL bridge and per-user Bonjour/LaunchAgent service
- [x] Optional WSD transport, BMP conversion and simulated batch/cancel recovery
- [x] WSD LaunchAgent Local Network permission and Image Capture Flatbed acquisition
- [x] WSD Image Capture ADF two-page PDF and automatic batch termination
- [ ] WSD Preview, physical jam recovery and native-client cancel/restart validation
- [x] Image Capture discovery and repeated Flatbed acquisition
- [x] Native crop boundary conversion regression and completed/canceled job cleanup tests
- [x] Image Capture ADF JPEG/PDF batch and native-client cancel/restart
- [x] Drain HTTP error bodies before CancelJob/status recovery
- [x] Advertise SOAP minimum geometry and bounded pending-image responses
- [x] Final system-wide installation, artifact hashes and override-free runtime verification
- [ ] Repeat physical jam/cancel recovery using the final installed service
- [x] Preview direct Flatbed acquisition and saved document
- [ ] Fresh-Mac release test
