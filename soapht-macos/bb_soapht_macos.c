/*
 * bb_soapht_macos.c - clean-room SOAPHT compatibility plugin for
 * HP scan-type=5 devices on macOS (hardware baseline: M127fn).
 *
 * Current scope (v0.4 + compatibility work):
 *   - USB/HPMUD channel: HP-SOAP-SCAN
 *   - Flatbed: Gray/Color, 150/300/600 dpi
 *   - ADF simplex: Gray/Color, 150/300 dpi
 *   - Multi-page ADF batch using one SOAPHT JobId
 *   - JFIF/JPEG transport with HTTP chunking + DIME extraction
 *
 * The public ABI is defined by HPLIP's soaphti.h.
 * This implementation was derived from observable wire behavior and
 * public HPLIP interfaces; it does not contain HP proprietary plugin code.
 */
#include <unistd.h>
#include <ctype.h>
#include <errno.h>
#include <limits.h>
#include <stdint.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "sane.h"
#include "saneopts.h"
#include "hpmud.h"
#include "hpip.h"
#include "soaphti.h"

#define SOAPHT_CHANNEL "HP-SOAP-SCAN"
#define SOAPHT_TIMEOUT 45
#define SOAPHT_IMAGE_TIMEOUT 300
#define SOAPHT_CANCEL_TIMEOUT 5
#define SOAPHT_READ_POLL_TIMEOUT 1
#define SOAPHT_MAX_RESPONSE (16u * 1024u * 1024u)

#include "soapht_capabilities.h"

struct bb_state {
    struct scanner_caps caps;
    int job_id;
    int job_active;
    int cancel_pending;
    int job_is_adf;
    unsigned char *jpeg;
    size_t jpeg_size;
    size_t jpeg_off;
    int pixels_per_line;
    int lines;
    int bytes_per_line;
};

static const char *xml_prefix =
    "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"
    "<SOAP-ENV:Envelope "
    "xmlns:SOAP-ENV=\"http://www.w3.org/2003/05/soap-envelope\" "
    "xmlns:SOAP-ENC=\"http://www.w3.org/2003/05/soap-encoding\" "
    "xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" "
    "xmlns:xsd=\"http://www.w3.org/2001/XMLSchema\" "
    "xmlns:wscn=\"http://tempuri.org/wscn.xsd\">"
    "<SOAP-ENV:Body>";

static const char *xml_suffix =
    "</SOAP-ENV:Body></SOAP-ENV:Envelope>";

static void debug_log(const char *format, ...)
{
    const char *level = getenv("SANE_DEBUG_HPAIO");
    if (!level || atoi(level) < 6) return;
    va_list args;
    va_start(args, format);
    fputs("[bb_soapht] ", stderr);
    vfprintf(stderr, format, args);
    fputc('\n', stderr);
    va_end(args);
}

/* The ABI still uses zero for success. Native failures use SANE_Status;
 * soapht.c maps these without changing struct soap_session or Linux plugins. */
static int transport_status(enum HPMUD_RESULT result)
{
    return result == HPMUD_R_DEVICE_BUSY ? SANE_STATUS_DEVICE_BUSY :
           SANE_STATUS_IO_ERROR;
}

static double monotonic_seconds(void)
{
    struct timespec now;
    if (clock_gettime(CLOCK_MONOTONIC, &now) != 0)
        return 0;
    return (double)now.tv_sec + (double)now.tv_nsec / 1e9;
}

/* HPMUD timeouts are integer seconds, so allow at most one second rounding. */
static int remaining_timeout(struct soap_session *ps, double deadline,
                             int limit, int cancelling)
{
    if (!cancelling && ps->user_cancel)
        return -SANE_STATUS_CANCELLED;
    double now = monotonic_seconds();
    double remaining = deadline - now;
    if (now == 0 || remaining <= 0)
        return -SANE_STATUS_IO_ERROR;
    if (remaining >= limit)
        return limit;
    int seconds = (int)remaining;
    return seconds + (remaining > seconds);
}

static int write_all(struct soap_session *ps, HPMUD_CHANNEL cd,
                     const void *buf, int len, int timeout,
                     double deadline, int cancelling)
{
    const unsigned char *p = (const unsigned char *)buf;
    int off = 0;
    while (off < len) {
        int wait = remaining_timeout(ps, deadline, timeout, cancelling);
        if (wait < 0) return -wait;
        int wrote = 0;
        enum HPMUD_RESULT r = hpmud_write_channel(ps->dd, cd, p + off,
                                                 len - off, wait, &wrote);
        if (!cancelling && ps->user_cancel) return SANE_STATUS_CANCELLED;
        if (r != HPMUD_R_OK) return transport_status(r);
        if (wrote <= 0 || wrote > len - off) return SANE_STATUS_IO_ERROR;
        off += wrote;
    }
    return SANE_STATUS_GOOD;
}

/* Parse an HTTP/1.1 chunked body.
 * return: 1 complete, 0 incomplete, -1 malformed, -2 allocation failure.
 */
