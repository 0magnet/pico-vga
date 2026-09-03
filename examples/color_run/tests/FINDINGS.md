# Color Run Test Findings

## Critical Discovery: DMA Collision Issue

### Symptom
- Display shows "faded color at top, fading to black at bottom"
- Same pattern with GREEN (0x07C0) and WHITE (0xFFDF)
- hello_world works correctly with same PIO/SM/timing config

### Root Cause: DMA Collisions
**DMA collision count: 27,000+ per ~2 seconds (~450 per frame)**

Without proper synchronization, each `startDMA()` call interrupts the previous transfer before it completes. This causes:
1. First scanlines: DMA completes (full color visible)
2. Middle scanlines: DMA partially completes (some color, some black)
3. Later scanlines: DMA barely starts before being interrupted (mostly black)

Result: Gradient from full color at top to black at bottom.

### Why hello_world Works
hello_world's `buildScanline()` function does substantial work:
- Reads frame buffer data
- Performs palette lookups (256*8 entry table)
- Builds 322 words with pixel-by-pixel processing

This naturally takes enough time for DMA to complete before the next scanline IRQ.

### Why color_run Fails
color_run's `buildSolidScanline()` is much simpler:
- Just fills 322 words with constant color
- No memory lookups or complex processing
- Completes too fast, causing immediate DMA collision

### Attempted Fixes

#### Fix 1: Wait for DMA completion
```go
for dma.CtrlTrig.Get()&(1<<24) != 0 {
    waitCycles++
    // wait...
}
```
**Result:** HSYNC dropped to 15853 Hz (should be 31250), display shows "not supported"
- The wait loop takes too long and breaks timing

#### Fix 2: Don't wait, just count collisions
```go
if dma.CtrlTrig.Get()&(1<<24) != 0 {
    dmaCollisionCount.Set(...)
}
```
**Result:** Confirmed massive collision count, display shows gradient

### Serial Debug Values Comparison

| Metric | hello_world | color_run (broken) | color_run (with wait) |
|--------|-------------|--------------------|-----------------------|
| HSYNC | 31250 Hz | 31250 Hz | 15853 Hz |
| VSYNC | 59 Hz | 59 Hz | 30 Hz |
| DMA collisions | 0 (implicit) | 27,000+ | 17,000+ |
| Display | Working | Faded gradient | Not supported |

### GPIO Line Samples
Both programs show same pattern (sampling happens during H-blank):
- Line 0: BLACK (0x0000) - vblank region
- Lines 100-400: Colored (WHITE/GREEN/RED) - active region
- Line 479: BLACK (0x0000) - end of frame

### Key Technical Details

1. **SHIFTCTRL = 0x400E0000** for both programs:
   - AUTOPULL = 0 (disabled)
   - OUT_SHIFTDIR = 0 (shift left)
   - FJOIN_TX = 1 (TX FIFO joined)

   TinyGo's SetOutShift() doesn't seem to set these correctly, but both programs work/don't work with same config.

2. **DMA DREQ configuration:**
   - TREQ_SEL = 0 (PIO0_TX0)
   - DMA is paced by PIO FIFO not-full
   - 322 words @ 32-bit = 1288 bytes per scanline

3. **Timing:**
   - PIO clock: 31.25 MHz (125 MHz / 4)
   - RAW_RUN: 2 cycles per pixel (out + jmp)
   - 640 pixels * 2 cycles = 1280 cycles = ~41 µs per scanline
   - Frame: 525 lines @ ~31.75 µs/line = ~16.67 ms (~60 Hz)

### Potential Solutions

1. **Add artificial delay** to buildSolidScanline() to match hello_world timing
2. **DMA chaining** - configure DMA to automatically chain multiple scanlines
3. **Pre-compute all scanlines** into a large buffer, single DMA per frame
4. **Non-blocking DMA check** - skip scanline if DMA still busy (accept visual artifact)

### Next Steps
- Try adding delay loop in buildSolidScanline() to match hello_world timing
- Investigate DMA chaining for smoother operation
- Consider using FIFO watermark interrupts instead of scanline-by-scanline DMA
