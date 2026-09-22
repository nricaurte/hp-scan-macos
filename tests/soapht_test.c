/* Deterministic transport/parser/lifecycle regression, no scanner required. */
#include <assert.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static int alloc_fail;
static int allocations_before_failure = -1;
static int allocation_fails(void)
{
    if (alloc_fail || allocations_before_failure == 0) return 1;
    if (allocations_before_failure > 0) allocations_before_failure--;
    return 0;
}
static double fake_seconds = 1000;
static int sleeps;
static int test_clock_gettime(clockid_t id, struct timespec *now)
{
    assert(id == CLOCK_MONOTONIC);
    now->tv_sec = (time_t)fake_seconds;
    now->tv_nsec = (long)((fake_seconds - now->tv_sec) * 1e9);
    return 0;
}
static int test_usleep(unsigned int us) { assert(us == 250000); sleeps++; return 0; }
static void *test_malloc(size_t n) { return allocation_fails() ? NULL : malloc(n); }
static void *test_calloc(size_t n, size_t s) { return allocation_fails() ? NULL : calloc(n, s); }
static void *test_realloc(void *p, size_t n) { return allocation_fails() ? NULL : realloc(p, n); }
#define malloc test_malloc
#define calloc test_calloc
#define realloc test_realloc
#define clock_gettime test_clock_gettime
#define usleep test_usleep
#include "../soapht-macos/bb_soapht_macos.c"
#undef malloc
#undef calloc
#undef realloc
#undef clock_gettime
#undef usleep

#include "../tools/soapht_probe_recovery.h"

static char capabilities[8192];
static void fixture(const char *name, char *out, size_t capacity)
{
    const char *dir = getenv("SOAPHT_FIXTURE_DIR");
    assert(dir);
    char path[4096];
    snprintf(path, sizeof(path), "%s/%s", dir, name);
    FILE *f = fopen(path, "rb");
    assert(f);
    size_t n = fread(out, 1, capacity - 1, f);
    assert(n && feof(f));
    out[n] = 0;
    fclose(f);
}
static const unsigned char *reply;
static size_t reply_len, reply_off, read_fragment = 4096;
static int opens, closes, writes, reads, fail_write, empty_read;
static enum HPMUD_RESULT open_result, read_result;
static char request[16384];
static size_t request_len;
static double read_seconds;
static struct soap_session *cancel_on_read;
static int oversized_read;
static int empty_polls, partial_timeout;

enum HPMUD_RESULT hpmud_open_channel(HPMUD_DEVICE d, const char *name, HPMUD_CHANNEL *c)
{
    (void)d;
    assert(strcmp(name, SOAPHT_CHANNEL) == 0);
    opens++;
    reply_off = 0;
    *c = 1;
    return open_result;
}
enum HPMUD_RESULT hpmud_close_channel(HPMUD_DEVICE d, HPMUD_CHANNEL c)
{ (void)d; (void)c; closes++; return HPMUD_R_OK; }
enum HPMUD_RESULT hpmud_write_channel(HPMUD_DEVICE d, HPMUD_CHANNEL c,
                                     const void *buf, int n, int timeout, int *wrote)
{
    (void)d; (void)c;
    assert(timeout > 0);
    writes++;
    if (fail_write) { *wrote = 0; return HPMUD_R_IO_ERROR; }
    /* Exercise short successful writes on every request. */
    if (n > 29) n = 29;
    assert(request_len + (size_t)n < sizeof(request));
    memcpy(request + request_len, buf, n);
    request_len += n;
    request[request_len] = 0;
    *wrote = n;
    return HPMUD_R_OK;
}
enum HPMUD_RESULT hpmud_read_channel(HPMUD_DEVICE d, HPMUD_CHANNEL c,
                                    void *buf, int n, int timeout, int *got)
{
    (void)d; (void)c;
    assert(timeout > 0);
    reads++;
    fake_seconds += read_seconds;
    if (cancel_on_read) cancel_on_read->user_cancel = 1;
    if (oversized_read) { *got = n + 1; return HPMUD_R_OK; }
    size_t take = reply_len - reply_off;
    if (take > (size_t)n) take = n;
    if (take > read_fragment) take = read_fragment;
    if (empty_read || (read_result != HPMUD_R_OK && !partial_timeout)) take = 0;
    if (empty_polls > 0) { empty_polls--; take = 0; }
    if (!take || read_result == HPMUD_R_IO_TIMEOUT) fake_seconds += timeout;
    if (take) memcpy(buf, reply + reply_off, take);
    reply_off += take;
    *got = (int)take;
    return read_result;
}

