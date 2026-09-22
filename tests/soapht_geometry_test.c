/* Exercise the compiled backend's real option handling without opening USB. */
#include <assert.h>
#include <dlfcn.h>
#include <stdio.h>
#include <string.h>
#include <signal.h>
#include "sane.h"
#include "hpmud.h"
#include "hpip.h"
#include "soaphti.h"

static struct soap_session *cancel_session;
static void (*cancel_scan)(SANE_Handle);
static int end_calls, end_status, page_calls, paper, start_status;
static void cancel_handler(int signo) { (void)signo; cancel_scan(cancel_session); }
static int end_scan(struct soap_session *ps, int error)
{ (void)ps; (void)error; end_calls++; return end_status; }
static int end_page(struct soap_session *ps, int error)
{ assert(error); page_calls++; return end_scan(ps, error); }
static int paper_present(struct soap_session *ps) { (void)ps; return paper; }
static int start_scan(struct soap_session *ps) { (void)ps; return start_status; }
static int capture_parameters(struct soap_session *ps, SANE_Parameters *pp, int started)
{
    (void)ps; (void)started;
    *pp = (SANE_Parameters){0};
    return 0;
}

int main(int argc, char **argv)
{
    assert(argc == 2);
    void *module = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
    if (!module) { fprintf(stderr, "%s\n", dlerror()); return 1; }
    SANE_Status (*control)(SANE_Handle, SANE_Int, SANE_Action, void *, SANE_Int *) =
        dlsym(module, "soapht_control_option");
    assert(control);
    cancel_scan = dlsym(module, "soapht_cancel");
    SANE_Status (*read_scan)(SANE_Handle, SANE_Byte *, SANE_Int, SANE_Int *) =
        dlsym(module, "soapht_read");
    SANE_Status (*start)(SANE_Handle) = dlsym(module, "soapht_start");
    SANE_Status (*parameters)(SANE_Handle, SANE_Parameters *) = dlsym(module, "soapht_get_parameters");
    assert(cancel_scan && read_scan && start && parameters);
    _Static_assert(sizeof(sig_atomic_t) == sizeof(int), "cancel flag ABI size");
    _Static_assert(_Alignof(sig_atomic_t) == _Alignof(int), "cancel flag ABI alignment");
    struct soap_session ps = {0};
    ps.inputSourceList[0] = "Flatbed";
    ps.inputSourceList[1] = "ADF";
    ps.inputSourceMap[0] = IS_PLATEN;
    ps.inputSourceMap[1] = IS_ADF;
    ps.platen_brxRange.max = ps.platen_tlxRange.max = SANE_FIX(215.9);
    ps.platen_bryRange.max = ps.platen_tlyRange.max = SANE_FIX(296.926);
    ps.adf_brxRange.max = ps.adf_tlxRange.max = SANE_FIX(215.9);
    ps.adf_bryRange.max = ps.adf_tlyRange.max = SANE_FIX(355.6);
    const SANE_Int flat_res[] = {3, 150, 300, 600}, adf_res[] = {2, 150, 300};
    memcpy(ps.platen_resolutionList, flat_res, sizeof(flat_res));
    memcpy(ps.adf_resolutionList, adf_res, sizeof(adf_res));
    char adf[] = "ADF", flatbed[] = "Flatbed";
    assert(control(&ps, SOAP_OPTION_INPUT_SOURCE, SANE_ACTION_SET_VALUE, adf, NULL) == SANE_STATUS_GOOD);
    const SANE_Fixed default_height = SANE_FIX(11.689 * MM_PER_INCH);
    assert(ps.currentBry == default_height);
    assert(ps.bryRange.max == SANE_FIX(355.6));
    assert(ps.currentResolution == 300 && ps.resolutionList[0] == 2);
    assert(control(&ps, SOAP_OPTION_INPUT_SOURCE, SANE_ACTION_SET_VALUE, flatbed, NULL) == 0);
    SANE_Int resolution = 600;
    assert(control(&ps, SOAP_OPTION_SCAN_RESOLUTION, SANE_ACTION_SET_VALUE, &resolution, NULL) == 0);
    assert(ps.currentResolution == 600);
    assert(control(&ps, SOAP_OPTION_INPUT_SOURCE, SANE_ACTION_SET_VALUE, adf, NULL) == 0);
    assert(ps.currentResolution == 300); /* Source switch cannot retain invalid 600. */
    assert(control(&ps, SOAP_OPTION_INPUT_SOURCE, SANE_ACTION_SET_AUTO, NULL, NULL) == 0);
    assert(ps.currentInputSource == IS_PLATEN && ps.resolutionList[0] == 3);
    assert(control(&ps, SOAP_OPTION_INPUT_SOURCE, SANE_ACTION_SET_VALUE, adf, NULL) == 0);

    SANE_Fixed legal = SANE_FIX(355.6), actual = 0;
    assert(control(&ps, SOAP_OPTION_BR_Y, SANE_ACTION_SET_VALUE, &legal, NULL) == SANE_STATUS_GOOD);
    assert(control(&ps, SOAP_OPTION_BR_Y, SANE_ACTION_GET_VALUE, &actual, NULL) == SANE_STATUS_GOOD);
    assert(actual == legal); /* Explicit Legal survives option handling. */
    ps.bb_get_parameters = capture_parameters;
    SANE_Parameters pp;
    const SANE_Fixed sizes[] = {SANE_FIX(297), SANE_FIX(279.4), SANE_FIX(355.6)};
    for (unsigned i = 0; i < sizeof(sizes) / sizeof(sizes[0]); i++) {
        SANE_Fixed bottom = sizes[i];
        assert(control(&ps, SOAP_OPTION_BR_Y, SANE_ACTION_SET_VALUE, &bottom, NULL) == SANE_STATUS_GOOD);
        assert(parameters(&ps, &pp) == SANE_STATUS_GOOD);
        assert(ps.effectiveBry == bottom && ps.effectiveTly == 0);
    }
    SANE_Fixed left = SANE_FIX(10), top_crop = SANE_FIX(20);
    SANE_Fixed right = SANE_FIX(110), bottom_crop = SANE_FIX(170);
    assert(control(&ps, SOAP_OPTION_TL_X, SANE_ACTION_SET_VALUE, &left, NULL) == SANE_STATUS_GOOD);
    assert(control(&ps, SOAP_OPTION_TL_Y, SANE_ACTION_SET_VALUE, &top_crop, NULL) == SANE_STATUS_GOOD);
    assert(control(&ps, SOAP_OPTION_BR_X, SANE_ACTION_SET_VALUE, &right, NULL) == SANE_STATUS_GOOD);
    assert(control(&ps, SOAP_OPTION_BR_Y, SANE_ACTION_SET_VALUE, &bottom_crop, NULL) == SANE_STATUS_GOOD);
    assert(parameters(&ps, &pp) == SANE_STATUS_GOOD);
    assert(ps.effectiveTlx == left && ps.effectiveTly == top_crop);
    assert(ps.effectiveBrx == right && ps.effectiveBry == bottom_crop);
    assert(control(&ps, SOAP_OPTION_INPUT_SOURCE, SANE_ACTION_SET_VALUE, adf, NULL) == SANE_STATUS_GOOD);
    assert(control(&ps, SOAP_OPTION_BR_Y, SANE_ACTION_SET_AUTO, NULL, NULL) == SANE_STATUS_GOOD);
    assert(ps.currentBry == default_height);

    /* An offset crop's automatic bottom retains the old hardware maximum. */
    SANE_Fixed top = SANE_FIX(20);
    assert(control(&ps, SOAP_OPTION_TL_Y, SANE_ACTION_SET_VALUE, &top, NULL) == SANE_STATUS_GOOD);
    assert(control(&ps, SOAP_OPTION_BR_Y, SANE_ACTION_SET_AUTO, NULL, NULL) == SANE_STATUS_GOOD);
    assert(ps.currentBry == legal && ps.currentTly == top);

    /* Switching sources and resetting the bottom leave Flatbed unchanged. */
    assert(control(&ps, SOAP_OPTION_INPUT_SOURCE, SANE_ACTION_SET_VALUE, flatbed, NULL) == SANE_STATUS_GOOD);
    assert(ps.currentBry == ps.platen_bryRange.max);
    assert(ps.bryRange.max == ps.platen_bryRange.max);
    SANE_Fixed crop = SANE_FIX(100);
    assert(control(&ps, SOAP_OPTION_BR_Y, SANE_ACTION_SET_VALUE, &crop, NULL) == SANE_STATUS_GOOD);
    assert(ps.currentBry == crop);
    assert(control(&ps, SOAP_OPTION_BR_Y, SANE_ACTION_SET_AUTO, NULL, NULL) == SANE_STATUS_GOOD);
    assert(ps.currentBry == ps.platen_bryRange.max);
    assert(control(&ps, SOAP_OPTION_INPUT_SOURCE, SANE_ACTION_SET_VALUE, adf, NULL) == SANE_STATUS_GOOD);
    assert(ps.currentBry == default_height && ps.bryRange.max == legal);

    /* Exercise SIGUSR1 -> cancel -> deferred cleanup using the compiled code.
     * The callback must not re-enter USB or free active scan state. */
    ps.bb_end_scan = end_scan;
    ps.bb_end_page = end_page;
    ps.bb_is_paper_in_adf = paper_present;
    ps.bb_start_scan = start_scan;
    cancel_session = &ps;
    struct sigaction action = {0}, previous;
    action.sa_handler = cancel_handler;
    sigemptyset(&action.sa_mask);
    assert(sigaction(SIGUSR1, &action, &previous) == 0);
    assert(raise(SIGUSR1) == 0);
    assert(ps.user_cancel && end_calls == 0 && page_calls == 0);
    SANE_Int length = 123;
    SANE_Byte byte;
    assert(read_scan(&ps, &byte, 1, &length) == SANE_STATUS_CANCELLED);
    assert(length == 0 && page_calls == 1 && end_calls == 1);

    /* Failed cleanup blocks a new job; retry cleanup before clearing cancel. */
    end_status = SANE_STATUS_DEVICE_BUSY;
    assert(start(&ps) == SANE_STATUS_DEVICE_BUSY && ps.user_cancel);
    end_status = SANE_STATUS_GOOD;
    paper = 0;
    assert(start(&ps) == SANE_STATUS_NO_DOCS && !ps.user_cancel);
    paper = 1;
    start_status = SANE_STATUS_DEVICE_BUSY;
    assert(start(&ps) == SANE_STATUS_DEVICE_BUSY);
    start_status = SANE_STATUS_IO_ERROR;
    assert(start(&ps) == SANE_STATUS_IO_ERROR);
    paper = -SANE_STATUS_IO_ERROR;
    assert(start(&ps) == SANE_STATUS_IO_ERROR); /* failed sensor query != empty */
    paper = -SANE_STATUS_JAMMED;
    assert(start(&ps) == SANE_STATUS_JAMMED);
    assert(sigaction(SIGUSR1, &previous, NULL) == 0);

    dlclose(module);
    puts("Compiled backend: geometry, unchanged Flatbed, signal cancel, deferred recovery and SANE errors passed");
    return 0;
}
