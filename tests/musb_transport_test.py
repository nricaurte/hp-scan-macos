#!/usr/bin/env python3
"""Compile the actual patched libusb read wrapper against a fault injector."""
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
src = Path(os.environ.get('HPLIP_SRC', '/tmp/hplip-build/hplip-3.25.8'))
text = (src / 'io/hpmud/musb.c').read_text()
start = text.index('static int libusb_bulk_read(')
end = text.index('static int libusb_bulk_write(', start)
out = Path(os.environ.get('TEST_OUT', str(root / 'build/tests')))
out.mkdir(parents=True, exist_ok=True)
harness = r'''
#include <assert.h>
#include <errno.h>
#include <stdio.h>
typedef void libusb_device_handle;
#define LIBUSB_ERROR_TIMEOUT -7
static int result, count, last_timeout;
static int libusb_bulk_transfer(libusb_device_handle *d, int ep, unsigned char *buf,
                                int n, int *actual, int timeout) {
    (void)d; (void)ep; (void)buf; (void)n;
    *actual = count; last_timeout = timeout; return result;
}
'''
harness += text[start:end]
harness += r'''
int main(void) {
    char buf[16];
    result = 0; count = 4;
    assert(libusb_bulk_read(NULL, 1, buf, sizeof(buf), 1000) == 4);
    result = LIBUSB_ERROR_TIMEOUT; count = 0;
    assert(libusb_bulk_read(NULL, 1, buf, sizeof(buf), 1000) == -ETIMEDOUT);
    result = LIBUSB_ERROR_TIMEOUT; count = 3;
    assert(libusb_bulk_read(NULL, 1, buf, sizeof(buf), 1000) == 3);
    result = -4; count = 0; /* LIBUSB_ERROR_NO_DEVICE */
    assert(libusb_bulk_read(NULL, 1, buf, sizeof(buf), 1000) == -EIO);
    result = -1; count = 2; /* a partial hard failure must not appear successful */
    assert(libusb_bulk_read(NULL, 1, buf, sizeof(buf), 1000) == -EIO);
    result = LIBUSB_ERROR_TIMEOUT; count = 0;
    assert(libusb_bulk_read(NULL, 1, buf, sizeof(buf), 0) == -ETIMEDOUT);
    assert(last_timeout == 1); /* never libusb's infinite timeout */
    puts("Patched USB wrapper: partial timeout, disconnect, errors and finite wait passed");
}
'''
(out / 'musb_transport_test.c').write_text(harness)
subprocess.run(['clang', '-Wall', '-Wextra', '-Werror', '-fsanitize=address,undefined',
                str(out / 'musb_transport_test.c'), '-o', str(out / 'musb_transport_test')], check=True)
subprocess.run([str(out / 'musb_transport_test')], check=True)