static void reset(const char *response)
{
    reply = (const unsigned char *)response;
    reply_len = strlen(response);
    reply_off = request_len = 0;
    request[0] = 0;
    opens = closes = writes = reads = fail_write = empty_read = 0;
    open_result = read_result = HPMUD_R_OK;
    read_fragment = 4096;
    read_seconds = 0;
    fake_seconds = 1000;
    cancel_on_read = NULL;
    oversized_read = sleeps = 0;
    empty_polls = partial_timeout = 0;
}

static void respond(const char *body)
{
    static char wire[8192];
    snprintf(wire, sizeof(wire), "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%zx\r\n%s\r\n0\r\n\r\n", strlen(body), body);
    reset(wire);
}

static void parser_tests(void)
{
    const char *valid = "3;foo=bar\r\nabc\r\n2\r\nde\r\n0\r\n\r\n";
    unsigned char *out = NULL;
    size_t len = 0;
    for (size_t n = 0; n < strlen(valid); n++)
        assert(dechunk_try((const unsigned char *)valid, n, &out, &len) == 0);
    assert(dechunk_try((const unsigned char *)valid, strlen(valid), &out, &len) == 1);
    assert(len == 5 && memcmp(out, "abcde", 6) == 0);
    free(out);
    const char *bad[] = { "-1\r\n", "xyz\r\n", "1x\r\n", "+1\r\n", " 1\r\n", "1000001\r\n", "ffffffffffffffffffff\r\n", "1\r\naXX", "0\r\nXX" };
    for (size_t i = 0; i < sizeof(bad)/sizeof(bad[0]); i++)
        assert(dechunk_try((const unsigned char *)bad[i], strlen(bad[i]), &out, &len) == -1);
    assert(dechunk_try((const unsigned char *)"0\r\n\r\n", 5, &out, &len) == 1);
    assert(len == 0 && out && out[0] == 0);
    free(out);
    alloc_fail = 1;
    assert(dechunk_try((const unsigned char *)valid, strlen(valid), &out, &len) == -2);
    alloc_fail = 0;
    int value;
    assert(tag_int("<JobId>42</JobId>", "JobId", &value) == 0 && value == 42);
    const char *bad_int[] = { "0", "-1", "abc", "12junk", "2147483648", "999999999999999999999999" };
    for (size_t i = 0; i < sizeof(bad_int)/sizeof(bad_int[0]); i++) {
        char xml[128];
        snprintf(xml, sizeof(xml), "<JobId>%s</JobId>", bad_int[i]);
        assert(tag_int(xml, "JobId", &value));
    }
}