static int dechunk_try(const unsigned char *src, size_t src_len,
                       unsigned char **out, size_t *out_len)
{
    size_t p = 0, total = 0;
    unsigned char *dst = NULL;

    while (1) {
        size_t line = p;
        while (line + 1 < src_len &&
               !(src[line] == '\r' && src[line + 1] == '\n'))
            line++;
        if (line + 1 >= src_len) {
            free(dst);
            return 0;
        }

        char hex[32];
        size_t n = line - p;
        if (n == 0 || n >= sizeof(hex)) {
            free(dst);
            return -1;
        }
        memcpy(hex, src + p, n);
        hex[n] = 0;
        char *semi = strchr(hex, ';');
        if (semi) *semi = 0;
        char *endp = NULL;
        errno = 0;
        unsigned long chunk = strtoul(hex, &endp, 16);
        if (!isxdigit((unsigned char)hex[0]) || endp == hex ||
            *endp != '\0' || errno == ERANGE ||
            chunk > SOAPHT_MAX_RESPONSE) {
            free(dst);
            return -1;
        }
        p = line + 2;

        if (chunk == 0) {
            /* The captured device terminates with 0\r\n\r\n. */
            if (p + 2 > src_len) {
                free(dst);
                return 0;
            }
            if (!(src[p] == '\r' && src[p + 1] == '\n')) {
                free(dst);
                return -1;
            }
            if (!dst) {
                dst = calloc(1, 1);
                if (!dst) return -2;
            }
            *out = dst;
            *out_len = total;
            return 1;
        }

        if (p + chunk + 2 > src_len) {
            free(dst);
            return 0;
        }
        if (!(src[p + chunk] == '\r' && src[p + chunk + 1] == '\n')) {
            free(dst);
            return -1;
        }

        if (total + chunk > SOAPHT_MAX_RESPONSE) {
            free(dst);
            return -1;
        }
        unsigned char *tmp = realloc(dst, total + chunk + 1);
        if (!tmp) {
            free(dst);
            return -2;
        }
        dst = tmp;
        memcpy(dst + total, src + p, chunk);
        total += chunk;
        dst[total] = 0;
        p += chunk + 2;
    }
}

static int read_http_response(struct soap_session *ps, HPMUD_CHANNEL cd,
                              unsigned char **body, size_t *body_len,
                              double deadline)
{
    unsigned char *raw = NULL;
    size_t raw_len = 0, raw_cap = 0, hdr_end = 0;
    int status = SANE_STATUS_IO_ERROR;
    int http_status = 0;

    for (;;) {
        unsigned char tmp[4096];
        int got = 0;
        /* Once a request has been sent, drain its response to the HTTP
         * boundary. Closing mid-DIME leaves M127 data for the next request.
         * Cancellation is returned after draining, under the same deadline. */
        int wait = remaining_timeout(ps, deadline, SOAPHT_READ_POLL_TIMEOUT, 1);
        if (wait < 0) { status = -wait; break; }
        enum HPMUD_RESULT r = hpmud_read_channel(ps->dd, cd, tmp, sizeof(tmp), wait, &got);
        if (r != HPMUD_R_OK && r != HPMUD_R_IO_TIMEOUT) {
            debug_log("read failed: HPMUD=%d", r);
            status = transport_status(r);
            break;
        }
        if (got < 0 || (size_t)got > sizeof(tmp) ||
            raw_len + (size_t)got > SOAPHT_MAX_RESPONSE)
            break;
        /* Continue the same response, retaining partial data. No SOAP request
         * is replayed. The absolute deadline bounds repeated empty polls. */
        if (got == 0) continue;
        if (raw_len + (size_t)got + 1 > raw_cap) {
            size_t nc = raw_cap ? raw_cap * 2 : 8192;
            while (nc < raw_len + (size_t)got + 1) nc *= 2;
            unsigned char *nr = realloc(raw, nc);
            if (!nr) { status = SANE_STATUS_NO_MEM; break; }
            raw = nr;
            raw_cap = nc;
        }
        memcpy(raw + raw_len, tmp, (size_t)got);
        raw_len += (size_t)got;
        raw[raw_len] = 0;

        if (!hdr_end) {
            for (size_t i = 0; i + 3 < raw_len; i++) {
                if (memcmp(raw + i, "\r\n\r\n", 4) == 0) {
                    hdr_end = i + 4;
                    break;
                }
            }
            if (!hdr_end) continue;
            if (hdr_end < 16 || memcmp(raw, "HTTP/1.1 ", 9) != 0 ||
                !isdigit(raw[9]) || !isdigit(raw[10]) || !isdigit(raw[11]) ||
                raw[12] != ' ')
                break;
            http_status = (raw[9] - '0') * 100 + (raw[10] - '0') * 10 + raw[11] - '0';
            debug_log("HTTP status=%d", http_status);
            /* Error responses have a body too. Closing at a 400/409/503
             * header leaves its chunks ahead of the next CancelJob/status
             * response on M127 USB. Drain the same HTTP response first. */
            if (http_status < 200 || http_status >= 300)
                debug_log("HTTP error headers: %.*s", (int)hdr_end, raw);
        }

        unsigned char *decoded = NULL;
        size_t decoded_len = 0;
        int complete = dechunk_try(raw + hdr_end, raw_len - hdr_end, &decoded, &decoded_len);
        if (complete < 0) {
            if (complete == -2) status = SANE_STATUS_NO_MEM;
            break;
        }
        if (!complete) continue;
        const char *xml = (const char *)decoded;
        int fault = strstr(xml, ":Fault") != NULL || strstr(xml, "<Fault") != NULL;
        if (fault || http_status < 200 || http_status >= 300) {
            debug_log("SOAP fault: %.1024s", xml);
            if (http_status == 409 || http_status == 503 ||
                (fault && (strstr(xml, "ScannerBusy") ||
                          strstr(xml, "ServerErrorNotAcceptingJobs") ||
                          /* M127fn HTTP 500 fault observed between scan jobs. */
                          strstr(xml, "The service is temporarily blocked and can't accept new scan job requests."))))
                status = SANE_STATUS_DEVICE_BUSY;
            free(decoded);
            break;
        }
        *body = decoded;
        *body_len = decoded_len;
        status = SANE_STATUS_GOOD;
        break;
    }
    free(raw);
    return status;
}

