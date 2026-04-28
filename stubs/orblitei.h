#ifndef _SANE_ORBLITE_INTERFACE_H
#define _SANE_ORBLITE_INTERFACE_H

/* macOS stub: orblite path disabled.
 * Smart Tank 500 (and other LEDM devices) do not use orblite.
 * Provides just the types and function prototypes that hpaio.c needs
 * to link, implemented as no-ops in orblite.c. */

#include "sane.h"

typedef enum {
    optCount = 0,
    optLast = 1,
} EOptionIndex;

struct t_SANE {
    char *tag;
    SANE_Option_Descriptor *Options;
};
typedef struct t_SANE *SANE_THandle;

extern SANE_Status orblite_init(SANE_Int *version_code, SANE_Auth_Callback authorize);
extern SANE_Status orblite_get_devices(const SANE_Device ***device_list, SANE_Bool local_only);
extern SANE_Status orblite_open(SANE_String_Const devicename, SANE_Handle *handle);
extern void orblite_close(SANE_Handle handle);
extern SANE_Status orblite_control_option(SANE_Handle handle, SANE_Int option, SANE_Action action, void *value, SANE_Int *info);
extern SANE_Status orblite_get_parameters(SANE_Handle handle, SANE_Parameters *params);
extern SANE_Status orblite_start(SANE_Handle handle);
extern SANE_Status orblite_read(SANE_Handle handle, SANE_Byte *data, SANE_Int max_length, SANE_Int *length);
extern void orblite_cancel(SANE_Handle handle);

#endif
