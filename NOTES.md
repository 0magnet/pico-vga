# VGA TinyGo Port - Development Notes

## Goal
Port GVga library and pico-extras scanvideo library to TinyGo for Raspberry Pi Pico with Pimoroni Pico VGA Demo Base.

## Hardware Setup
- HSYNC: GPIO16
- VSYNC: GPIO17
- RGB555: GPIO0-14 (R: 0-4, G: 5-9, B: 10-14)
- Target: 640x480@60Hz VGA (31.468 kHz HSYNC, 60 Hz VSYNC)

---

## Host Setup (Linux)

### udev Rules for Pico Access

Install the included udev rules to access the Pico without sudo:

```bash
sudo cp 99-pico.rules /etc/udev/rules.d/
sudo udevadm control --reload-rules
sudo udevadm trigger
```

This enables:
- Serial access via `/dev/ttyACM0` (normal mode)
- picotool access in BOOTSEL mode
- No sudo required for flashing or serial communication

### Required Tools

- **TinyGo**: For compiling firmware (`tinygo flash` or `tinygo build -o file.uf2`)
- **picotool**: For flashing and rebooting device (`picotool load`, `picotool reboot`)
- **mcu**: Serial monitor tool (`mcu mon` for serial output) - available at `/usr/bin/mcu` (symlinked from `~/go/bin/mcu`)

### Serial Monitoring

Use the `mcu mon` command to monitor serial output from the Pico:

```bash
mcu mon
```

This provides a clean serial terminal interface.

**WARNING:** NEVER use `cat /dev/ttyACM0` - it hangs and causes issues. Always use `mcu mon` instead.

### Remote Reboot via picotool

For firmware with USB serial enabled, you can reboot to BOOTSEL mode using picotool:

```bash
# Reboot to BOOTSEL mode (requires USB serial support in firmware)
picotool reboot -u -f

# Then flash new firmware
picotool load firmware.uf2
picotool reboot
```

The `-f` (force) flag allows picotool to communicate with a running device that has USB support.
The `-u` flag tells it to reboot into USB bootloader (BOOTSEL) mode.

### Rebooting to Bootloader Mode via Serial (TinyGo)

For TinyGo firmware that listens for serial input:

```bash
# Send 'r' character to trigger reboot to bootloader
echo 'r' > /dev/ttyACM0
```

---

# C Code Reference

This section documents the original C implementations used as reference for the TinyGo port.

## Building C Examples

### Environment Variables (Required)

```bash
export PICO_SDK_PATH=/home/d0mo/go/src/github.com/raspberrypi/pico-sdk
export PICO_EXTRAS_PATH=/home/d0mo/go/src/github.com/raspberrypi/pico-extras
export GVGA_HOME=/home/d0mo/go/src/github.com/drfrancintosh/GVga
```

### GVga C Hello World

**Location:** `/home/d0mo/go/src/github.com/drfrancintosh/GVga/apps/a0_hello_world`

**Build Commands:**
```bash
cd /home/d0mo/go/src/github.com/drfrancintosh/GVga/apps/a0_hello_world
rm -rf build && mkdir build && cd build
cmake ..
make -j4
```

**Output:** `hello_world.uf2` (~90KB with USB serial)

**Flash & Run:**
```bash
picotool load hello_world.uf2
picotool reboot
```

**USB Serial Support:** Enabled via CMakeLists.txt:
```cmake
pico_enable_stdio_usb(${PROJECT} 1)
pico_enable_stdio_uart(${PROJECT} 0)
```

### pico-playground scanvideo_minimal

**Location:** `/home/d0mo/go/src/github.com/raspberrypi/pico-playground/scanvideo/scanvideo_minimal`

**Build Commands:**
```bash
cd /home/d0mo/go/src/github.com/raspberrypi/pico-playground
rm -rf build && mkdir build && cd build
PICO_SDK_PATH=... PICO_EXTRAS_PATH=... cmake ..
make scanvideo_minimal -j4
```

**Output:** `build/scanvideo/scanvideo_minimal/scanvideo_minimal.uf2` (~90KB with USB serial)

**USB Serial Support:** Enabled via CMakeLists.txt:
```cmake
pico_enable_stdio_usb(scanvideo_minimal 1)
pico_enable_stdio_uart(scanvideo_minimal 0)
```

**Source modified:** `scanvideo_minimal.c` changed `setup_default_uart()` to `stdio_init_all()`

## C GVga Architecture (Reference)

```
┌─────────────────────────────────────────────────────────────┐
│ Core 0: Application                                          │
│   - gvga_init() → allocate frame buffer, palette             │
│   - gfx_*() → draw to frame buffer                           │
│   - gvga_swap() → double buffer swap                         │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│ Core 1: Render Loop (scanvideo)                              │
│   1. scanvideo_begin_scanline_generation(true)               │
│   2. scanlineRender() → fill buffer with composable commands │
│   3. scanvideo_end_scanline_generation()                     │
│   4. DMA transfers buffer to PIO                             │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│ PIO: Composable Scanline Program                             │
│   - Waits for IRQ 4 from timing SM                           │
│   - Executes commands: COLOR_RUN, RAW_RUN, RAW_1P, EOL_ALIGN │
│   - Outputs pixels at 25 MHz via OUT pins                    │
└─────────────────────────────────────────────────────────────┘
```

## C Composable Scanline Format

```
RAW_RUN: | cmd | color1 | count-1 | color2 | color3 | color4 | ... |
COLOR_RUN: | cmd | color | count-3 | next_cmd | ... |
RAW_1P: | cmd | color | next_cmd |
RAW_2P: | cmd | color1 | color2 | (wraps to RAW_1P) |
EOL_ALIGN: | cmd | 0 | (discards remaining, waits for IRQ) |
```

