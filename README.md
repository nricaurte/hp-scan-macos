# hp-scan-macos-scan-type-5

macOS 26 Tahoe / Apple Silicon에서 **HP LaserJet Pro MFP M127fn**의 USB 스캔 기능을 복원하기 위한 HPLIP/SANE 기반 프로젝트입니다.

이 저장소는 [`nricaurte/hp-scan-macos`](https://github.com/nricaurte/hp-scan-macos)를 기반으로 하며, 기존 프로젝트의 LEDM(`scan-type=7`) 지원에 더해 **SOAPHT 계열 `scan-type=5` 장치**를 macOS에서 사용할 수 있도록 확장합니다.

현재 핵심 목표는 Linux용 HP proprietary plugin에 의존하지 않고, macOS용 native Mach-O SOAPHT compatibility plugin을 통해 M127fn을 동작시키는 것입니다.

## 개발 배경

이 프로젝트는 제가 사용하던 **HP LaserJet Pro MFP M127fn**을 macOS Tahoe / Apple Silicon에서 다시 스캔에 활용하려는 시도에서 시작했습니다.

처음에는 [HPScanner4MacOS](https://github.com/herb2k/HPScanner4MacOS)의 안내를 참고했습니다. HP 5.1.1 Printer Software Update를 최신 macOS에 우회 설치하고, Image Capture Support Apps를 추가한 뒤, 여러 드라이버를 시도해 맞는 것을 선택하면 이미지 캡처에서 스캐너를 사용할 수 있다는 방법이었습니다. 하지만 제 환경에서는 이 과정으로 정상 동작을 얻지 못했습니다.

이후 [hp-printer-fix-macos](https://github.com/pavelbinar/hp-printer-fix-macos)의 `HewlettPackardPrinterDrivers.pkg` 수정 방법과, 이미 수정된 패키지를 제공하는 [HewlettPackardPrinterDrivers-MacOS](https://github.com/gabrielllzs/HewlettPackardPrinterDrivers-MacOS)도 찾아보았습니다. 두 프로젝트가 설명하는 수정은 드라이버 자체의 변경이나 재서명이 아니라, 설치 프로그램의 `Distribution` 파일에서 macOS 버전 제한을 해제하는 것입니다. 다만 기존 HP 스캐너 지원 앱을 Apple Silicon에서 사용하는 경로에는 Rosetta 2가 필요하다는 안내가 있어, 이 방법은 더 진행하지 않았습니다.

그러던 중 ChatGPT의 도움으로 [nricaurte/hp-scan-macos](https://github.com/nricaurte/hp-scan-macos)의 HPLIP/SANE 기반 접근을 알게 되었습니다. 당시 이 프로젝트는 LEDM(`scan-type=7`) 장치를 대상으로 하고 있었고, 제가 가진 M127fn은 SOAPHT(`scan-type=5`) 장치여서 추가 구현이 필요했습니다.

이를 출발점으로 M127fn 실기기에서 직접 스캔과 통신 실험을 반복하며, SOAPHT 동작을 클린룸 방식으로 재구현한 **macOS arm64 네이티브 호환 플러그인**을 개발했습니다. 이 저장소는 그 구현과 검증 과정을 담고 있으며, Rosetta 2에 의존하지 않는 스캔 경로를 만드는 것이 개발 방향입니다.

## 현재 확인된 환경

| 항목 | 상태 |
|---|---|
| macOS 26 Tahoe | ✅ |
| Apple Silicon / arm64 | ✅ |
| HPLIP 3.25.8 | ✅ |
| Homebrew SANE 1.4.x | ✅ |
| libusb | ✅ |
| HP LaserJet Pro MFP M127fn USB | ✅ |
| `scan-type=5` / SOAPHT | ✅ |
| Flatbed Gray 150 dpi | ✅ |
| Flatbed Gray 300 dpi | ✅ |
| Flatbed Gray 600 dpi | ✅ |
| Flatbed Color 300 dpi | ✅ |
| Flatbed Color 600 dpi | ✅ |
| ADF simplex Gray 150/300, Color 300 | ✅ |
| ADF multi-page / PaperInADF debounce | ✅ |
| Geometry / crop / offset | ✅ |
| hp-scan presets / multi-page PDF | ✅ |
| Apple Image Capture 직접 연동 | M127fn Flatbed·ADF 2페이지 JPEG/PDF 검증 |
| Apple Preview 직접 연동 | M127fn Flatbed·취소 후 재시작 검증 |

테스트 장치:

```text
HP LaserJet Pro MFP M127fn
USB VID:PID 03f0:322a
HPLIP scan-type=5
Protocol: SOAPHT
```

## 왜 이 프로젝트가 필요한가

M127fn은 HPLIP에서 스캐너로 인식되지만, Linux 환경에서는 SOAPHT 스캔 처리를 위해 HP binary plugin을 사용합니다. 해당 Linux plugin은 macOS에서 직접 사용할 수 없습니다.

원본 plugin의 외부 의존성을 분석한 결과 핵심 통신은 HPLIP의 `hpmud` 채널 API를 통해 이루어지며, 주요 채널은 다음과 같습니다.

```text
HP-SOAP-SCAN
```

실제 장치 트래픽 분석을 통해 다음 흐름을 확인했습니다.

```text
GetScannerElements
        ↓
CreateScanJob
        ↓
RetrieveImage
        ↓
DIME records
        ↓
JPEG image
        ↓
CancelJob
```

이 프로젝트는 이 동작을 clean-room 방식으로 재구현한 macOS arm64 plugin을 포함합니다.

## 저장소 구조

```text
hp-scan-macos-scan-type-5/
├── README.md
├── readme.en.md
├── prompt.md
├── prompt.en.md
├── build.sh
├── LICENSE
├── soapht-macos/
│   ├── bb_soapht_macos.c
│   └── build-plugin.sh
├── patches/
│   ├── 01-darwin-headers.patch
│   ├── 02-musb-macos.patch
│   ├── 03-hpaio-uninit-fix.patch
│   └── 04-soapht-macos-plugin.patch
├── stubs/
├── bin/
├── app/
├── airscan-bridge/
└── docs/
    ├── ARCHITECTURE.md
    ├── BUILD_AND_TEST.md
    ├── RESEARCH_HISTORY.md
    ├── TROUBLESHOOTING.md
    └── ROADMAP.md
```

## 설치

```bash
git clone https://github.com/kimdongup/hp-scan-macos-scan-type-5.git
cd hp-scan-macos-scan-type-5
./build.sh
```

`build.sh`는 HPLIP 3.25.8 다운로드, Darwin patch 적용, `libhpmud`/`libhpip`/`libsane-hpaio` 빌드, native `bb_soapht.so` 빌드 및 설치, SANE backend 등록까지 수행합니다.

## 장치 확인

```bash
scanimage -L
```

정상 예:

```text
device `hpaio:/usb/HP_LaserJet_Pro_MFP_M127fn?serial=XXXXXXXXXXXX' is a Hewlett-Packard HP_LaserJet_Pro_MFP_M127fn all-in-one
```

## 지원 옵션 확인

```bash
scanimage -d 'hpaio:/usb/HP_LaserJet_Pro_MFP_M127fn?serial=YOUR_SERIAL' --all-options
```

현재 주요 옵션:

```text
--mode Gray|Color
--resolution 150|300|600dpi
--source Flatbed|ADF
--compression None|JPEG
```

## 실제 스캔

### 300 dpi Gray

```bash
scanimage   -d 'hpaio:/usb/HP_LaserJet_Pro_MFP_M127fn?serial=YOUR_SERIAL'   --resolution 300   --mode Gray   --source Flatbed   --format=jpeg   > ~/Desktop/m127-gray-300.jpg
```

### 300 dpi Color

```bash
scanimage   -d 'hpaio:/usb/HP_LaserJet_Pro_MFP_M127fn?serial=YOUR_SERIAL'   --resolution 300   --mode Color   --source Flatbed   --format=jpeg   > ~/Desktop/m127-color-300.jpg
```

## ADF 배치 PDF와 v0.4 검증

```bash
hp-scan --source ADF --resolution 300 --mode Color \
  --page-size A4 --batch ~/Desktop/document.pdf
```

ADF는 150/300 dpi simplex를 지원합니다. Flatbed는 Gray 150/300/600,
Color 300/600 dpi에서 확인되었습니다. `--page-size`는 A4, Letter, Legal을
받으며 Legal은 ADF 전용입니다. Legal 요청은 전체 355.6 mm 높이로 전달합니다.

```bash
# 기존 설치본을 유지하며 별도 디렉터리에 빌드
WORK="$(mktemp -d /tmp/hplip-v04.XXXXXX)" INSTALL=0 ./build.sh
# 장치 없이 parser/transport/ADF lifecycle 오류 주입 검사
HPLIP_SRC=/tmp/hplip-build/hplip-3.25.8 bash tests/run.sh
```

일반 로그는 기본적으로 조용하며 `SANE_DEBUG_HPAIO=8`로 추적할 수 있습니다.
[Build & Test](docs/BUILD_AND_TEST.md)에 새 빌드만 선택해 실기기를 검사하는
방법과 [Troubleshooting](docs/TROUBLESHOOTING.md)에 복구 동작을 정리했습니다.

## SOAPHT plugin

소스:

```text
soapht-macos/bb_soapht_macos.c
```

설치 위치:

```text
/opt/homebrew/share/hplip/scan/plugins/bb_soapht.so
```

plugin은 다음 HPLIP symbols를 runtime에 사용합니다.

```text
hpmud_open_channel
hpmud_read_channel
hpmud_write_channel
hpmud_close_channel
```

## HPLIP runtime configuration

```text
/opt/homebrew/etc/hp/hplip.conf
```

확인:

```bash
strings /opt/homebrew/lib/libhpmud.0.dylib | grep hplip.conf
```

## 문서

- [Architecture](docs/ARCHITECTURE.md)
- [Build & Test](docs/BUILD_AND_TEST.md)
- [Research History](docs/RESEARCH_HISTORY.md)
- [Troubleshooting](docs/TROUBLESHOOTING.md)
- [Roadmap](docs/ROADMAP.md)
- [Development Prompt / Korean](prompt.md)
- [Development Prompt / English](prompt.en.md)

## Upstream

```text
https://github.com/nricaurte/hp-scan-macos
```

권장 Git remote 구성:

```text
origin   → https://github.com/kimdongup/hp-scan-macos-scan-type-5.git
upstream → https://github.com/nricaurte/hp-scan-macos.git
```

## Project status

현재 **HP LaserJet Pro MFP M127fn USB Flatbed SOAPHT scanning on macOS Tahoe / Apple Silicon**은 동작 검증되었습니다.

ADF simplex, 다중 페이지, PaperInADF debounce, geometry/crop/offset, page-size preset과 PDF wrapper도 구현되어 있습니다.

v0.4에서는 로그 정리, 실패 경로 정리, 트랜잭션 timeout, busy/cancel 상태 전달 및 복구, 설치 없는 clean build와 모의 통신 회귀 검사를 추가했습니다. macOS 취소 콜백은 신호 안전한 플래그만 설정하고 정리는 읽기/다음 시작/닫기 시점에 수행합니다. USB 응답은 1초 단위로 읽으며 분리 오류를 I/O error로 전달합니다. 명시적인 `MediaJam` 상태는 `JAMMED`로 전달하며, 손상된 ADF 이미지 응답 뒤에는 장치 상태를 한 번 확인합니다. 취소 시 이미 시작한 SOAP 응답은 끝까지 받아 다음 요청과 섞이지 않게 하므로, 현재 원고 취득이 끝날 때까지 취소 반환이 지연될 수 있습니다. 실기기 검증 결과는 [v0.4 검증 기록](docs/V0.4_VALIDATION.md)을 참고하십시오.

ADF Legal 요청을 A4 수준으로 줄이던 보정은 제거했습니다. ADF 선택 시 기본 높이는 기존 약 296.9 mm이며, 명시적 Legal 요청은 355.6 mm를 유지합니다. Flatbed 동작은 변경하지 않았습니다. SOAP 요청 및 SANE 옵션 회귀 검사는 통과했으며, 실제 Legal 원고의 하단까지 캡처하는 실기기 검증은 별도입니다.

## License

원본 HPLIP 및 upstream 프로젝트의 라이선스를 따릅니다. HPLIP-derived patch 및 integration code를 배포할 때에는 upstream license 조건을 확인하십시오.


## scan-type=5 모델 호환성 확장

M127fn 고정 capability를 제거하고 `GetScannerElements`의 급지 방식, 최소/최대
크기, 광학 해상도, 색상 및 JFIF 지원을 읽도록 확장했습니다. Flatbed 전용과
ADF 전용 장치도 구분하며, namespace가 있는 XML 응답도 처리합니다. M127fn의
기존 Flatbed geometry와 ADF 기본값/Legal 분리는 유지합니다.

HPLIP 3.25.8에는 scan-type=5 모델 식별자 105개가 있습니다. 이들은 **호환성 후보**이며,
실기기 기준은 여전히 M127fn입니다. 다른 모델의 동작을 검증 완료로 표시하지 않습니다.
지원 범위·제한·신규 모델의 응답 수집 방법은 [호환성 안내](docs/COMPATIBILITY.md),
전체 후보는 [모델 목록](docs/SOAPHT_MODELS.md)을 참고하십시오.
여러 스캐너가 연결된 경우 `hp-scan --device 'hpaio:...' ...`로 선택할 수 있습니다.

## macOS Image Capture / Preview

LAN 연결에는 별도의 WSD 경로를 추가했습니다. USB 모드는 그대로 유지하며,
`AIRSCAN_WSD_URL`을 지정하면 WSD → 로컬 AirScan 방식으로 연결합니다.
M127fn에서 Image Capture의 Flatbed 스캔과 ADF 컬러 300dpi 2장 PDF·자동 종료를 확인했습니다.
설치 방법과 실제 검증 범위는 [WSD 네트워크 스캔](docs/WSD_NETWORK.md)을 참고하십시오.

로컬 AirScan/eSCL 브리지를 추가했습니다. 설치된 SOAPHT 장치의 실제 source·DPI·geometry를
광고하고, ADF 여러 페이지를 한 SANE 작업으로 전달합니다. 서버와 Bonjour 등록은 이 Mac
전용이며 LAN에는 공개하지 않습니다.

M127fn에서 Image Capture의 Flatbed 및 ADF 2페이지 JPEG/PDF, Preview의 Flatbed와
취소 후 재시작을 확인했습니다. ADF의 간헐적 장치 오류는 남아 있으며, 취소는 진행 중인
장치 응답을 정리할 때까지 지연될 수 있습니다. 현재 설치본과 시험 후보의 구분은 아래
검증 기록에 명시했습니다.

```bash
./airscan-bridge/build.sh
./airscan-bridge/service.sh install
```

[설치·사용·제한](docs/AIRSCAN.md)과 [네이티브 앱 검증 기록](docs/AIRSCAN_VALIDATION.md)을
참고하십시오. 다른 HP 모델의 동작은 실제 장치 검증 전까지 experimental입니다.