static int soap_transaction_mode(struct soap_session *ps, const char *xml,
                                 unsigned char **body, size_t *body_len,
                                 int timeout, int cancelling)
{
    HPMUD_CHANNEL cd = -1;
    char header[256], chunk_head[32];
    int xml_len = (int)strlen(xml);
    *body = NULL;
    *body_len = 0;
    double now = monotonic_seconds();
    if (now == 0) return SANE_STATUS_IO_ERROR;
    double deadline = now + timeout;
    if (!cancelling && ps->user_cancel) return SANE_STATUS_CANCELLED;
    enum HPMUD_RESULT r = hpmud_open_channel(ps->dd, SOAPHT_CHANNEL, &cd);
    if (r != HPMUD_R_OK) return transport_status(r);

    int hn = snprintf(header, sizeof(header),
        "POST / HTTP/1.1\r\n"
        "Host: http:0\r\n"
        "User-Agent: gSOAP/2.7\r\n"
        "Content-Type: application/soap+xml; charset=utf-8\r\n"
        "Transfer-Encoding: chunked\r\n"
        "Connection: close\r\n\r\n");
    int cn = snprintf(chunk_head, sizeof(chunk_head), "%x\r\n", xml_len);
    /* Never abandon a partially written SOAP request merely because a signal
     * arrived. Complete this transaction, then cancel the known job. */
    int status = !cancelling && ps->user_cancel ? SANE_STATUS_CANCELLED : SANE_STATUS_GOOD;
    if (!status) status = write_all(ps, cd, header, hn, SOAPHT_TIMEOUT, deadline, 1);
    if (!status) status = write_all(ps, cd, chunk_head, cn, 1, deadline, 1);
    if (!status) status = write_all(ps, cd, xml, xml_len, 1, deadline, 1);
    if (!status) status = write_all(ps, cd, "\r\n0\r\n\r\n", 7, 1, deadline, 1);
    if (!status) status = read_http_response(ps, cd, body, body_len, deadline);
    r = hpmud_close_channel(ps->dd, cd);
    if (!status && r != HPMUD_R_OK) status = transport_status(r);
    /* Preserve CreateScanJob's reply long enough to learn/cancel its JobId. */
    if (!status && !cancelling && ps->user_cancel && !strstr(xml, "CreateScanJobRequest"))
        status = SANE_STATUS_CANCELLED;
    if (status) {
        debug_log("transaction failed: status=%d, request=%s", status,
                  strstr(xml, "CreateScanJobRequest") ? "CreateScanJob" :
                  strstr(xml, "RetrieveImageRequest") ? "RetrieveImage" :
                  cancelling ? "CancelJob" : "GetScannerElements");
        free(*body);
        *body = NULL;
        *body_len = 0;
    }
    return status;
}

static int soap_transaction(struct soap_session *ps, const char *xml,
                            unsigned char **body, size_t *body_len, int timeout)
{
    return soap_transaction_mode(ps, xml, body, body_len, timeout, 0);
}

static char *make_envelope(const char *inner)
{
    size_t n = strlen(xml_prefix) + strlen(inner) + strlen(xml_suffix) + 1;
    char *s = malloc(n);
    if (!s) return NULL;
    snprintf(s, n, "%s%s%s", xml_prefix, inner, xml_suffix);
    return s;
}

static SANE_Fixed thou_to_mm_fixed(int thou)
{
    double mm = ((double)thou / 1000.0) * 25.4;
    return SANE_FIX(mm);
}

static void free_image(struct bb_state *st)
{
    if (!st) return;
    free(st->jpeg);
    st->jpeg = NULL;
    st->jpeg_size = 0;
    st->jpeg_off = 0;
}

static int mm_fixed_to_thou(SANE_Fixed value)
{
    double mm = SANE_UNFIX(value);
    double thou = (mm / 25.4) * 1000.0;

    if (thou < 0.0)
        thou = 0.0;

    return (int)(thou + 0.5);
}

