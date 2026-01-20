# VGA TinyGo Port - Development Notes

## Goal
Port GVga library and pico-extras scanvideo library to TinyGo for Raspberry Pi Pico with Pimoroni Pico VGA Demo Base.

## Hardware Setup
- HSYNC: GPIO16
- VSYNC: GPIO17
- RGB565: GPIO0-15
- Target: 640x480@60Hz VGA (31.468 kHz HSYNC, 60 Hz VSYNC)

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

## Next Steps

1. ✅ Restore working color bars (073f37a) - DONE
2. ✅ Verify color bars work - DONE (jagged but visible)
3. ✅ Review commit 49d1b12 for PIO RGB output approach - DONE
4. ✅ Implement PIO-based pixel output (PIO shifts out pixels, not CPU) - DONE
5. ✅ Add DMA to feed scanline data to PIO - DONE
6. ✅ Port composable scanline format from scanvideo - DONE
7. Test hello_world.uf2 on hardware
8. Fix timing issues if needed (back porch, pixel alignment)
9. Add run-length optimization for solid regions (COLOR_RUN)
10. Port remaining gvga features (text mode, multi-bit color)
