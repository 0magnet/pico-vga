# VGA TinyGo Port - Development Notes

## Goal
Port GVga library and pico-extras scanvideo library to TinyGo for Raspberry Pi Pico with Pimoroni Pico VGA Demo Base.

## Hardware Setup
- HSYNC: GPIO16
- VSYNC: GPIO17
- RGB565: GPIO0-15
- Target: 640x480@60Hz VGA (31.468 kHz HSYNC, 60 Hz VSYNC)

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
- **Serial terminal**: For debug output (`cat /dev/ttyACM0`, `minicom`, etc.)

### Rebooting to Bootloader Mode via Serial

Instead of physically pressing BOOTSEL while plugging in, you can reboot the Pico into bootloader mode via its serial interface:

```bash
# Send 'r' character to trigger reboot to bootloader
echo 'r' > /dev/ttyACM0
```

This works because the firmware listens for the 'r' character on serial input and calls the ROM's `reset_usb_boot()` function. The implementation in TinyGo uses:

```go
//go:linkname enterBootloader machine.enterBootloader
func enterBootloader()

// In main loop:
if machine.Serial.Buffered() > 0 {
    b, _ := machine.Serial.ReadByte()
    if b == 'r' {
        enterBootloader()
    }
}
```

After sending 'r', the Pico will reboot and appear as a USB mass storage device (RPI-RP2) ready for flashing.

## Key Technical Requirements
- PIO for timing-critical operations (HSYNC, pixel output)
- DMA for feeding pixel data to PIO (CPU alone is too slow/jittery)
- Composable scanline format from pico-extras scanvideo

---

## Current Working State (commit 49d1b12 - Two-SM PIO RGB)

**Test Date: 2025-01-20**

**Result: Color bars with SMOOTH EDGES!**

This is a major breakthrough - PIO-based pixel output eliminates jitter.