static void source_ranges(const struct source_caps *caps, SANE_Range *x, SANE_Range *y)
{
    *x = (SANE_Range){0, thou_to_mm_fixed(caps->max_width), 0};
    *y = (SANE_Range){0, thou_to_mm_fixed(caps->max_height), 0};
}

static void setup_capabilities(struct soap_session *ps, const struct scanner_caps *caps)
{
    const struct source_caps *first = caps->platen.present ? &caps->platen : &caps->adf;
    int gray = first->gray, color = first->color, n = 0;
    if (caps->platen.present && caps->adf.present) {
        gray &= caps->adf.gray;
        color &= caps->adf.color;
    }
    memset(ps->scanModeList, 0, sizeof(ps->scanModeList));
    memset(ps->scanModeMap, 0, sizeof(ps->scanModeMap));
    if (gray) { ps->scanModeList[n] = SANE_VALUE_SCAN_MODE_GRAY; ps->scanModeMap[n++] = CE_GRAY8; }
    if (color) { ps->scanModeList[n] = SANE_VALUE_SCAN_MODE_COLOR; ps->scanModeMap[n++] = CE_RGB24; }
    memset(ps->inputSourceList, 0, sizeof(ps->inputSourceList));
    memset(ps->inputSourceMap, 0, sizeof(ps->inputSourceMap));
    n = 0;
    if (caps->platen.present) { ps->inputSourceList[n] = "Flatbed"; ps->inputSourceMap[n++] = IS_PLATEN; }
    if (caps->adf.present) { ps->inputSourceList[n] = "ADF"; ps->inputSourceMap[n++] = IS_ADF; }
    /* Duplex capability is recorded but not advertised until implemented. */
    memcpy(ps->platen_resolutionList, caps->platen.resolutions, sizeof(ps->platen_resolutionList));
    memcpy(ps->adf_resolutionList, caps->adf.resolutions, sizeof(ps->adf_resolutionList));
    memcpy(ps->resolutionList, first->resolutions, sizeof(ps->resolutionList));
    ps->platen_min_width = thou_to_mm_fixed(caps->platen.min_width);
    ps->platen_min_height = thou_to_mm_fixed(caps->platen.min_height);
    ps->adf_min_width = thou_to_mm_fixed(caps->adf.min_width);
    ps->adf_min_height = thou_to_mm_fixed(caps->adf.min_height);
    source_ranges(&caps->platen, &ps->platen_tlxRange, &ps->platen_tlyRange);
    ps->platen_brxRange = ps->platen_tlxRange;
    ps->platen_bryRange = ps->platen_tlyRange;
    source_ranges(&caps->adf, &ps->adf_tlxRange, &ps->adf_tlyRange);
    ps->adf_brxRange = ps->adf_tlxRange;
    ps->adf_bryRange = ps->adf_tlyRange;
    ps->jpegQualityRange = (SANE_Range){0, 100, 0};
}

static const struct source_caps *selected_caps(struct soap_session *ps)
{
    struct bb_state *st = ps->bb_session;
    if (!st) return NULL;
    if (ps->currentInputSource == IS_PLATEN && st->caps.platen.present) return &st->caps.platen;
    if (ps->currentInputSource == IS_ADF && st->caps.adf.present) return &st->caps.adf;
    return NULL;
}

static int source_dpi(const struct source_caps *caps, int dpi)
{
    for (int i = 1; i <= caps->resolutions[0]; i++)
        if (caps->resolutions[i] == dpi) return dpi;
    for (int i = 1; i <= caps->resolutions[0]; i++)
        if (caps->resolutions[i] == 300) return 300;
    return caps->resolutions[1];
}

static int cancel_job(struct soap_session *ps, struct bb_state *st);
int bb_end_scan(struct soap_session *ps, int io_error);

__attribute__((visibility("default")))
int bb_open(struct soap_session *ps)
{
    if (!ps || ps->bb_session) return SANE_STATUS_IO_ERROR;
    struct bb_state *st = calloc(1, sizeof(*st));
    if (!st) return SANE_STATUS_NO_MEM;
    ps->bb_session = st;

    /* Probe the real scanner once, matching the proprietary plugin's flow. */
    char *xml = make_envelope(
        "<wscn:GetScannerElements></wscn:GetScannerElements>");
    if (!xml) {
        free(st);
        ps->bb_session = NULL;
        return SANE_STATUS_NO_MEM;
    }
    unsigned char *resp = NULL;
    size_t resp_len = 0;
    int rc = soap_transaction(ps, xml, &resp, &resp_len, SOAPHT_TIMEOUT);
    free(xml);
    if (rc) {
        free(resp);
        free(st);
        ps->bb_session = NULL;
        return rc;
    }

    rc = parse_capabilities((char *)resp, resp_len, &st->caps);
    free(resp);
    if (rc) {
        free(st);
        ps->bb_session = NULL;
        return rc;
    }
    setup_capabilities(ps, &st->caps);
    return 0;
}