static void transport_tests(void)
{
    struct soap_session ps = {0};
    unsigned char *body = NULL;
    size_t len = 0;
    respond("<ok/>");
    read_fragment = 1;
    assert(soap_transaction(&ps, "<request/>", &body, &len, 45) == 0);
    assert(len == 5 && strcmp((char *)body, "<ok/>") == 0);
    assert(opens == 1 && closes == 1 && writes > 4);
    free(body);
    reset("");
    fail_write = 1;
    assert(soap_transaction(&ps, "<request/>", &body, &len, 45));
    assert(writes == 1 && reads == 0 && closes == 1 && body == NULL);
    reset("");
    empty_read = 1;
    assert(soap_transaction(&ps, "<request/>", &body, &len, 45));
    assert(reads == 45 && closes == 1);
    respond("<bad/>");
    assert(bb_open(&ps));
    assert(ps.bb_session == NULL && opens == closes);
    respond(capabilities);
    assert(bb_open(&ps) == 0);
    assert(ps.bb_session && ps.adf_resolutionList[0] == 2);
    assert(bb_close(&ps) == 0 && ps.bb_session == NULL);
    allocations_before_failure = 1; /* state succeeds, envelope allocation fails */
    assert(bb_open(&ps) == SANE_STATUS_NO_MEM && ps.bb_session == NULL);
    allocations_before_failure = -1;

    reset("");
    open_result = HPMUD_R_DEVICE_BUSY;
    assert(bb_open(&ps) == SANE_STATUS_DEVICE_BUSY);
    assert(ps.bb_session == NULL && closes == 0);
    respond("<SOAP:Fault><Value>wscn:ScannerBusy</Value></SOAP:Fault>");
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_DEVICE_BUSY);
    assert(body == NULL && closes == 1);
    respond("<SOAP-ENV:Fault><SOAP-ENV:Reason><SOAP-ENV:Text>The service is temporarily blocked and can't accept new scan job requests.</SOAP-ENV:Text></SOAP-ENV:Reason></SOAP-ENV:Fault>");
    /* Match the M127fn's actual HTTP 500 busy fault, including its reason. */
    ((unsigned char *)reply)[9] = '5';
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_DEVICE_BUSY);
    reset("HTTP/1.1 503 Service Unavailable\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n");
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_DEVICE_BUSY);
    /* Error headers and body in separate USB reads must not leave chunks
     * queued for the next CancelJob/GetScannerElements transaction. */
    for (int code = 0; code < 3; code++) {
        const char *codes[] = {"400", "409", "503"};
        respond("<SOAP:Fault><Value>RequestFailed</Value></SOAP:Fault>");
        memcpy((unsigned char *)reply + 9, codes[code], 3);
        read_fragment = 7;
        int want = code == 0 ? SANE_STATUS_IO_ERROR : SANE_STATUS_DEVICE_BUSY;
        assert(soap_transaction(&ps, "x", &body, &len, 45) == want);
        assert(reply_off == reply_len && body == NULL && opens == 1 && closes == 1);
    }
    respond("<SOAP:Fault><Value>OtherError</Value></SOAP:Fault>");
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_IO_ERROR);
    respond("<ok/>");
    oversized_read = 1;
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_IO_ERROR);
    respond("<ok/>");
    read_result = HPMUD_R_IO_TIMEOUT;
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_IO_ERROR);
    assert(reads == 45 && closes == 1);
    respond("<ok/>");
    empty_polls = 2;
    assert(soap_transaction(&ps, "x", &body, &len, 45) == 0);
    assert(reads == 3 && opens == 1 && len == 5);
    free(body);
    respond("<ok/>");
    read_result = HPMUD_R_IO_TIMEOUT;
    partial_timeout = 1;
    read_fragment = 32;
    assert(soap_transaction(&ps, "x", &body, &len, 45) == 0);
    assert(opens == 1 && len == 5 && strcmp((char *)body, "<ok/>") == 0);
    free(body);
    respond("<ok/>");
    read_result = HPMUD_R_IO_ERROR; /* disconnected device must not be polled */
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_IO_ERROR);
    assert(reads == 1 && closes == 1);
    respond("<ok/>");
    read_fragment = 1;
    read_seconds = 20;
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_IO_ERROR);
    assert(reads == 3 && closes == 1); /* total deadline, not a fresh 45s each read */
    respond("<ok/>");
    cancel_on_read = &ps;
    read_fragment = 1; /* Cancellation must consume the entire response. */
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_CANCELLED);
    assert(body == NULL && closes == 1 && reply_off == reply_len);
    reset("");
    assert(soap_transaction(&ps, "x", &body, &len, 45) == SANE_STATUS_CANCELLED);
    assert(opens == 0);
    ps.user_cancel = 0;
}

static const char *job_reply = "<Response><JobId>42</JobId><PixelsPerLine>2549</PixelsPerLine>"
                               "<NumberOfLines>3506</NumberOfLines><BytesPerLine>5099</BytesPerLine></Response>";

