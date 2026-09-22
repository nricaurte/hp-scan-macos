/* Called only after the owning scanimage has exited, with acquisitions excluded.
 * Read pending USB replies without sending/replaying a scan request. A quiet
 * one-second interval ends recovery; byte/time caps prevent unbounded draining.
 */
static int probe_drain_pending(struct soap_session *ps, size_t *drained)
{
    HPMUD_CHANNEL cd = -1;
    *drained = 0;
    enum HPMUD_RESULT r = hpmud_open_channel(ps->dd, SOAPHT_CHANNEL, &cd);
    if (r != HPMUD_R_OK) return transport_status(r);
    double deadline = monotonic_seconds() + 8;
    int status = SANE_STATUS_IO_ERROR;
    while (monotonic_seconds() < deadline && *drained < 32u * 1024u * 1024u) {
        unsigned char buf[4096];
        int got = 0;
        r = hpmud_read_channel(ps->dd, cd, buf, sizeof(buf), 1, &got);
        if (r != HPMUD_R_OK && r != HPMUD_R_IO_TIMEOUT) {
            status = transport_status(r);
            break;
        }
        if (got < 0 || (size_t)got > sizeof(buf)) break;
        if (got == 0 && r == HPMUD_R_IO_TIMEOUT) {
            status = SANE_STATUS_GOOD;
            break;
        }
        *drained += (size_t)got;
    }
    r = hpmud_close_channel(ps->dd, cd);
    if (!status && r != HPMUD_R_OK) status = transport_status(r);
    return status;
}