__attribute__((visibility("default")))
int bb_close(struct soap_session *ps)
{
    if (!ps) return 0;
    struct bb_state *st = (struct bb_state *)ps->bb_session;
    if (st) {
        bb_end_scan(ps, 0);
        free_image(st);
        free(st);
    }
    ps->bb_session = NULL;
    return 0;
}

__attribute__((visibility("default")))
int bb_get_parameters(struct soap_session *ps, SANE_Parameters *pp,
                      int scan_started)
{
    (void)scan_started;
    if (!ps || !pp) return SANE_STATUS_IO_ERROR;

    struct bb_state *st = (struct bb_state *)ps->bb_session;

    memset(pp, 0, sizeof(*pp));

    const int is_rgb = (ps->currentScanMode == CE_RGB24);
    int dpi = ps->currentResolution;

    const struct source_caps *caps = selected_caps(ps);
    if (!caps) return SANE_STATUS_INVAL;
    dpi = source_dpi(caps, dpi);

    pp->format = is_rgb ? SANE_FRAME_RGB : SANE_FRAME_GRAY;
    pp->last_frame = SANE_TRUE;
    pp->depth = 8;

    if (st && st->pixels_per_line > 0 && st->lines > 0) {
        pp->pixels_per_line = st->pixels_per_line;
        pp->lines = st->lines;

        /*
         * IMPORTANT: Do not trust MediaFrontImageInfo/BytesPerLine for JFIF.
         *
         * On the M127fn the SOAP response reports inconsistent BytesPerLine
         * values for compressed JPEG jobs.  Observed examples from the
         * proprietary Linux plugin:
         *
         *   150 dpi Gray: PixelsPerLine=1274, BytesPerLine=7647
         *   300 dpi RGB : PixelsPerLine=2549, BytesPerLine=5099
         *   600 dpi Gray: PixelsPerLine=5099, BytesPerLine=1274
         *
         * The actual reconstructed JPEGs are respectively:
         *   1274x1753 L, 2549x3506 RGB, 5099x7013 L.
         *
         * HPLIP's image processor needs the uncompressed output row size
         * for its best-guess traits, not that unreliable SOAP field.
         */
        pp->bytes_per_line =
            st->pixels_per_line * (is_rgb ? 3 : 1);
    } else {
        int width = mm_fixed_to_thou(ps->effectiveBrx - ps->effectiveTlx);
        int height = mm_fixed_to_thou(ps->effectiveBry - ps->effectiveTly);
        int left = mm_fixed_to_thou(ps->effectiveTlx), top = mm_fixed_to_thou(ps->effectiveTly);
        if (left >= caps->max_width || top >= caps->max_height) return SANE_STATUS_INVAL;
        if (width <= 0) width = caps->max_width;
        if (height <= 0) height = ps->currentInputSource == IS_ADF && caps->max_height > 11689 ? 11689 : caps->max_height;
        if (width > caps->max_width - left) width = caps->max_width - left;
        if (height > caps->max_height - top) height = caps->max_height - top;
        pp->pixels_per_line = (width * dpi) / 1000;
        pp->lines = (height * dpi) / 1000;
        pp->bytes_per_line =
            pp->pixels_per_line * (is_rgb ? 3 : 1);
    }

    return 0;
}

static int query_paper_in_adf(struct soap_session *ps)
{
    char *xml = make_envelope(
        "<wscn:GetScannerElements></wscn:GetScannerElements>");

    if (!xml)
        return -SANE_STATUS_NO_MEM;

    unsigned char *resp = NULL;
    size_t resp_len = 0;

    int rc = soap_transaction(
        ps,
        xml,
        &resp,
        &resp_len,
        SOAPHT_TIMEOUT);

    free(xml);

    if (rc || !resp) {
        free(resp);
        return -(rc ? rc : SANE_STATUS_IO_ERROR);
    }

    if (scanner_media_jam((char *)resp)) {
        debug_log("ScannerStateReason=MediaJam");
        free(resp);
        return -SANE_STATUS_JAMMED;
    }
    int result = xml_boolean((char *)resp, "PaperInADF");
    debug_log("PaperInADF=%d", result);
    if (result < 0) result = -SANE_STATUS_IO_ERROR;

    free(resp);
    return result;
}

__attribute__((visibility("default")))
int bb_is_paper_in_adf(struct soap_session *ps)
{
    if (!ps || !ps->bb_session)
        return -SANE_STATUS_IO_ERROR;

    struct bb_state *st =
        (struct bb_state *)ps->bb_session;

    /*
     * First page:
     * no active scan job yet, so read the feeder sensor immediately.
     */
    if (!st->job_active)
        return query_paper_in_adf(ps);

    /*
     * Between ADF pages the M127fn may briefly continue to report
     * PaperInADF=true after the last sheet has already been ejected.
     *
     * Poll for a short settling period.  If the sensor becomes false
     * at any point, the batch is finished.  If it remains true for the
     * entire interval, another page is really present.
     */
    for (int i = 0; i < 6; i++) {
        int result = query_paper_in_adf(ps);

        if (result <= 0)
            return result;

        if (i < 5)
            usleep(250000);
    }

    return 1;
}


