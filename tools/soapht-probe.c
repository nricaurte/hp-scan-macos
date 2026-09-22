/* Read-only diagnostic: capture GetScannerElements, without creating a job.
 * Build via probe-soapht.sh against the same HPLIP headers/runtime as the plugin. */
#include "../soapht-macos/bb_soapht_macos.c"
#include "soapht_probe_recovery.h"
int main(int argc, char **argv)
{
    int recover = argc == 3 && !strcmp(argv[1], "--recover");
    if (argc != 2 && !recover) return 2;
    char *uri = argv[recover ? 2 : 1];
    struct soap_session ps = {0};
    struct hpmud_model_attributes model;
    if (hpmud_query_model(uri, &model) != HPMUD_R_OK || model.scantype != 5) {
        fputs("Device is not classified as scan-type=5 by HPLIP.\n", stderr);
        return 2;
    }
    if (hpmud_open_device(uri, model.mfp_mode, &ps.dd) != HPMUD_R_OK) return 3;
    if (recover) {
        size_t drained = 0;
        int status = probe_drain_pending(&ps, &drained);
        fprintf(stderr, "SOAPHT recovery: drained=%zu status=%d\n", drained, status);
        if (status) { hpmud_close_device(ps.dd); return status; }
    }
    char *xml = make_envelope("<wscn:GetScannerElements></wscn:GetScannerElements>");
    unsigned char *body = NULL;
    size_t length = 0;
    int status = xml ? soap_transaction(&ps, xml, &body, &length, SOAPHT_TIMEOUT) : SANE_STATUS_NO_MEM;
    if (!status && fwrite(body, 1, length, stdout) != length) status = SANE_STATUS_IO_ERROR;
    free(xml);
    free(body);
    hpmud_close_device(ps.dd);
    return status;
}