**Physical Measurements:**
- Monitor diagonal: ~55 cm (likely 22" 16:9 monitor)
- Monitor width: ~47.7 cm
- White bar: ~4.7 cm
- Yellow/Cyan/Green/Magenta/Red/Blue bars: ~5.7 cm each
- Black bar: ~7.5 cm

**Math Check:**
- Total measured: 4.7 + (6 × 5.7) + 7.5 = 4.7 + 34.2 + 7.5 = 46.4 cm
- Monitor width: 47.7 cm
- Difference: ~1.3 cm (borders/overscan)

**Pixel Width Derivation:**
- 640 pixels across ~47.7 cm = 0.0745 cm/pixel = 0.745 mm/pixel
- Expected bar width: 640/8 = 80 pixels = 5.96 cm

**Actual vs Expected Bar Widths:**
| Bar    | Measured | Pixels (est) | Expected | Difference |
|--------|----------|--------------|----------|------------|
| White  | 4.7 cm   | ~63 pixels   | 80       | -17 pixels |
| Colors | 5.7 cm   | ~77 pixels   | 80       | -3 pixels  |
| Black  | 7.5 cm   | ~101 pixels  | 80       | +21 pixels |

**Analysis:**
- White bar is too narrow (back porch eating into it?)
- Color bars slightly narrow but close
- Black bar too wide (gets remaining time at end of line)
- Total pixel budget seems correct, just distributed unevenly

**Current Architecture (Two-SM PIO):**
- **SM0 (HSYNC)**:
  - Generates HSYNC timing at 25 MHz
  - Fires IRQ 0 (CPU notification) and IRQ 4 (RGB SM trigger)
  - count=1172 for ~31.4 kHz HSYNC
- **SM1 (RGB)**:
  - Waits for IRQ 4 from HSYNC SM
  - Back porch: ~104 cycles delay
  - Color bars: 7 pulled colors + 1 hardcoded black
  - Each bar: ~109 cycles (x=6 iterations × 17 cycles)
  - Outputs via `mov pins, osr` at fixed 25 MHz rate
- **CPU**:
  - Waits for IRQ 0
  - Pushes 8 colors to RGB SM FIFO for next line
  - Handles VSYNC
- **DMA**: NOT USED (yet)

**Why edges are now smooth:**
PIO outputs pixels at fixed 25 MHz clock rate. No CPU jitter involved in pixel timing.

**Remaining timing issues:**
The bar widths aren't exactly 80 pixels each because:
1. Back porch delay in RGB SM may be wrong
2. Per-bar loop count may not give exactly 80 pixels
3. End-of-line handling gives black bar extra time

---

## Previous State (commit 073f37a - CPU pixel output)

**Result: Color bars visible but JAGGED EDGES**

**Architecture:**
- PIO: HSYNC timing only
- CPU: Outputs colors via GPIO (causes jitter)
- DMA: Not used

**Issues:**
- Edges jagged due to CPU timing jitter
- Black/white bars thin

---

## Approach History

### Approach 1: CPU Bit-Bang Everything (Failed)
- CPU handles HSYNC, VSYNC, and pixel output directly
- Result: Timing too slow and jittery, "not supported" on monitor
- Lesson: CPU alone cannot achieve required timing precision

### Approach 2: PIO HSYNC + CPU Pixel Output (Current - Partial Success)
- PIO generates HSYNC timing and fires IRQ
- CPU waits for IRQ, then outputs pixels via GPIO
- Count=1172 gives HSYNC=31440 Hz (very close to 31468 Hz target)
- Result: Color bars visible, but edges jagged due to CPU jitter
- **This is where we are now**

### Approach 3: Two-SM Composable (Attempted - No Signal)
- SM0: Scanline PIO with composable commands (waits for IRQ 4)
- SM1: Timing PIO generates HSYNC and fires IRQ 4
- CPU feeds composable scanline buffer to SM0 TX FIFO
- Result: "No signal" - synchronization issues between SMs
- Needs debugging: IRQ timing, FIFO pre-fill, command format

### Approach 4: PIO Pixel Output + DMA (Not Yet Attempted)
- This is how the original C code works (scanvideo library)
- DMA transfers scanline buffer to PIO TX FIFO
- PIO outputs pixels at exact pixel clock rate
- Should give smooth edges, correct timing
- Requires: TinyGo DMA support (direct register access)

---

## To Fix Jagged Edges

The ONLY way to get smooth edges is to have PIO output the pixels at a fixed clock rate.
CPU cannot do this because of timing jitter.

Options:
1. **PIO pixel output with CPU FIFO feeding** - CPU pushes pixel data to PIO TX FIFO, PIO shifts out at 25 MHz
2. **PIO pixel output with DMA** - DMA transfers scanline buffer to PIO, fully automatic

The pico-extras scanvideo library uses option 2 (DMA + PIO).

---

## Working Milestones (from git history)

### Commit f344115: "SUCCESS: VGA 640x480@60Hz working!"
- Pure CPU bit-bang, no pixel output (just sync signals)
- H=680/90 with 2x LOW delay, 31452Hz HSYNC

### Commit 073f37a: "Working VGA color bars with PIO HSYNC timing"
- PIO handles HSYNC, CPU outputs color bars
- Color bars visible but edges jagged
- **This is the current restored state**

### Commit 49d1b12: "PIO-based RGB output with 8 color bars"
- Two-SM PIO architecture: HSYNC SM + RGB SM
- SM0 fires IRQ 4 to trigger SM1 (RGB output)
- CPU feeds 8 colors to FIFO each line
- **Result: SMOOTH EDGES!** But bar widths not perfect

### Commit 98064fe: "Fix: Use goroutine for render loop (runs on core1)"
- **MAJOR FIX**: Render loop in goroutine → runs on core1
- Main loop on core0 handles USB/serial
- **Result: Stable video + working serial without interference**
- Frame rate: ~60 fps confirmed
- Color bars remain stable even when reading serial output

---

## Timing Calibration Data

| Count | HSYNC (Hz) | VSYNC (Hz) | Notes |
|-------|-----------|-----------|-------|
| 700   | ~33530    | 63        | CPU bottleneck |
| 746   | 33788     | 63        | CPU bottleneck |
| 905   | 33530     | 63        | CPU bottleneck |
| 1100  | 33321     | 63        | CPU bottleneck |
| 1172  | 31440     | 59        | PIO limited, good! |
| 1200  | 30763     | 58        | PIO limited, too slow |
| 1500  | 24994     | 47        | PIO limited |

---

## Hello World Demo (hello_world.go)

**Test Date: 2025-01-20**

**Implementation: DMA + Composable PIO (port of scanvideo approach)**

This is a direct port of the pico-extras scanvideo library approach to TinyGo.

**Architecture:**
- **SM0 (HSYNC)**: Generates HSYNC timing, fires IRQ 0 (CPU) and IRQ 4 (RGB SM)
- **SM1 (RGB)**: Composable scanline program (based on scanvideo.pio)
  - Supports: `color_run`, `raw_run`, `raw_1p`, `raw_2p`, `eol_align`
  - Uses `out pc, 16` for command dispatch
  - Autopull enabled for continuous DMA feeding
- **DMA Channel 0**: Transfers scanline buffer to PIO1 TX FIFO
  - DREQ-gated by PIO TX not-full
  - Triggered per scanline after IRQ 0
- **CPU (Core1)**: Builds composable scanlines from frame buffer
- **CPU (Core0)**: Animation loop + serial output

**Composable Scanline Format:**
```
RAW_RUN: | cmd | color1 | count-1 | color2 | color3 | color4 | ... |
COLOR_RUN: | cmd | color | count-3 | next_cmd | ... |
RAW_1P: | cmd | color | next_cmd |
RAW_2P: | cmd | color1 | color2 | (wraps to RAW_1P) |
EOL_ALIGN: | cmd | 0 | (discards remaining, waits for IRQ) |
```

**Features:**
- 640x480 @ 60Hz, 1-bit color (black/white)
- Frame buffer: 38400 bytes (640×480/8)
- Double-buffered scanline buffers
- Animated "HELLO WORLD" bouncing text
- Border boxes for visual reference

**Files:**
- `hello_world.go` - Main demo source
- `hello.uf2` - Compiled firmware

**To Test:**
```bash
# Put Pico in BOOTSEL mode (hold BOOTSEL while plugging in)
picotool load hello.uf2 -x
# Or copy to mounted Pico drive
```

---

## Current Working State (2025-01-20)

**hello_world.go - Frame Buffer Rendering via PIO**

**Status: WORKING with limitations**

- VGA signal detected and stable
- HSYNC: ~31657 Hz (target 31468 Hz, 0.6% off)
- VSYNC: 60 Hz (correct)
- Frame buffer content visible (animation cycling)
- 7-sample horizontal resolution (low res proof of concept)

**Issues to fix:**
1. Vertical edges jittery/sawblade-like (line-to-line timing variance)
2. Low horizontal resolution (only 7 sample points)
3. ✅ Reboot to BOOTSEL - FIXED using `//go:linkname enterBootloader machine.enterBootloader`

**Architecture:**
- SM0 (HSYNC): Generates timing, fires IRQ 0 (CPU) and IRQ 4 (RGB SM)
- SM1 (RGB): Outputs 7 pulled colors + hardcoded black (same as working color bars)
- CPU (Core1): Samples frame buffer at 7 positions per line, pushes to PIO
- CPU (Core0): Runs animation loop, handles serial

**Key learnings:**
1. Composable scanline format requires DMA - CPU too slow for 640 pixels/line
2. Simple 7-bar approach works well for proof of concept
3. Must match main.go's RGB PIO program exactly for stable output
4. ROM function pointer calls for reset_usb_boot are tricky in TinyGo

---

## Scanvideo Port Status (2025-01-20)

### Package Status

| Package | File | Status | Notes |
|---------|------|--------|-------|
| scanvideo | types.go | Complete | Timing, Mode, ScanlineBuffer, RGB565 |
| scanvideo | pio.go | Complete | BuildTimingProgram, BuildScanlineProgram (composable) |
| scanvideo | driver.go | ~80% | Setup, DMA, IRQ handler - needs integration testing |
| gvga | types.go | Complete | GVga struct, Color, Font types |
| gvga | gvga.go | **CPU output** | Still uses GPIO bit-bang - causes jitter |
| gvga | gfx.go | Complete | Graphics primitives |
| gvga | font.go | Complete | Default font data |
| gvga | palette.go | Complete | Palette lookup tables |

### Original GVga Architecture (C)
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

### Current hello_world.go Architecture (TinyGo)
```
┌─────────────────────────────────────────────────────────────┐
│ Core 0: Animation loop, serial handling                      │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│ Core 1: Render goroutine                                     │
│   - Wait for IRQ 0                                           │
│   - Push 7 pre-computed colors to PIO FIFO                   │
│   - Compute colors for next line (during display)            │
│   - Handle VSYNC                                             │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│ PIO: Simple RGB Program (not composable)                     │
│   - Waits for IRQ 4                                          │
│   - Outputs 7 pulled colors + 1 hardcoded black              │
│   - Each "bar" is ~109 cycles (80 pixels)                    │
└─────────────────────────────────────────────────────────────┘
```

### Key Differences / Missing Pieces

1. **Composable scanlines**: Original uses RAW_RUN for 640 individual pixels; current uses 7 "bars"
2. **DMA transfer**: Original uses DMA to feed scanline buffer; current uses CPU TxPut()
3. **Buffer management**: Original uses scanline buffer pool with Begin/End; current has none
4. **Full resolution**: Original outputs 640 pixels/line; current outputs 7 color samples

### Next Steps

1. ✅ Restore working color bars (073f37a) - DONE
2. ✅ Verify color bars work - DONE (jagged but visible)
3. ✅ Review commit 49d1b12 for PIO RGB output approach - DONE
4. ✅ Implement PIO-based pixel output (PIO shifts out pixels, not CPU) - DONE
5. ✅ Add DMA to feed scanline data to PIO - DONE
6. ✅ Port composable scanline format from scanvideo - DONE
7. ✅ Reboot to BOOTSEL - DONE (using //go:linkname enterBootloader)
8. 🔄 Fix vertical edge jitter - IN PROGRESS (pre-computed colors)
9. 🔄 **Full 640-pixel composable scanlines** - IN PROGRESS (monitor shows "not supported")
10. **Update gvga package to use scanvideo (not CPU output)**
11. Add run-length optimization (COLOR_RUN for solid regions)
12. Port remaining gvga features (text mode, multi-bit color)

---

## Composable Scanline Attempt (2025-01-20)

**Status: Monitor shows "not supported" - timing investigation in progress**

### Implementation Details

hello_world.go now implements full 640-pixel composable scanlines:

**Architecture:**
- **SM0 (HSYNC)**: 25 MHz, generates timing, fires IRQ 0 + IRQ 4
- **SM1 (RGB)**: 50 MHz (2x pixel clock), composable scanline program at offset 0
- **DMA Channel 0**: Transfers 323 words per scanline to PIO TX FIFO
- **Core1**: Builds scanline buffers from frame buffer using palette lookup
- **Core0**: Animation + serial debug output

**Composable PIO Program** (must be at offset 0):
```
Offset 0:  out null, 32        ; end_of_scanline_skip_ALIGN
Offset 1:  wait irq 4          ; entry_point - wait for timing
Offset 2:  out pc, 16          ; dispatch based on command
Offset 3:  out pins, 16        ; color_run - output color
Offset 4:  out x, 16           ; load count
Offset 5:  jmp x-- 5 [1]       ; color_loop
Offset 6:  out pc, 16 [1]      ; next command
Offset 7:  out pins, 16        ; raw_run - first pixel
Offset 8:  out x, 16           ; load count
Offset 9:  out pins, 16        ; pixel_loop
Offset 10: jmp x-- 9           ; loop
Offset 11: out pins, 16        ; raw_1p
Offset 12: out pc, 16          ; next command
Offset 13: out pins, 16 [1]    ; raw_2p (wraps)
Offset 14: out pins, 32        ; raw_1p_skip_ALIGN
Offset 15: out pc, 16          ; nop_extra0
```

**Scanline Buffer Format** (RAW_RUN for 640 pixels):
```
Word 0:  COMPOSABLE_RAW_RUN (7) | first_pixel << 16
Word 1:  count (637) | second_pixel << 16
Word 2:  pixel3 | pixel4 << 16
...
Word 322: COMPOSABLE_RAW_1P (11) | 0 << 16
Word 323: COMPOSABLE_EOL_ALIGN (1) << 16
```

**Palette Lookup Table:**
- 256 × 8 = 2048 entries
- For each byte value (0-255), pre-compute 8 RGB565 colors
- Enables fast 1bpp → RGB565 conversion during scanline build

### Timing Calibration (Composable Mode)

| Count | HSYNC (Hz) | VSYNC (Hz) | Lines/frame | Notes |
|-------|------------|------------|-------------|-------|
| 695   | 48,593     | 92         | 525         | Way too fast |
| 1073  | 35,337     | 67         | 525         | Still too fast |
| 1205  | 31,885     | 60         | 525         | Close! 1.3% off |

**Current state with count=1205:**
- HSYNC: 31,885 Hz (target 31,468 Hz) - 1.3% high
- VSYNC: 60 Hz (correct)
- Lines/frame: 525 (correct)
- Monitor still shows "not supported"

### Key Findings

1. **RGB SM clock must be 2x pixel clock**: Each pixel in raw_run takes 2 PIO cycles (out + jmp), so SM needs 50 MHz to achieve 25 MHz pixel rate

2. **HSYNC timing count differs from 8-bar version**: The 8-bar approach used count=1172, but composable needs different tuning (currently at 1205)

3. **Frequency measurement is essential**: Added debug output showing actual HSYNC/VSYNC frequencies in Hz

4. **Serial runs on Core0, render on Core1**: Using goroutine for render loop correctly separates concerns

### Issues to Investigate

1. **Why "not supported" with correct frequencies?**
   - HSYNC polarity? (currently active-low)
   - VSYNC polarity? (currently active-low)
   - Pixel timing within line?
   - Back porch / front porch timing?

2. **Timing count doesn't match theoretical calculation**
   - Theory: 794 cycles/line at 25 MHz for 31.468 kHz
   - Actual: count=1205 gives 31.885 kHz
   - Discrepancy suggests PIO program overhead is different than calculated

3. **DMA transfer timing**
   - Is DMA completing before next IRQ?
   - Is FIFO being starved or overfilled?

### Files Modified

- `hello_world.go` - Full composable scanline implementation with DMA
- `scanvideo/pio.go` - Correct composable PIO program offsets
- `scanvideo/types.go` - COMPOSABLE_* constants

---

## Building Original C Examples (2025-01-21)

### GVga C Hello World - BUILD SUCCESSFUL

**Location:** `/home/d0mo/go/src/github.com/drfrancintosh/GVga/apps/a0_hello_world`

**Required Environment Variables:**
```bash
export PICO_SDK_PATH=/home/d0mo/go/src/github.com/raspberrypi/pico-sdk
export PICO_EXTRAS_PATH=/home/d0mo/go/src/github.com/raspberrypi/pico-extras
export GVGA_HOME=/home/d0mo/go/src/github.com/drfrancintosh/GVga
```

**Build Commands:**
```bash
cd /home/d0mo/go/src/github.com/drfrancintosh/GVga/apps/a0_hello_world
rm -rf build && mkdir build && cd build
cmake ..
make -j4
```

**Output:** `hello_world.uf2` (73KB)

**Flash Command:**
```bash
picotool load hello_world.uf2 -x
```

**Key Build Details:**
- Uses `pico_scanvideo_dpi` library from pico-extras
- Default board: "pico" (standard Raspberry Pi Pico)
- Compiler: arm-none-eabi-gcc 14.2.0
- Build type: Release
- No special GPIO pin configuration in CMake - scanvideo uses default pins

**Dependencies:**
- pico_stdlib
- pico_multicore
- pico_unique_id
- hardware_i2c
- libgvga (from GVGA_HOME/libs)
- pico_scanvideo_dpi (from pico-extras)

### pico-playground scanvideo_minimal - BUILD SUCCESSFUL, TESTED

**Location:** `/home/d0mo/go/src/github.com/raspberrypi/pico-playground/scanvideo/scanvideo_minimal`

**Required Environment Variables:**
```bash
export PICO_SDK_PATH=/home/d0mo/go/src/github.com/raspberrypi/pico-sdk
export PICO_EXTRAS_PATH=/home/d0mo/go/src/github.com/raspberrypi/pico-extras
```

**Build Commands:**
```bash
cd /home/d0mo/go/src/github.com/raspberrypi/pico-playground/build
make scanvideo_minimal -j4
```

**Output:** `build/scanvideo/scanvideo_minimal/scanvideo_minimal.uf2` (57KB)

**Flash Command:**
```bash
picotool load /home/d0mo/go/src/github.com/raspberrypi/pico-playground/build/scanvideo/scanvideo_minimal/scanvideo_minimal.uf2 -x
```

**Test Result: WORKING!**
- Monitor displays valid VGA signal
- Pattern: Horizontal gradient stripes (black→red), with vertical green/yellow gradient
- This is the expected output - the code sets each scanline to `color = lineNumber << 2`

**Key Implementation Details (from scanvideo_minimal.c):**
```c
#define VGA_MODE vga_mode_320x240_60

void render_scanline(struct scanvideo_scanline_buffer *dest, int core) {
    int l = scanvideo_scanline_number(dest->scanline_id);
    uint16_t bgcolor = (uint16_t) l << 2;  // Color based on line number
    dest->data_used = single_color_scanline(buf, buf_length, VGA_MODE.width, bgcolor);
}
```

**What This Proves:**
1. The pico-extras scanvideo library works correctly
2. The Pimoroni VGA Demo Base hardware is functioning
3. VGA timing is valid and monitor accepts the signal
4. The C compilation environment is correctly configured

---

## CRITICAL: Architecture Mismatch Analysis (2025-01-20)

### The Problem

The Go gvga package does **NOT** use the scanvideo architecture. It uses **CPU bit-banging** which cannot produce stable VGA timing.

### C gvga Architecture (CORRECT)

```c
// gvga.c - render_loop() runs on core1
static void render_loop(GVga *gvga) {
    while (true) {
        // 1. Get a scanline buffer from scanvideo
        struct scanvideo_scanline_buffer *dest = scanvideo_begin_scanline_generation(true);

        // 2. Fill buffer with composable commands (RAW_RUN, COLOR_RUN, etc.)
        dest->data_used = gvga->scanlineRender(gvga, dest->data, ...);

        // 3. Release buffer - scanvideo handles DMA to PIO
        scanvideo_end_scanline_generation(dest);
    }
}

// core1_func() - runs the scanvideo infrastructure
static void core1_func() {
    scanvideo_setup(_gvga.vga_mode);
    scanvideo_timing_enable(true);
    render_loop(&_gvga);
}
```

### Go gvga Architecture (WRONG - CPU bit-banging)

```go
// gvga.go - renderLoop() does CPU bit-banging
func (g *GVga) renderLoop() {
    for g.running {
        // Wait for PIO IRQ
        for Pio.GetIRQ()&1 == 0 {}

        // PROBLEM: Direct GPIO writes!
        for byteIdx := 0; byteIdx < int(g.Width)/8; byteIdx++ {
            b := g.ShowFrame[rowOffset+byteIdx]
            colors := g.PaletteBuf[int(b)*8:]
            for i := 0; i < 8; i++ {
                gpioOut.Set(uint32(colors[i]))  // CPU timing jitter!
                _ = gpioOut.Get() // "delay"
            }
        }
    }
}
```

### Why This Matters

| Aspect | C (scanvideo) | Go (bit-bang) |
|--------|---------------|---------------|
| Pixel timing | PIO @ 25 MHz | CPU variable |
| Jitter | None | High |
| Consistency | Hardware-perfect | Line-to-line variance |
| Resolution | 640 actual pixels | Timing-dependent |
| CPU load | Low (DMA handles transfer) | 100% (busy loop) |

### What Needs to Change

The Go gvga package must be rewritten to:

1. **Call scanvideo.BeginScanlineGeneration()** instead of direct GPIO
2. **Fill scanline buffers with composable commands** (RAW_RUN for pixels)
3. **Call scanvideo.EndScanlineGeneration()** to queue for DMA
4. **Let scanvideo handle all timing** via PIO + DMA

### Comparison Summary

| Component | C Match % | Status |
|-----------|-----------|--------|
| scanvideo timing state | ~80% | Structure matches, needs IRQ debug |
| scanvideo PIO programs | ~90% | Good match to timing.pio |
| scanvideo buffer mgmt | ~70% | Simplified vs C |
| scanvideo topUpTimingFIFO | ~90% | Matches C logic |
| **gvga architecture** | **~10%** | **COMPLETELY DIFFERENT** |
| gvga Init/types | ~85% | Good match |
| gvga graphics primitives | ~90% | Good match |

### Action Items

1. ✅ Document this analysis in NOTES.md
2. 🔄 Rewrite gvga.Start() to call scanvideo.Setup() and scanvideo.TimingEnable()
3. 🔄 Rewrite gvga.renderLoop() to use scanvideo buffer API
4. 🔄 Create scanline render functions that emit composable commands
5. Test on device with proper scanvideo integration

---

## MAJOR BREAKTHROUGH: Working Composable Scanlines (2025-01-21)

### Status: VGA OUTPUT WORKING!

**hello_world.go now displays 640x480 VGA with proper composable scanline format!**

**Test Results:**
- HSYNC: ~31,261 Hz (target 31,468 Hz) - 99.3% accurate
- VSYNC: 59 Hz (target 60 Hz)
- Lines per frame: 523 (target 525)
- Display shows "HELLO WORLD" text with border
- Animation works (text bounces, updates every second)

### Key Fixes Applied

1. **Removed DMA blocking wait** - `waitDMAComplete()` was causing deadlock:
   - DMA waits for SM0 (scanline) to consume data
   - SM0 waits for IRQ 4 from SM3 (timing)
   - SM3 waits for timing FIFO data
   - Goroutine blocked in waitDMAComplete(), couldn't feed SM3
   - Fix: Let DREQ pace the DMA transfer, don't block

2. **Fixed blank scanline format** - Changed from RAW_RUN to COLOR_RUN:
   - RAW_RUN requires pixel data for all 640 pixels (many FIFO words)
   - COLOR_RUN outputs one color for N pixel periods (just 3 words)
   - Matches C pico-extras `_missing_scanline_data` format

3. **Fixed scanline buffer termination** - Removed explicit RAW_1P command:
   - After RAW_RUN loop exits, PIO falls through to raw_1p automatically
   - raw_1p outputs pixel 640 from OSR, then dispatches to EOL
   - Previous code had RAW_1P in buffer which was read as a pixel!
   - Fix: Buffer ends with just EOL_SKIP_ALIGN command

4. **Proper multicore operation** - TinyGo 0.40.1 goroutine support:
   - Render goroutine runs on core1 (time-critical)
   - Main loop runs on core0 (animation, serial output)
   - No cooperative scheduling issues

### Current Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ Core 0: Main Loop                                            │
│   - time.Sleep(1 second)                                     │
│   - Update animation                                         │
│   - Serial status output                                     │
│   - clearScreen() + drawPattern()                            │
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

### Scanline Buffer Format (RAW_RUN)

```
Word 0:  COMPOSABLE_RAW_RUN (7) | pixel1
Word 1:  count (637) | pixel2
Word 2:  pixel3 | pixel4
Word 3:  pixel5 | pixel6
...
Word 320: pixel639 | pixel640
Word 321: COMPOSABLE_EOL_SKIP_ALIGN (0)
```

Total: 322 words = 1288 bytes per scanline

### Blank Scanline Format (COLOR_RUN)

```
Word 0:  COMPOSABLE_COLOR_RUN (3) | BLACK (0)
Word 1:  count (637) | COMPOSABLE_RAW_1P (11)
Word 2:  BLACK (0) | COMPOSABLE_EOL_ALIGN (1)
```

Total: 3 words = 12 bytes (much smaller than RAW_RUN!)

### Remaining Minor Issues

1. **Flickering horizontal band** - Moves up screen, likely due to frame buffer update during display
2. **Slow animation** - Only updates every second (intentional for now)
3. **Colors don't match C example** - Currently black/white only
4. **Right border slightly thinner** - Possible pixel alignment issue

### Files Modified

- `hello_world.go` - Working composable scanline implementation
- `hello.uf2` - Compiled firmware

### What This Proves

1. TinyGo CAN do proper VGA output with PIO + DMA
2. Composable scanline format works correctly
3. Multicore (goroutine on core1) provides stable timing
4. The architecture matches C pico-extras scanvideo library

---

## Color Format Investigation (2025-01-21)

### Status: SOLVED - Format mismatch between GVGA and Pimoroni VGA board!

The TinyGo VGA port had incorrect color output due to a format mismatch.

### The Problem: GVGA vs Pimoroni Color Format

**GVGA (C library) color format** - has a gap at bit 5:
```c
#define GVGA_COLOR(r,g,b) (((b)<<11u)|((g)<<6)|((r)<<0))
// Bits 0-4:   R (5 bits)
// Bit 5:     0 (UNUSED GAP!)
// Bits 6-10: G (5 bits)
// Bits 11-15: B (5 bits)

GVGA_WHITE = 0xFFDF  (binary: 1111 1111 1101 1111)
GVGA_RED = 0x001F    (binary: 0000 0000 0001 1111)
```

**Pimoroni VGA board GPIO mapping** - contiguous, no gap:
```
GPIO 0-4:   R (5 bits)
GPIO 5-9:   G (5 bits)
GPIO 10-14: B (5 bits)
```

**The mismatch:**
When outputting GVGA_WHITE (0xFFDF):
- Bits 0-4 → GPIO 0-4 (R) = 11111 ✓
- Bit 5 → GPIO 5 (G LSB) = 0 ✗ (should be 1!)
- Bits 6-10 → GPIO 6-10 (G bits 1-4, B LSB) = shifted!
- Bits 11-15 → GPIO 11-15 (B bits 1-4 + unused pin) = shifted!

This caused WHITE (0xFFDF) to appear as CYAN (no red component visible) and explained
why colors only worked correctly on BLACK backgrounds.

### The Solution: Use RGB555 without gap

**Correct format for Pimoroni VGA:**
```
RGB555 = (R << 0) | (G << 5) | (B << 10)

RGB555_WHITE = (31 << 0) | (31 << 5) | (31 << 10) = 0x7FFF
RGB555_RED   = (31 << 0) | (0 << 5) | (0 << 10)   = 0x001F
RGB555_GREEN = (0 << 0) | (31 << 5) | (0 << 10)   = 0x03E0
RGB555_BLUE  = (0 << 0) | (0 << 5) | (31 << 10)   = 0x7C00
```

### Previous Test Results (before fix)

| Color Value | Used For | Expected | Actual Result |
|-------------|----------|----------|---------------|
| 0x0000 | Background | BLACK | BLACK ✓ |
| 0x001F | Lines (on 0x0000 bg) | RED | RED ✓ |
| 0xFFDF | Lines (on 0x0000 bg) | WHITE | WHITE ✓ (coincidence) |
| 0xFFDF | Background | WHITE | CYAN ✗ |
| 0x001F | Lines (on 0xFFDF bg) | RED | BLACK ✗ |

The reason RED (0x001F) worked on BLACK is that bits 0-4 map correctly for red-only.
The reason WHITE appeared correct on BLACK is visual perception - even with reduced
green/blue, it looked "white enough" against pure black.

### Fix Attempt #1: RGB555 without gap

Updated `initPalette()` in hello_world.go to use RGB555:
```go
const (
    RGB555_WHITE = (31 << 0) | (31 << 5) | (31 << 10) // 0x7FFF
    RGB555_RED   = (31 << 0) | (0 << 5) | (0 << 10)   // 0x001F
)
```

**Result: GREEN background, BLACK text** - Still not correct!

### Current Status: STILL INVESTIGATING

The RGB555 fix didn't fully work. Latest test shows:
- Background (should be WHITE 0x7FFF) → shows GREEN
- Text (should be RED 0x001F) → shows BLACK

This suggests the pin mapping or color channel order is still wrong.

### Possible Issues to Investigate

1. **Pin order might be BGR not RGB** - Try swapping R and B
2. **Bits might be inverted** - Try inverting all color bits
3. **Different resistor DAC arrangement** - Pimoroni might use different weighting
4. **PIO output order** - Verify which bits go to which pins

### Test Values to Try Next

| Format | WHITE | RED | GREEN | BLUE |
|--------|-------|-----|-------|------|
| RGB555 (current) | 0x7FFF | 0x001F | 0x03E0 | 0x7C00 |
| BGR555 | 0x7FFF | 0x7C00 | 0x03E0 | 0x001F |
| Inverted RGB | 0x8000 | 0x7FE0 | 0x7C1F | 0x03FF |

### Key Learnings So Far

1. **GVGA format (with gap) is definitely wrong** - Cyan background proved that
2. **Simple RGB555 also doesn't work** - Green background proves pin mapping differs
3. **Need to find actual Pimoroni VGA schematic** to determine exact pin→color mapping

### Files Changed

- `hello_world.go` - Color format experiments
- `NOTES.md` - This investigation log

### Note for GVga Port

Color format TBD - need to solve the pin mapping first!

---

## Session Notes (2026-01-21)

### Key Discovery: FIFO Pre-fill Required

**Problem:** Display showed "brief flashes of color" or "fading to black" with composable scanlines.

**Root Cause:** The scanline SM starts at "wait irq 4" but the FIFO was empty. When IRQ 4 fired, there was no data ready, causing synchronization issues.

**Solution:** Pre-fill the scanline FIFO before enabling video output:
```go
// In enableVideo():
scanlineSM.TxPut(uint32(COMPOSABLE_COLOR_RUN) | (0 << 16))
scanlineSM.TxPut(uint32(frameWidth-3) | (uint32(COMPOSABLE_RAW_1P) << 16))
scanlineSM.TxPut(0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16))
```

This ensures the scanline SM has valid data when it first starts.

### Clock Divider Effects on RAW_RUN

| Divider | PIO Clock | Pixels/Line Time | Result |
|---------|-----------|------------------|--------|
| 4 | 31.25 MHz | 41 µs | Full width but timing drift |
| 2.5 | 50 MHz | 25.6 µs | 2/3 width, "swimming" effect |
| 2 | 62.5 MHz | 20.5 µs | Half width |

The timing SM runs at divider 4 (31.25 MHz), so the scanline SM should ideally match. However, RAW_RUN with 2 cycles/pixel at 31.25 MHz takes too long for the visible window, causing the RED channel to appear missing (CYAN background, BLACK lines).

### COLOR_RUN vs RAW_RUN

- **COLOR_RUN works correctly** - Solid colors display as expected
- **RAW_RUN has RED channel issue** - Shows CYAN instead of WHITE, BLACK instead of RED

The difference: COLOR_RUN outputs color once and holds with sticky output during delay loop. RAW_RUN outputs each pixel individually, which may be affected by timing alignment with the visible window.

### Workflow Notes

**One-liner build/flash/reboot:**
```bash
tinygo build -target=pico -o hello.uf2 hello_world.go 2>&1 && echo 'r' > /dev/ttyACM0 2>/dev/null; sleep 3; picotool load -f hello.uf2 && picotool reboot
```

**With serial monitor (chain with mcu mon):**
```bash
tinygo build -target=pico -o hello.uf2 hello_world.go 2>&1 && echo 'r' > /dev/ttyACM0 2>/dev/null; sleep 3; picotool load -f hello.uf2 && picotool reboot; sleep 3; mcu mon -m /dev/ttyACM0 -b 115200
```

**Individual steps:**
1. Build: `tinygo build -target=pico -o hello.uf2 hello_world.go`
2. Reboot to bootloader: `echo 'r' > /dev/ttyACM0`
3. Flash: `picotool load -f hello.uf2`
4. Reboot: `picotool reboot`
5. Monitor: `mcu mon -m /dev/ttyACM0 -b 115200`

### Prototype Saved

`prototypes/hello_world_swimming_effect.go` - Version with clock divider 2.5 that created interesting "swimming through background" visual effect.

### Outstanding Issues

1. **RED channel not working with RAW_RUN** - CYAN background instead of WHITE
2. **Pixel timing vs visible window alignment** - Need to understand why COLOR_RUN works but RAW_RUN doesn't
3. **Consider using COLOR_RUN for solid regions** - RLE optimization could avoid RAW_RUN issues

---

## Session Notes (2026-01-24) - RAW_RUN Debugging

### Current Problem

The gvga library's RAW_RUN format produces "mostly white but faded screen with jittering black bars on edges" while the simpler COLOR_RUN format works perfectly.

### What Works
- scanvideo library port is complete and working
- scanvideo minimal example displays correctly (uses COLOR_RUN)
- COLOR_RUN with `count = width-3` works perfectly (solid white screen)

### What Doesn't Work
- RAW_RUN format produces display artifacts regardless of count value tested:
  - `count = width-3 = 317`: Black bars more pronounced on right
  - `count = width-2 = 318`: Similar issues
  - `count = width-1 = 319`: Mostly white but faded, jittering black bars on right

### Key C Code Analysis (`_gvga_scanlines.c`)

```c
// For 1bpp with width=320:
push0(buf, COMPOSABLE_RAW_RUN);
push1(buf, *colors++);    // pixel0
push0(buf, width-3);      // count = 317
push1(buf, *colors++);    // pixel1
// ... pixels 2-7 from first byte (6 more pixels)
// cols-- (39 remaining)
// Loop 39 times × 8 pixels = 312 pixels
// Total: 8 + 312 = 320 pixels of data

push0(buf, COMPOSABLE_RAW_1P);
push1(buf, 0);
push32(buf, COMPOSABLE_EOL_ALIGN);
```

### Pixel Count Discrepancy

**C provides 320 pixels but RAW_RUN with count=317 only outputs 318:**
- Offset 7: output pixel0 (1 pixel)
- Offset 8: load count = 317
- Offset 9-10 loop: 317 iterations outputting pixels 1-317
- Total from RAW_RUN: 1 + 317 = 318 pixels

**Remaining 2 pixels (318-319) overflow into RAW_1P handling:**
- After loop ends, OSR is empty (pixel317 was from high bits, OSR cleared)
- At offset 11 (RAW_1P fallthrough), autopull loads next word
- BUT next word is pixels 318-319, NOT the RAW_1P|0 word!
- So pixel318 gets output, pixel319 becomes the jump target!

**If pixel319 = White (0xFFDF):**
- Jump target = 0xFFDF & 0x1F = 31 (out of bounds for 16-instruction program!)
- This should cause erratic behavior

**Mystery:** Why does the C code work? This remains unresolved.

### PIO Program Flow (for reference)

```
Offset 7:  OUT PINS 16        ; raw_run - output pixel0
Offset 8:  OUT X 16           ; load count
Offset 9:  OUT PINS 16        ; pixel_loop - output pixel
Offset 10: JMP X-- 9          ; loop back
Offset 11: OUT PINS 16        ; raw_1p (fallthrough from raw_run!)
Offset 12: OUT PC 16          ; next command
```

### Current Test Code State

`gvga/gvga.go` renderScanline1BPP is simplified to output solid white:
- count = width-1 = 319
- 320 pixels of white data (2 header + 159 word pairs)
- EOL word: 0 | (EOL_ALIGN << 16)

Result: "Mostly white with jittering black bars on right edge"

### Possible Explanations to Investigate

1. **C code might use different video mode/timing** that masks the overflow
2. **C pico-extras scanvideo might have different PIO program behavior**
3. **The xscale=2 (320x240 mode) timing might be different**
4. **DMA/FIFO timing differences between C and TinyGo**

### Files to Reference

- C source: `/home/d0mo/go/src/github.com/drfrancintosh/GVga/libs/gvga/_gvga_scanlines.c`
- TinyGo gvga: `/home/d0mo/go/src/github.com/0magnet/pico-vga/gvga/gvga.go`
- TinyGo scanvideo PIO: `/home/d0mo/go/src/github.com/0magnet/pico-vga/scanvideo/pio.go`
- Working minimal example: `/home/d0mo/go/src/github.com/0magnet/pico-vga/examples/scanvideo/minimal/main.go`

### Commands to Resume

```bash
cd /home/d0mo/go/src/github.com/0magnet/pico-vga/examples/gvga/hello_world_lib
tinygo build -target=pico -o hello_world.uf2 .
picotool load hello_world.uf2 -f && picotool reboot
```

### Next Steps

1. Check actual pico-extras scanvideo.pio source to verify PIO program matches
2. Look for C gvga initialization that might explain the count/pixel relationship
3. Verify if C code uses different width than 320
4. Consider if the pixel overflow is somehow handled differently in C timing