__attribute__((visibility("default")))
int bb_start_scan(struct soap_session *ps)
{
    if (!ps || !ps->bb_session)
        return SANE_STATUS_IO_ERROR;

    struct bb_state *st = (struct bb_state *)ps->bb_session;
    const int is_adf =
        (ps->currentInputSource == IS_ADF ||
         ps->currentInputSource == IS_ADF_DUPLEX);

    const struct source_caps *caps = selected_caps(ps);
    if (!caps) return SANE_STATUS_INVAL;
    if (ps->user_cancel) return SANE_STATUS_CANCELLED;

    /*
     * The M127fn supports simplex ADF only. Keep the same SOAPHT JobId
     * across ADF pages; each subsequent RetrieveImage returns the next page.
     */
    /* Retain one JobId and its geometry for successful ADF pages only. */
    free_image(st);
    ps->cnt = ps->index = 0;
    if (st->cancel_pending || (st->job_active && !(is_adf && st->job_is_adf))) {
        int rc = cancel_job(ps, st);
        if (rc) return rc;
    }
    if (is_adf && st->job_active && st->job_id > 0)
        return SANE_STATUS_GOOD;

    st->job_id = 0;
    st->job_active = 0;
    st->pixels_per_line = 0;
    st->lines = 0;
    st->bytes_per_line = 0;

    if (ps->currentCompression != SF_JFIF)
        ps->currentCompression = SF_JFIF;

    ps->currentResolution = source_dpi(caps, ps->currentResolution);
    if ((ps->currentScanMode == CE_GRAY8 && !caps->gray) ||
        (ps->currentScanMode == CE_RGB24 && !caps->color)) return SANE_STATUS_INVAL;

    const char *color_processing;
    switch (ps->currentScanMode) {
        case CE_RGB24:
            color_processing = "RGB24";
            break;
        case CE_GRAY8:
            color_processing = "GrayScale8";
            break;
        default:
            return SANE_STATUS_IO_ERROR;
    }

    const char *source_name = is_adf ? "ADF" : "Platen";

    int x_offset = mm_fixed_to_thou(ps->effectiveTlx);
    int y_offset = mm_fixed_to_thou(ps->effectiveTly);

    int media_width =
        mm_fixed_to_thou(ps->effectiveBrx - ps->effectiveTlx);

    int media_height =
        mm_fixed_to_thou(ps->effectiveBry - ps->effectiveTly);

    /* Uninitialized callers use this source's bounds; explicit geometry is
     * preserved, including Legal. Never substitute another model's limits. */
    if (media_width <= 0) media_width = caps->max_width;
    if (media_height <= 0) media_height = is_adf && caps->max_height > 11689 ? 11689 : caps->max_height;
    if (x_offset >= caps->max_width || y_offset >= caps->max_height) return SANE_STATUS_INVAL;
    if (media_width > caps->max_width - x_offset) media_width = caps->max_width - x_offset;
    if (media_height > caps->max_height - y_offset) media_height = caps->max_height - y_offset;

    char inner[4096];
    int n = snprintf(
        inner, sizeof(inner),
        "<wscn:CreateScanJobRequest>"
        "<ScanIdentifier></ScanIdentifier>"
        "<ScanTicket><JobDescription></JobDescription>"
        "<DocumentParameters>"
        "<Format>jfif</Format>"
        "<CompressionQualityFactor>0</CompressionQualityFactor>"
        "<ImagesToTransfer>0</ImagesToTransfer>"
        "<InputSource>%s</InputSource>"
        "<ContentType>Auto</ContentType>"
        "<InputSize><InputMediaSize>"
        "<Width>%d</Width><Height>%d</Height>"
        "</InputMediaSize>"
        "<DocumentSizeAutoDetect>false</DocumentSizeAutoDetect>"
        "</InputSize>"
        "<Exposure><AutoExposure>false</AutoExposure>"
        "<ExposureSettings>"
        "<Contrast>%d</Contrast>"
        "<Brightness>%d</Brightness>"
        "</ExposureSettings></Exposure>"
        "<MediaSides><MediaFront>"
        "<ScanRegion>"
        "<ScanRegionXOffset>%d</ScanRegionXOffset>"
        "<ScanRegionYOffset>%d</ScanRegionYOffset>"
        "<ScanRegionWidth>%d</ScanRegionWidth>"
        "<ScanRegionHeight>%d</ScanRegionHeight>"
        "</ScanRegion>"
        "<ColorProcessing>%s</ColorProcessing>"
        "<Resolution><Width>%d</Width><Height>%d</Height></Resolution>"
        "</MediaFront></MediaSides>"
        "</DocumentParameters>"
        "<RetrieveImageTimeout>300</RetrieveImageTimeout>"
        "<ScanManufacturingParameters>"
        "<DisableImageProcessing>false</DisableImageProcessing>"
        "</ScanManufacturingParameters>"
        "</ScanTicket>"
        "</wscn:CreateScanJobRequest>",
        source_name,
        media_width,
        media_height,
        ps->currentContrast,
        ps->currentBrightness,
        x_offset,
        y_offset,
        media_width,
        media_height,
        color_processing,
        ps->currentResolution,
        ps->currentResolution);

    if (n < 0 || (size_t)n >= sizeof(inner))
        return SANE_STATUS_IO_ERROR;

    char *xml = make_envelope(inner);
    if (!xml)
        return SANE_STATUS_NO_MEM;

    unsigned char *resp = NULL;
    size_t resp_len = 0;
    int rc = soap_transaction(
        ps, xml, &resp, &resp_len, SOAPHT_TIMEOUT);

    free(xml);

    if (rc || !resp) {
        free(resp);
        return rc ? rc : SANE_STATUS_IO_ERROR;
    }

    if (tag_int((char *)resp, "JobId", &st->job_id)) {
        debug_log("CreateScanJob returned no valid JobId: %.1024s", resp);
        free(resp);
        return SANE_STATUS_IO_ERROR;
    }
    /* From here on we own a real job, even if its metadata is malformed. */
    st->job_active = 1;
    st->job_is_adf = is_adf;
    if (ps->user_cancel) {
        free(resp);
        bb_end_scan(ps, 0);
        return SANE_STATUS_CANCELLED;
    }
    if (tag_int((char *)resp, "PixelsPerLine", &st->pixels_per_line) ||
        tag_int((char *)resp, "NumberOfLines", &st->lines) ||
        tag_int((char *)resp, "BytesPerLine", &st->bytes_per_line) ||
        st->pixels_per_line > INT_MAX / 3) {
        debug_log("invalid image metadata for JobId=%d", st->job_id);
        free(resp);
        bb_end_scan(ps, 1);
        return SANE_STATUS_IO_ERROR;
    }

    free(resp);
    st->job_active = 1;
    return 0;
}