static void lifecycle_tests(void)
{
    struct soap_session ps = {0};
    respond(capabilities);
    assert(bb_open(&ps) == 0);
    struct bb_state *st = ps.bb_session;
    ps.currentInputSource = IS_ADF;
    ps.currentScanMode = CE_RGB24;
    ps.currentResolution = 300;
    ps.effectiveTlx = SANE_FIX(10);
    ps.effectiveTly = SANE_FIX(20);
    ps.effectiveBrx = SANE_FIX(110);
    ps.effectiveBry = SANE_FIX(170);
    respond(job_reply);
    cancel_on_read = &ps;
    assert(bb_start_scan(&ps) == SANE_STATUS_CANCELLED);
    assert(strstr(request, "CreateScanJobRequest") && strstr(request, "CancelJobRequest"));
    assert(!st->job_active && !st->job_id);
    ps.user_cancel = 0;
    respond(job_reply);
    assert(bb_start_scan(&ps) == 0);
    assert(strstr(request, "<InputSource>ADF</InputSource>"));
    assert(strstr(request, "<ScanRegionXOffset>394</ScanRegionXOffset>"));
    assert(strstr(request, "<ScanRegionYOffset>787</ScanRegionYOffset>"));
    assert(strstr(request, "<ScanRegionWidth>3937</ScanRegionWidth>"));
    assert(st->job_id == 42 && st->job_active);
    SANE_Parameters pp;
    assert(bb_get_parameters(&ps, &pp, 1) == 0 && pp.bytes_per_line == 2549 * 3);
    respond("<ok/>");
    st->jpeg = malloc(4);
    memcpy(st->jpeg, "\xff\xd8\xff\xd9", 4);
    st->jpeg_size = 4;
    assert(bb_get_image_data(&ps, 2) == 0 && ps.cnt == 2);
    assert(bb_get_image_data(&ps, 2) == 0 && st->jpeg_off == 2); /* unconsumed data */
    ps.cnt = 0;
    assert(bb_get_image_data(&ps, 2) == 0 && st->jpeg_off == 4);
    ps.cnt = 0;
    assert(bb_get_image_data(&ps, 2) == 0 && ps.cnt == 0);
    assert(bb_end_page(&ps, 0) == 0 && !st->jpeg && st->job_active);
    assert(bb_start_scan(&ps) == 0 && opens == 0 && st->pixels_per_line == 2549);
    respond("<PaperInADF>true</PaperInADF>");
    assert(bb_is_paper_in_adf(&ps) == 1 && opens == 6 && sleeps == 5);
    respond("<PaperInADF>false</PaperInADF>");
    assert(bb_is_paper_in_adf(&ps) == 0 && opens == 1 && sleeps == 0);
    respond("<Status xmlns:s='urn:test'><s:ScannerStateReason> MediaJam </s:ScannerStateReason><s:PaperInADF>false</s:PaperInADF></Status>");
    assert(bb_is_paper_in_adf(&ps) == -SANE_STATUS_JAMMED && opens == 1);
    respond("<Status><ScannerStateReason>MediaJam</ScannerStateReason><PaperInADF>true</PaperInADF></Status>");
    assert(bb_get_image_data(&ps, 0) == SANE_STATUS_JAMMED);
    assert(strstr(request, "GetScannerElements") && strstr(request, "CancelJobRequest"));
    assert(!st->job_active && !st->jpeg);
    respond(job_reply);
    assert(bb_start_scan(&ps) == 0 && st->job_active);
    respond("<Status><ScannerStateReason>MediaJamExtra</ScannerStateReason><PaperInADF>false</PaperInADF></Status>");
    assert(bb_is_paper_in_adf(&ps) == 0); /* Exact reason only. */
    respond("<ok/>");
    ps.user_cancel = 1;
    assert(bb_end_scan(&ps, 0) == 0); /* cancel is sent even with user_cancel set */
    assert(strstr(request, "CancelJobRequest") && !st->job_active && !st->pixels_per_line);
    assert(bb_end_scan(&ps, 0) == 0 && opens == 1); /* idempotent */
    ps.user_cancel = 0;

    respond(job_reply);
    assert(bb_start_scan(&ps) == 0);
    reset("");
    read_result = HPMUD_R_IO_TIMEOUT;
    assert(bb_end_scan(&ps, 1) == SANE_STATUS_IO_ERROR);
    assert(st->cancel_pending && st->job_id == 42 && !st->jpeg);
    assert(bb_start_scan(&ps) == SANE_STATUS_IO_ERROR);
    assert(!strstr(request, "CreateScanJobRequest"));
    respond("<ok/>");
    assert(bb_end_scan(&ps, 0) == 0 && !st->job_active);
    /* Successful page, failed intermediate page/cancel, then a fresh job. */
    respond(job_reply);
    assert(bb_start_scan(&ps) == 0);
    assert(bb_end_page(&ps, 0) == 0 && st->job_active);
    assert(bb_start_scan(&ps) == 0);
    reset("");
    read_result = HPMUD_R_IO_ERROR;
    assert(bb_get_image_data(&ps, 0) == SANE_STATUS_IO_ERROR);
    assert(st->cancel_pending && st->job_id == 42 && ps.cnt == 0);
    respond(job_reply);
    assert(bb_start_scan(&ps) == 0);
    assert(strstr(request, "CancelJobRequest") && strstr(request, "CreateScanJobRequest"));
    assert(st->job_active && !st->cancel_pending);
    respond("<ok/>"); /* invalid image, then successful cancel */
    assert(bb_get_image_data(&ps, 0) == SANE_STATUS_IO_ERROR);
    assert(!st->job_active && !st->jpeg && strstr(request, "CancelJobRequest"));
    respond("<Response><JobId>42</JobId><PixelsPerLine>bad</PixelsPerLine></Response>");
    assert(bb_start_scan(&ps) == SANE_STATUS_IO_ERROR);
    assert(strstr(request, "CancelJobRequest") && !st->job_active);

    for (int source = IS_PLATEN; source <= IS_ADF; source++) {
        for (int mode = CE_GRAY8; mode <= CE_RGB24; mode++) {
            for (int dpi = 150; dpi <= (source == IS_ADF ? 300 : 600); dpi *= 2) {
                ps.currentInputSource = source;
                ps.currentScanMode = mode;
                ps.currentResolution = dpi;
                respond(job_reply);
                assert(bb_start_scan(&ps) == 0);
                assert(ps.currentResolution == dpi && ps.currentCompression == SF_JFIF);
                assert(bb_get_parameters(&ps, &pp, 1) == 0);
                assert(pp.bytes_per_line == 2549 * (mode == CE_RGB24 ? 3 : 1));
                respond("<ok/>");
                assert(bb_end_scan(&ps, 0) == 0);
            }
        }
    }
    respond(job_reply);
    assert(bb_start_scan(&ps) == 0);
    respond("<ok/>");
    assert(bb_end_page(&ps, 1) == 0 && !st->job_active);
    respond(job_reply);
    assert(bb_start_scan(&ps) == 0);
    respond("<ok/>");
    assert(bb_close(&ps) == 0 && ps.bb_session == NULL);
    assert(strstr(request, "CancelJobRequest"));
    assert(bb_close(&ps) == 0);
}

