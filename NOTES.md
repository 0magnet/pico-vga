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