static uint16_t be16(const unsigned char *p)
{
    return ((uint16_t)p[0] << 8) | p[1];
}

static uint32_t be32(const unsigned char *p)
{
    return ((uint32_t)p[0] << 24) |
           ((uint32_t)p[1] << 16) |
           ((uint32_t)p[2] << 8)  |
           (uint32_t)p[3];
}

static size_t pad4(size_t n)
{
    return (n + 3u) & ~3u;
}

static int extract_dime_jpeg(const unsigned char *body,
                             size_t body_len,
                             unsigned char **jpeg,
                             size_t *jpeg_len)
{
    size_t pos = 0;
    unsigned char *out = NULL;
    size_t out_len = 0;
    int image_started = 0;
    int status = SANE_STATUS_IO_ERROR;
    const char *reason = "no complete JFIF record";

    while (pos + 12 <= body_len) {
        const unsigned char *h = body + pos;

        /*
         * DIME 12-byte header:
         *   byte 0    version + MB/ME/CF
         *   byte 1    type format
         *   byte 2-3  options length
         *   byte 4-5  ID length
         *   byte 6-7  type length
         *   byte 8-11 data length
         */
        unsigned char flags = h[0];
        int cf = (flags & 0x01) != 0;
        if ((flags >> 3) != 1 || (cf && (flags & 0x02))) {
            reason = "invalid DIME flags";
            goto fail;
        }

        uint16_t options_len = be16(h + 2);
        uint16_t id_len = be16(h + 4);
        uint16_t type_len = be16(h + 6);
        uint32_t data_len = be32(h + 8);

        pos += 12;

        size_t options_pos = pos;
        size_t id_pos = options_pos + pad4(options_len);
        size_t type_pos = id_pos + pad4(id_len);
        size_t data_pos = type_pos + pad4(type_len);
        size_t next_pos = data_pos + pad4(data_len);

        if (data_len > SOAPHT_MAX_RESPONSE || next_pos > body_len) {
            reason = "DIME record exceeds response";
            goto fail;
        }
        debug_log("DIME record: flags=0x%02x type=%.*s data=%u", flags,
                  type_len > 80 ? 80 : (int)type_len, body + type_pos, data_len);

        if (!image_started &&
            type_len == strlen("image/jfif") &&
            memcmp(body + type_pos, "image/jfif", type_len) == 0) {
            image_started = 1;
        }

        if (image_started && data_len > 0) {
            if (out_len + data_len > SOAPHT_MAX_RESPONSE) {
                reason = "JPEG exceeds response limit";
                goto fail;
            }
            unsigned char *tmp = realloc(out, out_len + data_len);
            if (!tmp) {
                status = SANE_STATUS_NO_MEM;
                reason = "JPEG allocation failed";
                goto fail;
            }

            out = tmp;
            memcpy(out + out_len, body + data_pos, data_len);
            out_len += data_len;
        }

        if (image_started && !cf) {
            if (out_len < 4 ||
                out[0] != 0xff ||
                out[1] != 0xd8 ||
                out[out_len - 2] != 0xff ||
                out[out_len - 1] != 0xd9) {
                reason = "missing JPEG SOI/EOI";
                if (out_len >= 4)
                    debug_log("JPEG boundary: first=%02x%02x last=%02x%02x",
                              out[0], out[1], out[out_len - 2], out[out_len - 1]);
                goto fail;
            }

            *jpeg = out;
            *jpeg_len = out_len;
            return 0;
        }

        pos = next_pos;
    }

fail:
    debug_log("DIME extraction failed: %s (body=%zu offset=%zu jpeg=%zu)",
              reason, body_len, pos, out_len);
    free(out);
    return status;
}