static void dime_tests(void)
{
    unsigned char dime[40] = {0x0d, 0x10, 0, 0, 0, 0, 0, 10, 0, 0, 0, 2};
    memcpy(dime + 12, "image/jfif", 10);
    dime[24] = 0xff; dime[25] = 0xd8;
    dime[28] = 0x0a; /* final continuation record, no type */
    dime[39] = 2;
    unsigned char full[44];
    memcpy(full, dime, 40);
    full[40] = 0xff; full[41] = 0xd9; full[42] = full[43] = 0;
    unsigned char *out = NULL;
    size_t len;
    for (size_t n = 0; n < sizeof(full); n++)
        assert(extract_dime_jpeg(full, n, &out, &len));
    assert(extract_dime_jpeg(full, sizeof(full), &out, &len) == 0);
    assert(len == 4 && memcmp(out, "\xff\xd8\xff\xd9", 4) == 0);
    free(out);
    alloc_fail = 1;
    assert(extract_dime_jpeg(full, sizeof(full), &out, &len) == SANE_STATUS_NO_MEM);
    alloc_fail = 0;
    full[41] = 0;
    assert(extract_dime_jpeg(full, sizeof(full), &out, &len));
    full[41] = 0xd9; full[0] = 0xfd;
    assert(extract_dime_jpeg(full, sizeof(full), &out, &len));
    full[0] = 0x0d; full[8] = 0xff;
    assert(extract_dime_jpeg(full, sizeof(full), &out, &len));
    unsigned int seed = 42;
    for (int i = 0; i < 5000; i++) {
        unsigned char fuzz[128];
        for (size_t j = 0; j < sizeof(fuzz); j++) {
            seed = seed * 1664525u + 1013904223u;
            fuzz[j] = seed >> 24;
        }
        size_t n = (seed >> 16) % sizeof(fuzz);
        if (extract_dime_jpeg(fuzz, n, &out, &len) == 0) free(out);
        if (dechunk_try(fuzz, n, &out, &len) == 1) free(out);
    }
}

