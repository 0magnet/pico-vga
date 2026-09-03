/*
 * Modified scanvideo_minimal with debug logging for comparison with TinyGo version
 */

#include <stdio.h>
#include <string.h>

#include "pico.h"
#include "pico/scanvideo.h"
#include "pico/scanvideo/composable_scanline.h"
#include "pico/multicore.h"
#include "pico/sync.h"
#include "pico/stdlib.h"
#include "hardware/clocks.h"
#include "hardware/pio.h"
#include "pico/bootrom.h"

#define VGA_MODE vga_mode_640x480_60
extern const struct scanvideo_pio_program video_24mhz_composable;

static struct mutex frame_logic_mutex;
static volatile uint32_t hsync_count = 0;
static volatile uint32_t frame_count = 0;

static void frame_update_logic();
static void render_scanline(struct scanvideo_scanline_buffer *dest, int core);

void render_loop() {
    static uint32_t last_frame_num = 0;
    int core_num = get_core_num();
    printf("Rendering on core %d\n", core_num);

    while (true) {
        struct scanvideo_scanline_buffer *scanline_buffer = scanvideo_begin_scanline_generation(true);
        hsync_count++;

        mutex_enter_blocking(&frame_logic_mutex);
        uint32_t frame_num = scanvideo_frame_number(scanline_buffer->scanline_id);
        if (frame_num != last_frame_num) {
            last_frame_num = frame_num;
            frame_count++;
            frame_update_logic();
        }
        mutex_exit(&frame_logic_mutex);

        render_scanline(scanline_buffer, core_num);
        scanvideo_end_scanline_generation(scanline_buffer);
    }
}

struct semaphore video_setup_complete;

void core1_func() {
    sem_acquire_blocking(&video_setup_complete);
    render_loop();
}

// Debug: print timing state info
void print_timing_debug() {
    printf("=== Timing Debug ===\n");
    printf("System clock: %lu Hz\n", clock_get_hz(clk_sys));
    printf("VGA Mode: %dx%d\n", VGA_MODE.width, VGA_MODE.height);

    const scanvideo_timing_t *timing = VGA_MODE.default_timing;
    printf("Timing:\n");
    printf("  h_active=%d h_front_porch=%d h_pulse=%d h_total=%d\n",
           timing->h_active, timing->h_front_porch, timing->h_pulse, timing->h_total);
    printf("  v_active=%d v_front_porch=%d v_pulse=%d v_total=%d\n",
           timing->v_active, timing->v_front_porch, timing->v_pulse, timing->v_total);
    printf("  h_sync_polarity=%d v_sync_polarity=%d\n",
           timing->h_sync_polarity, timing->v_sync_polarity);
    printf("  clock_freq=%lu\n", timing->clock_freq);

    // Calculate expected frequencies
    uint32_t expected_hsync = timing->clock_freq / timing->h_total;
    uint32_t expected_vsync = expected_hsync / timing->v_total;
    printf("Expected: HSYNC=%lu Hz, VSYNC=%lu Hz\n", expected_hsync, expected_vsync);
}

int vga_main(void) {
    mutex_init(&frame_logic_mutex);
    sem_init(&video_setup_complete, 0, 1);

    printf("\n=== VGA Scanvideo Debug ===\n");
    print_timing_debug();

    // Core 1 will wait for us to finish video setup, and then start rendering
    multicore_launch_core1(core1_func);

    printf("Setting up scanvideo...\n");
    scanvideo_setup(&VGA_MODE);
    printf("Enabling timing...\n");
    scanvideo_timing_enable(true);
    printf("Video enabled!\n");

    // Print PIO state after setup
    printf("\nPIO State after setup:\n");
    printf("  PIO0 CTRL: 0x%08lx\n", pio0_hw->ctrl);
    printf("  SM0 enabled: %d\n", (pio0_hw->ctrl >> 0) & 1);
    printf("  SM1 enabled: %d\n", (pio0_hw->ctrl >> 1) & 1);
    printf("  SM2 enabled: %d\n", (pio0_hw->ctrl >> 2) & 1);
    printf("  SM3 enabled: %d\n", (pio0_hw->ctrl >> 3) & 1);

    sem_release(&video_setup_complete);

    // Debug output loop on core0
    uint32_t last_hsync = 0;
    uint32_t last_frame = 0;
    absolute_time_t last_time = get_absolute_time();

    while (true) {
        sleep_ms(1000);

        absolute_time_t now = get_absolute_time();
        int64_t elapsed_us = absolute_time_diff_us(last_time, now);
        if (elapsed_us <= 0) elapsed_us = 1;

        uint32_t h = hsync_count;
        uint32_t f = frame_count;

        uint32_t hsync_delta = h - last_hsync;
        uint32_t frame_delta = f - last_frame;

        uint32_t hsync_hz = (hsync_delta * 1000000) / elapsed_us;
        uint32_t vsync_hz = (frame_delta * 1000000) / elapsed_us;
        uint32_t lines_per_frame = (frame_delta > 0) ? (hsync_delta / frame_delta) : 0;

        printf("HSYNC: %lu Hz  VSYNC: %lu Hz  Lines: %lu\n", hsync_hz, vsync_hz, lines_per_frame);
        printf("  SM0 PC: %d  SM3 PC: %d\n", pio0_hw->sm[0].addr, pio0_hw->sm[3].addr);
        printf("  SM3 EXECCTRL: 0x%08lx\n", pio0_hw->sm[3].execctrl);

        last_hsync = h;
        last_frame = f;
        last_time = now;

        // Check for reboot command
        int c = getchar_timeout_us(0);
        if (c == 'r' || c == 'R') {
            printf("Rebooting to bootloader...\n");
            sleep_ms(100);
            reset_usb_boot(0, 0);
        }
    }

    return 0;
}

void frame_update_logic() {
    // Nothing to do
}

#define MIN_COLOR_RUN 3

int32_t single_color_scanline(uint32_t *buf, size_t buf_length, int width, uint32_t color16) {
    assert(buf_length >= 2);
    assert(width >= MIN_COLOR_RUN);

    buf[0] = COMPOSABLE_COLOR_RUN | (color16 << 16);
    buf[1] = (width - MIN_COLOR_RUN) | (COMPOSABLE_RAW_1P << 16);
    buf[2] = 0 | (COMPOSABLE_EOL_ALIGN << 16);

    return 3;
}

void render_scanline(struct scanvideo_scanline_buffer *dest, int core) {
    uint32_t *buf = dest->data;
    size_t buf_length = dest->data_max;

    int l = scanvideo_scanline_number(dest->scanline_id);
    uint16_t bgcolour = (uint16_t) l << 2;
    dest->data_used = single_color_scanline(buf, buf_length, VGA_MODE.width, bgcolour);
    dest->status = SCANLINE_OK;
}

int main(void) {
    stdio_init_all();
    sleep_ms(2000); // Wait for USB serial to connect

    printf("\n\n=== Scanvideo Minimal Debug Build ===\n");
    printf("Press 'r' to reboot to bootloader\n\n");

    return vga_main();
}