static int fetch_jpeg(struct soap_session *ps, struct bb_state *st)
{
    char inner[768];
    int n = snprintf(
        inner, sizeof(inner),
        "<wscn:RetrieveImageRequest>"
        "<JobId>%d</JobId><JobToken></JobToken>"
        "<DocumentDescription></DocumentDescription>"
        "</wscn:RetrieveImageRequest>",
        st->job_id);

    if (n < 0 || (size_t)n >= sizeof(inner))
        return SANE_STATUS_IO_ERROR;

    char *xml = make_envelope(inner);
    if (!xml)
        return SANE_STATUS_NO_MEM;

    unsigned char *body = NULL;
    size_t body_len = 0;
    int rc = soap_transaction(
        ps, xml, &body, &body_len, SOAPHT_IMAGE_TIMEOUT);

    free(xml);

    if (rc) {
        free(body);
        return rc;
    }

    unsigned char *jpeg = NULL;
    size_t jpeg_len = 0;

    rc = extract_dime_jpeg(body, body_len, &jpeg, &jpeg_len);
    if (rc) {
        free(body);
        /* A jam can truncate an otherwise complete HTTP/DIME response.
         * Query status once; never retry image acquisition or mask an unknown
         * malformed image as an empty feeder. Preserve other original errors. */
        if (rc == SANE_STATUS_IO_ERROR && st->job_is_adf &&
            query_paper_in_adf(ps) == -SANE_STATUS_JAMMED)
            return SANE_STATUS_JAMMED;
        return rc;
    }

    free(body);

    st->jpeg = jpeg;
    st->jpeg_size = jpeg_len;
    st->jpeg_off = 0;
    return 0;
}

__attribute__((visibility("default")))
int bb_get_image_data(struct soap_session *ps, int max_length)
{
    if (!ps || !ps->bb_session) return SANE_STATUS_IO_ERROR;
    struct bb_state *st = (struct bb_state *)ps->bb_session;

    /* soapht.c may call us while unconsumed input remains. */
    if (ps->user_cancel) return SANE_STATUS_CANCELLED;
    if (ps->cnt > 0)
        return 0;

    if (!st->jpeg) {
        if (!st->job_active || st->cancel_pending)
            return SANE_STATUS_IO_ERROR;
        int rc = fetch_jpeg(ps, st);
        if (rc) {
            bb_end_scan(ps, 1);
            return rc;
        }
    }

    if (st->jpeg_off >= st->jpeg_size) {
        ps->index = 0;
        ps->cnt = 0;
        return 0;
    }

    size_t n = st->jpeg_size - st->jpeg_off;
    if (n > sizeof(ps->buf)) n = sizeof(ps->buf);
    if (max_length > 0 && n > (size_t)max_length) n = (size_t)max_length;

    memcpy(ps->buf, st->jpeg + st->jpeg_off, n);
    st->jpeg_off += n;
    ps->index = 0;
    ps->cnt = (int)n;
    return 0;
}

__attribute__((visibility("default")))
int bb_end_page(struct soap_session *ps, int io_error)
{
    if (io_error)
        return bb_end_scan(ps, 1);

    if (!ps || !ps->bb_session)
        return 0;

    struct bb_state *st = (struct bb_state *)ps->bb_session;

    /*
     * Release only the current page image. For an ADF batch the SOAPHT
     * JobId remains active so the next sane_start() can retrieve the next
     * page from the same scan job.
     */
    free_image(st);
    ps->index = 0;
    ps->cnt = 0;
    return 0;
}

static int cancel_job(struct soap_session *ps, struct bb_state *st)
{
    if (!st || !st->job_active || st->job_id <= 0)
        return 0;

    st->cancel_pending = 1;
    char inner[768];
    snprintf(inner, sizeof(inner),
        "<wscn:CancelJobRequest>"
        "<JobId>%d</JobId><JobToken></JobToken>"
        "<DocumentDescription></DocumentDescription>"
        "</wscn:CancelJobRequest>", st->job_id);
    char *xml = make_envelope(inner);
    if (!xml) return SANE_STATUS_NO_MEM;

    unsigned char *resp = NULL;
    size_t resp_len = 0;
    int rc = soap_transaction_mode(ps, xml, &resp, &resp_len, SOAPHT_CANCEL_TIMEOUT, 1);
    free(xml);
    free(resp);
    if (!rc) {
        st->job_active = 0;
        st->job_id = 0;
        st->cancel_pending = 0;
    }
    return rc;
}

__attribute__((visibility("default")))
int bb_end_scan(struct soap_session *ps, int io_error)
{
    (void)io_error;
    if (!ps || !ps->bb_session) return 0;
    struct bb_state *st = (struct bb_state *)ps->bb_session;
    int rc = cancel_job(ps, st);
    free_image(st);
    st->pixels_per_line = st->lines = st->bytes_per_line = 0;
    ps->index = 0;
    ps->cnt = 0;
    return rc;
}
