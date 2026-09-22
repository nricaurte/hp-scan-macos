/* Opt-in physical-scanner test; NOT run by tests/run.sh.
 * Compile with -I/opt/homebrew/include -L/opt/homebrew/lib -lsane.
 * Usage: sane_recovery 'hpaio:/usb/...' [--signal]
 * --signal cancels a Color 600 acquisition from SIGALRM after eight seconds.
 * Requires a flatbed test document. */
#include <assert.h>
#include <stdio.h>
#include <signal.h>
#include <string.h>
#include <unistd.h>
#include <sane/sane.h>

static SANE_Handle active_handle;
static volatile sig_atomic_t signal_delivered;
static void cancel_from_signal(int signo)
{
    (void)signo;
    signal_delivered = 1;
    sane_cancel(active_handle);
}

static void option(SANE_Handle h, const char *name, void *value)
{
    for (int i = 1; ; i++) {
        const SANE_Option_Descriptor *d = sane_get_option_descriptor(h, i);
        assert(d);
        if (d->name && strcmp(d->name, name) == 0) {
            assert(sane_control_option(h, i, SANE_ACTION_SET_VALUE, value, NULL) == SANE_STATUS_GOOD);
            return;
        }
    }
}

int main(int argc, char **argv)
{
    assert(argc == 2 || (argc == 3 && strcmp(argv[2], "--signal") == 0));
    const int signal_test = argc == 3;
    assert(sane_init(NULL, NULL) == SANE_STATUS_GOOD);
    SANE_Handle h;
    assert(sane_open(argv[1], &h) == SANE_STATUS_GOOD);
    char source[] = "Flatbed", mode[8];
    strcpy(mode, signal_test ? "Color" : "Gray");
    SANE_Word dpi = signal_test ? 600 : 150;
    option(h, "source", source);
    option(h, "mode", mode);
    option(h, "resolution", &dpi);
    if (signal_test) {
        active_handle = h;
        struct sigaction action = {0};
        action.sa_handler = cancel_from_signal;
        sigemptyset(&action.sa_mask);
        assert(sigaction(SIGALRM, &action, NULL) == 0);
        alarm(8);
        SANE_Status started = sane_start(h);
        alarm(0);
        printf("signal during acquisition: delivered=%d, start=%s (%d)\n",
               signal_delivered, sane_strstatus(started), started);
        fflush(stdout);
        assert(signal_delivered && started == SANE_STATUS_CANCELLED);
    } else {
        assert(sane_start(h) == SANE_STATUS_GOOD);
        sane_cancel(h);
    }
    unsigned char buf[65536];
    SANE_Int length = 123;
    assert(sane_read(h, buf, sizeof(buf), &length) == SANE_STATUS_CANCELLED);
    assert(length == 0);
    puts("cancelled read: CANCELLED, zero bytes");
    fflush(stdout);

    /* Observe the real post-job busy response without automatically retrying
     * a request inside the driver. Retry below is an explicit frontend action. */
    strcpy(mode, "Gray");
    dpi = 150;
    option(h, "mode", mode);
    option(h, "resolution", &dpi);
    SANE_Status status = sane_start(h);
    printf("immediate restart: %s (%d)\n", sane_strstatus(status), status);
    fflush(stdout);
    assert(status == SANE_STATUS_DEVICE_BUSY || status == SANE_STATUS_GOOD);
    if (status == SANE_STATUS_DEVICE_BUSY) {
        sleep(12);
        assert(sane_start(h) == SANE_STATUS_GOOD);
    }
    size_t total = 0;
    while ((status = sane_read(h, buf, sizeof(buf), &length)) == SANE_STATUS_GOOD) {
        assert(length >= 0);
        total += length;
    }
    assert(status == SANE_STATUS_EOF && total > 0);
    sane_cancel(h);
    sane_close(h);
    sane_exit();
    printf("same-handle recovery: EOF, %zu raster bytes\n", total);
    return 0;
}
