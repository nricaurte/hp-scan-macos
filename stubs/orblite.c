/* macOS stub for the orblite scan path.
 * The Smart Tank 500 series and other LEDM-based MFPs do not use the
 * orblite scan protocol, so we provide no-op implementations. The
 * SANE_STATUS_UNSUPPORTED return value matches hpaio's expectation
 * for devices that don't speak orblite. */

#include <stddef.h>
#include "sane.h"
#include "orblitei.h"

SANE_Status orblite_init(SANE_Int *version_code, SANE_Auth_Callback authorize) {
    (void)authorize;
    if (version_code) *version_code = SANE_VERSION_CODE(1, 0, 0);
    return SANE_STATUS_GOOD;
}

SANE_Status orblite_get_devices(const SANE_Device ***device_list, SANE_Bool local_only) {
    /* hpaio.c passes an uninitialized pointer to this function; do not
     * dereference device_list. The hpaio code already populated its own
     * DeviceList before calling us. */
    (void)device_list;
    (void)local_only;
    return SANE_STATUS_GOOD;
}

SANE_Status orblite_open(SANE_String_Const devicename, SANE_Handle *handle) {
    (void)devicename; (void)handle;
    return SANE_STATUS_UNSUPPORTED;
}

void orblite_close(SANE_Handle handle) {
    (void)handle;
}

SANE_Status orblite_control_option(SANE_Handle handle, SANE_Int option, SANE_Action action, void *value, SANE_Int *info) {
    (void)handle; (void)option; (void)action; (void)value; (void)info;
    return SANE_STATUS_UNSUPPORTED;
}

SANE_Status orblite_get_parameters(SANE_Handle handle, SANE_Parameters *params) {
    (void)handle; (void)params;
    return SANE_STATUS_UNSUPPORTED;
}

SANE_Status orblite_start(SANE_Handle handle) {
    (void)handle;
    return SANE_STATUS_UNSUPPORTED;
}

SANE_Status orblite_read(SANE_Handle handle, SANE_Byte *data, SANE_Int max_length, SANE_Int *length) {
    (void)handle; (void)data; (void)max_length; (void)length;
    return SANE_STATUS_UNSUPPORTED;
}

void orblite_cancel(SANE_Handle handle) {
    (void)handle;
}
