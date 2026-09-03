#!/bin/bash
# Flash and cycle through all C VGA examples for Pimoroni Pico VGA Demo Base
# Each example runs for RUN_TIME seconds before rebooting to the next
#
# Requirements:
# - picotool installed and in PATH
# - Device in BOOTSEL mode to start
#
# All examples have been modified to include USB serial support for remote reboot.

RUN_TIME=15  # seconds to run each example

# Get the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Examples from pico-playground scanvideo library
# Source: https://github.com/raspberrypi/pico-playground/tree/master/scanvideo
PICO_PLAYGROUND_EXAMPLES=(
    # https://github.com/raspberrypi/pico-playground/tree/master/scanvideo/demo1
    "demo1.uf2"

    # https://github.com/raspberrypi/pico-playground/tree/master/scanvideo/mandelbrot
    "mandelbrot.uf2"

    # https://github.com/raspberrypi/pico-playground/tree/master/scanvideo/mario_tiles
    "mario_tiles.uf2"

    # https://github.com/raspberrypi/pico-playground/tree/master/scanvideo/sprite_demo
    "sprite_demo.uf2"

    # https://github.com/raspberrypi/pico-playground/tree/master/scanvideo/test_pattern
    "test_pattern.uf2"

    # https://github.com/raspberrypi/pico-playground/tree/master/scanvideo/textmode
    "pico_textmode.uf2"

    # https://github.com/raspberrypi/pico-playground/tree/master/scanvideo/hscroll_dma_tiles
    "hscroll_dma_tiles.uf2"

    # https://github.com/raspberrypi/pico-playground/tree/master/scanvideo/scanvideo_minimal
    "scanvideo_minimal.uf2"
)

# Examples from GVga library
# Source: https://github.com/drfrancintosh/GVga/tree/main/apps
GVGA_EXAMPLES=(
    # https://github.com/drfrancintosh/GVga/tree/main/apps/a0_hello_world
    "gvga_hello_world.uf2"

    # https://github.com/drfrancintosh/GVga/tree/main/apps/a1_screensaver
    "gvga_screensaver.uf2"

    # https://github.com/drfrancintosh/GVga/tree/main/apps/b0_fonts
    "gvga_fonts.uf2"

    # https://github.com/drfrancintosh/GVga/tree/main/apps/b1_text_mode
    "gvga_text_mode.uf2"

    # https://github.com/drfrancintosh/GVga/tree/main/apps/b2_alt_text_mode
    "gvga_alt_text_mode.uf2"
)

# Combine all examples
EXAMPLES=("${PICO_PLAYGROUND_EXAMPLES[@]}" "${GVGA_EXAMPLES[@]}")

TOTAL=${#EXAMPLES[@]}
COUNT=0

echo "=== VGA Examples Flash Cycle ==="
echo "Run time per example: ${RUN_TIME}s"
echo "Total examples: ${TOTAL}"
echo ""

for uf2 in "${EXAMPLES[@]}"; do
    ((COUNT++))
    name=$(basename "$uf2" .uf2)
    uf2_path="${SCRIPT_DIR}/${uf2}"

    if [[ ! -f "$uf2_path" ]]; then
        echo "[$COUNT/$TOTAL] SKIP: $name (file not found)"
        continue
    fi

    echo "[$COUNT/$TOTAL] Flashing: $name"

    if ! picotool load "$uf2_path" 2>/dev/null; then
        echo "  ERROR: Failed to load $name - is device in BOOTSEL mode?"
        exit 1
    fi

    picotool reboot
    echo "  Running for ${RUN_TIME}s..."
    sleep "$RUN_TIME"

    echo "  Rebooting to BOOTSEL..."
    if ! picotool reboot -u -f 2>/dev/null; then
        echo "  ERROR: Failed to reboot $name - USB serial may not be working"
        exit 1
    fi
    sleep 1

    echo "  OK"
    echo ""
done

echo "=== All ${TOTAL} examples completed ==="
