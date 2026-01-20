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

## Current Working State (commit 073f37a restored)

**Test Date: 2025-01-20**

**Result: Color bars visible, approximately correct sizes**

**Issues:**
1. Black and white bars are slightly thin
2. Edges of color bars are jagged (vertical lines not smooth)

**Current Architecture:**
- **PIO**: Generates HSYNC timing only
  - Runs at 25 MHz (125 MHz / 5 clock divider)
  - High period controlled by count in X register (count=1172)
  - Low period fixed at 95 cycles (3x set instructions with delays)
  - Fires IRQ 0 at end of sync pulse to notify CPU
- **CPU**: Handles everything else
  - Waits for PIO IRQ 0 (busy-wait loop)
  - Outputs back porch delay (60 iterations of volatile read)
  - Outputs color bars via GPIO set/clear (100 iterations per bar)
  - Handles VSYNC directly via GPIO
- **DMA**: NOT USED

**Why edges are jagged:**
CPU timing jitter causes inconsistent pixel widths. Sources of jitter:
1. Go runtime overhead (garbage collection, scheduler)
2. Variable loop iteration times
3. Memory access latency variations
4. Pipeline stalls
5. No cycle-accurate timing guarantee from CPU

**Why some bars are thin:**
The 100-iteration delay loops don't precisely match 80 pixels worth of time.
Each bar should be 80 pixels at 25 MHz = 3.2 µs = 400 CPU cycles at 125 MHz.
But loop overhead and Go compiler output varies.

**Measured Frequencies (from earlier tests with timing code):**
- HSYNC: ~31440 Hz (target: 31468 Hz) - 0.09% error, acceptable
- VSYNC: ~59 Hz (target: 60 Hz) - acceptable
- Lines/frame: 525 (correct)

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
- Attempted PIO for RGB output
- Need to review this approach

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

## Next Steps

1. ✅ Restore working color bars (073f37a) - DONE
2. ✅ Verify color bars work - DONE (jagged but visible)
3. Review commit 49d1b12 for PIO RGB output approach
4. Implement PIO-based pixel output (PIO shifts out pixels, not CPU)
5. Add DMA to feed scanline data to PIO
6. Port composable scanline format from scanvideo