---

# TinyGo Development

This section documents the TinyGo port progress.

## Current Status (2025-01-21)

**hello_world.go - VGA OUTPUT WORKING!**

- 640x480 @ 60Hz VGA with composable scanlines
- HSYNC: ~31,261 Hz (target 31,468 Hz) - 99.3% accurate
- VSYNC: 59 Hz (target 60 Hz)
- Display shows "HELLO WORLD" text with border
- Animation works (text bounces)
- Remote reboot via serial (`echo 'r' > /dev/ttyACM0`)

**Remaining Issue:** Color format investigation in progress

## TinyGo Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ Core 0: Main Loop                                            │
│   - Animation updates                                        │
│   - Serial status output                                     │
│   - clearScreen() + drawPattern()                            │
│   - Listens for 'r' to reboot                                │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ Core 1: Render Goroutine                                     │
│   - topUpTimingFIFO() - feed SM3                             │
│   - Check PIO IRQs (0 = active, 1 = vblank)                  │
│   - startDMA() - transfer scanline buffer to SM0 FIFO        │
│   - buildScanline() - prepare next line's buffer             │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ SM3 (Timing): Offset 16-21                                   │
│   - Generates HSYNC/VSYNC timing                             │
│   - Fires IRQ 0 (active lines) or IRQ 1 (vblank)             │
│   - Fires IRQ 4 to trigger scanline SM                       │
│   - Clock divider: 4 (31.25 MHz)                             │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ SM0 (Scanline): Offset 0-15                                  │
│   - Composable scanline program                              │
│   - Waits for IRQ 4, then processes commands                 │
│   - RAW_RUN outputs 639 pixels, falls through to raw_1p      │
│   - raw_1p outputs pixel 640, dispatches to EOL              │
│   - Clock divider: 4 (31.25 MHz = ~25 MHz pixel rate)        │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ DMA Channel 0                                                │
│   - DREQ = PIO0_TX0 (paced by FIFO not-full)                 │
│   - Transfers scanline buffer to SM0 TX FIFO                 │
│   - 322 words per scanline                                   │
└─────────────────────────────────────────────────────────────┘
```

## TinyGo Scanline Buffer Format

**Active Scanline (RAW_RUN):**
```
Word 0:  COMPOSABLE_RAW_RUN (7) | pixel1
Word 1:  count (637) | pixel2
Word 2:  pixel3 | pixel4
...
Word 320: pixel639 | pixel640
Word 321: COMPOSABLE_EOL_SKIP_ALIGN (0)
```
Total: 322 words = 1288 bytes per scanline

**Blank Scanline (COLOR_RUN):**
```
Word 0:  COMPOSABLE_COLOR_RUN (3) | BLACK (0)
Word 1:  count (637) | COMPOSABLE_RAW_1P (11)
Word 2:  BLACK (0) | COMPOSABLE_EOL_ALIGN (1)
```
Total: 3 words = 12 bytes

## TinyGo Key Fixes Applied

1. **Removed DMA blocking wait** - `waitDMAComplete()` caused deadlock. Let DREQ pace the transfer.

2. **Fixed blank scanline format** - Use COLOR_RUN (3 words) instead of RAW_RUN (322 words).

3. **Fixed scanline buffer termination** - Buffer ends with EOL_SKIP_ALIGN only; PIO falls through to raw_1p automatically.

4. **Proper multicore operation** - Render goroutine runs on core1, main loop on core0.

## TinyGo Serial Reboot Implementation

```go
// In main loop:
if machine.Serial.Buffered() > 0 {
    b, _ := machine.Serial.ReadByte()
    if b == 'r' {
        machine.EnterBootloader()
    }
}
```

## TinyGo Progress History

| Commit | Status | Notes |
|--------|--------|-------|
| f344115 | Working | Pure CPU bit-bang sync signals only |
| 073f37a | Partial | PIO HSYNC + CPU pixels - jagged edges |
| 49d1b12 | Working | Two-SM PIO - smooth edges |
| 98064fe | Working | Goroutine fix - stable video + serial |
| a8817c5 | Working | Composable scanlines - 640x480 output |
| 757917f | WIP | Color format investigation |

## TinyGo Color Format Investigation (In Progress)

**Problem:** Colors don't display correctly.

**GVGA C library format** (has gap at bit 5):
```c
#define GVGA_COLOR(r,g,b) (((b)<<11u)|((g)<<6)|((r)<<0))
// Bits 0-4: R, Bit 5: GAP, Bits 6-10: G, Bits 11-15: B
```

**Pimoroni VGA board GPIO** (contiguous):
```
GPIO 0-4: R (5 bits)
GPIO 5-9: G (5 bits)
GPIO 10-14: B (5 bits)
```

**Correct RGB555 format:**
```
RGB555 = (R << 0) | (G << 5) | (B << 10)
RGB555_WHITE = 0x7FFF
RGB555_RED   = 0x001F
RGB555_GREEN = 0x03E0
RGB555_BLUE  = 0x7C00
```

**Status:** Still investigating - RGB555 shows unexpected colors. May need to verify actual pin mapping.

## TinyGo Files

- `hello_world.go` - Main demo with composable scanlines
- `hello.uf2` - Compiled firmware
- `gvga/` - TinyGo GVga library port (WIP)
- `scanvideo/` - TinyGo scanvideo library port (WIP)

## TinyGo Next Steps

1. ✅ Working composable scanlines
2. ✅ Remote reboot via serial
3. 🔄 Fix color format
4. ⬚ Update gvga package to use scanvideo
5. ⬚ Add run-length optimization (COLOR_RUN for solid regions)
6. ⬚ Port remaining gvga features (text mode, multi-bit color)