static void adf_height_tests(void)
{
    struct soap_session ps = {0};
    respond(capabilities);
    assert(bb_open(&ps) == 0);
    ps.currentInputSource = IS_ADF;
    ps.currentScanMode = CE_GRAY8;
    ps.effectiveBrx = SANE_FIX(215.9);
    const struct { double mm; int thou; } heights[] = {
        {355.6, 14000},   /* Legal must not become A4. */
        {355.5746, 13999}, /* Old fallback also incorrectly matched near Legal. */
        {297.0, 11693},  /* Explicit A4. */
        {279.4, 11000},  /* Letter. */
        {296.9006, 11689}, /* Existing default selected by the backend. */
        {0.0, 11689},    /* Uninitialized geometry fallback. */
        {400.0, 14000},  /* Hardware maximum still applies. */
    };
    for (int dpi = 150; dpi <= 300; dpi *= 2) {
        ps.currentResolution = dpi;
        for (size_t i = 0; i < sizeof(heights) / sizeof(heights[0]); i++) {
            ps.effectiveBry = SANE_FIX(heights[i].mm);
            SANE_Parameters pp;
            assert(bb_get_parameters(&ps, &pp, 0) == 0);
            assert(pp.lines == (heights[i].thou * dpi) / 1000);
            respond(job_reply);
            assert(bb_start_scan(&ps) == 0);
            char expected[128];
            snprintf(expected, sizeof(expected), "<Height>%d</Height></InputMediaSize>", heights[i].thou);
            assert(strstr(request, expected));
            snprintf(expected, sizeof(expected), "<ScanRegionHeight>%d</ScanRegionHeight>", heights[i].thou);
            assert(strstr(request, expected));
            assert(strstr(request, "<InputSource>ADF</InputSource>"));
            /* A second page retains the ticket without a new CreateScanJob. */
            respond("<ok/>");
            assert(bb_end_page(&ps, 0) == 0);
            assert(bb_start_scan(&ps) == 0 && opens == 0);
            assert(bb_end_scan(&ps, 0) == 0);
        }
    }
    assert(bb_close(&ps) == 0);
}

