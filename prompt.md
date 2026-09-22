# 개발 프롬프트 기록 — hp-scan-macos-scan-type-5

이 문서는 소스코드 자체가 아니라 **이 프로젝트가 어떤 문제를 해결하기 위해 어떤 단계로 진행되었는지**를 기록합니다.

## 목표

macOS 26 Tahoe / Apple Silicon에서 HP LaserJet Pro MFP M127fn의 USB 스캐너를 동작시킨다.

조건:

- HPLIP/SANE 기반
- M127fn은 `scan-type=5`
- Linux-only proprietary `.so`를 macOS에서 직접 사용할 수 없음
- macOS arm64 native 구현 필요
- clean build가 재현 가능해야 함

## Phase 1 — 장치 및 HPLIP 경로 확인

```text
Model: HP LaserJet Pro MFP M127fn
VID:PID: 03f0:322a
HPLIP scan-type: 5
```

## Phase 2 — Linux plugin 분석

확인된 export:

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

핵심 HPMUD dependency:

```text
hpmud_open_channel
hpmud_read_channel
hpmud_write_channel
hpmud_close_channel
```

## Phase 3 — HPMUD traffic tracing

```text
HP-SOAP-SCAN
GetScannerElements
CreateScanJob
RetrieveImage
CancelJob
```

## Phase 4 — DIME/JPEG reconstruction

`RetrieveImage` 응답은 HTTP chunked body 안의 DIME record stream으로 전달된다. 초기 단순 SOI→EOI 복사는 continuation header를 이미지에 포함시켜 JPEG를 손상시켰다.

해결:

- 12-byte DIME header 파싱
- options/id/type/data 4-byte padding 처리
- `image/jfif` record만 추출
- CF continuation 연결
- 마지막 JPEG 검증

## Phase 5 — native macOS plugin

```text
soapht-macos/bb_soapht_macos.c
soapht-macos/build-plugin.sh
```

결과:

```text
Mach-O 64-bit dynamically linked shared library arm64
```

## Phase 6 — HPLIP runtime/build fixes

정상 runtime config:

```text
/opt/homebrew/etc/hp/hplip.conf
```

`--disable-network-build`에서 비어 있는 `libhpipp.la`는 macOS `ar`이 거부하므로 generated Makefile dependency에서 제거한다.

## Phase 7 — 검증

```text
Gray 150 dpi
Gray 300 dpi
Gray 600 dpi
Color 300 dpi
Color 600 dpi
```

## 다음 단계

```text
ADF
geometry/crop
regression tests
native macOS scan integration
```