static void capability_tests(void)
{
    char xml[8192];
    struct scanner_caps caps;
    assert(parse_capabilities(capabilities, strlen(capabilities), &caps) == 0);
    assert(caps.platen.max_width == 8500 && caps.platen.max_height == 11690);
    assert(caps.platen.resolutions[0] == 3 && caps.adf.resolutions[0] == 2);
    assert(caps.adf.max_height == 14000 && !caps.adf_duplex);
    struct soap_session ps = {0};
    fixture("flatbed-only.xml", xml, sizeof(xml));
    respond(xml); assert(bb_open(&ps) == 0);
    assert(ps.inputSourceMap[0] == IS_PLATEN && !ps.inputSourceList[1]);
    assert(ps.adf_resolutionList[0] == 0 && ps.adf_bryRange.max == 0);
    ps.currentInputSource = IS_ADF;
    assert(bb_start_scan(&ps) == SANE_STATUS_INVAL);
    assert(bb_close(&ps) == 0);

    fixture("wide-adf-gray.xml", xml, sizeof(xml));
    respond(xml); assert(bb_open(&ps) == 0);
    assert(ps.scanModeMap[0] == CE_GRAY8 && !ps.scanModeList[1]);
    assert(!ps.inputSourceList[2]); /* Duplex not advertised. */
    assert(ps.adf_brxRange.max == SANE_FIX(297.18));
    assert(ps.adf_bryRange.max == SANE_FIX(431.8));
    ps.currentInputSource = IS_ADF; ps.currentScanMode = CE_GRAY8;
    ps.currentResolution = 600; ps.effectiveBrx = SANE_FIX(297.18); ps.effectiveBry = SANE_FIX(431.8);
    SANE_Parameters pp;
    assert(bb_get_parameters(&ps, &pp, 0) == 0 && pp.lines == 10200 && pp.pixels_per_line == 7020);
    respond(job_reply); assert(bb_start_scan(&ps) == 0);
    assert(strstr(request, "<ScanRegionWidth>11700</ScanRegionWidth>"));
    assert(strstr(request, "<ScanRegionHeight>17000</ScanRegionHeight>"));
    assert(strstr(request, "<Resolution><Width>600</Width><Height>600</Height>"));
    assert(bb_end_page(&ps, 0) == 0);
    respond("<ok/>"); assert(bb_start_scan(&ps) == 0 && opens == 0);
    assert(bb_close(&ps) == 0);

    fixture("adf-only.xml", xml, sizeof(xml));
    respond(xml); assert(bb_open(&ps) == 0);
    assert(ps.inputSourceMap[0] == IS_ADF && !ps.inputSourceList[1]);
    assert(ps.resolutionList[0] == 2 && ps.platen_bryRange.max == 0);
    assert(bb_close(&ps) == 0);
    fixture("hpraw-only.xml", xml, sizeof(xml));
    respond(xml); assert(bb_open(&ps) == SANE_STATUS_UNSUPPORTED && !ps.bb_session);
    const char *bad[] = {
        "<ScannerConfiguration/>", "<ScannerConfiguration>",
        "<!DOCTYPE a [<!ENTITY x SYSTEM 'file:///etc/passwd'>]><a>&x;</a>"
    };
    for (size_t i = 0; i < sizeof(bad)/sizeof(bad[0]); i++)
        assert(parse_capabilities(bad[i], strlen(bad[i]), &caps));
    int id;
    assert(tag_int("<s:JobId xmlns:s='urn:test'> 42 </s:JobId>", "JobId", &id) == 0 && id == 42);
    assert(xml_boolean("<s:PaperInADF xmlns:s='urn:test'> 1 </s:PaperInADF>", "PaperInADF") == 1);
    assert(xml_boolean("<PaperInADF>truejunk</PaperInADF>", "PaperInADF") == -1);
    puts("Capabilities: captured M127, flatbed-only, ADF-only, wide/Gray/600, namespaces and unsupported formats passed");
}

int main(void)
{
    {
        struct soap_session ps = {0}; size_t drained;
        reset("stale reply"); read_result = HPMUD_R_IO_TIMEOUT; partial_timeout = 1;
        assert(probe_drain_pending(&ps, &drained) == SANE_STATUS_GOOD);
        assert(drained == strlen("stale reply") && writes == 0 && opens == closes);
        reset(""); read_result = HPMUD_R_IO_TIMEOUT;
        assert(probe_drain_pending(&ps, &drained) == SANE_STATUS_GOOD && drained == 0);
        reset(""); read_result = HPMUD_R_IO_ERROR;
        assert(probe_drain_pending(&ps, &drained) == SANE_STATUS_IO_ERROR && opens == closes);
        reset("stale reply"); read_fragment = 1; read_seconds = 2;
        assert(probe_drain_pending(&ps, &drained) == SANE_STATUS_IO_ERROR);
        assert(drained < strlen("stale reply") && writes == 0 && opens == closes);
    }

    fixture("m127fn-capabilities.xml", capabilities, sizeof(capabilities));
    parser_tests();
    transport_tests();
    lifecycle_tests();
    dime_tests();
    adf_height_tests();
    capability_tests();
    puts("SOAPHT parser, transport, timeout, busy, cancel, ADF geometry and mid-page recovery passed");
    return 0;
}
