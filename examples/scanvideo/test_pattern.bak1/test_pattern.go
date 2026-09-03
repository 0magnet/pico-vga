// Test Pattern - Precise port of pico-playground scanvideo/test_pattern
//
// Draws 7 horizontal color bands with 32 vertical grayscale bars
// Press SPACE to invert colors
//
// Build: tinygo build -target=pico -o test_pattern.uf2 .
//
// This port matches the C pico-extras scanvideo implementation exactly:
// - Same VGA timing (from vga_modes.c vga_mode_640x480_60)
// - Same PIO programs (video_24mhz_composable and timing.pio)
// - Same composable scanline format (COLOR_RUN)
// - Same draw_color_bar algorithm
package main

import (
	"device/rp"
	"machine"
	"runtime/volatile"
	"time"
	"unsafe"

	pio "github.com/tinygo-org/pio/rp2-pio"
)

// Pin assignments (Pimoroni VGA Demo Base)
const (
	pinHSYNC   = machine.GPIO16
	pinVSYNC   = machine.GPIO17
	pinRGBBase = machine.GPIO0
)

// VGA timing from pico-extras vga_modes.c: vga_timing_640x480_60_default
// These values MUST match the C source exactly
const (
	// Horizontal timing
	hActive     = 640
	hFrontPorch = 16
	hPulse      = 64  // C: h_pulse = 64 (NOT standard VESA 96)
	hTotal      = 800 // C: h_total = 800

	// Vertical timing
	vActive     = 480
	vFrontPorch = 1 // C: v_front_porch = 1 (NOT standard VESA 10)
	vPulse      = 2 // C: v_pulse = 2
	vTotal      = 523 // C: v_total = 523 (NOT standard VESA 525)

	// Sync polarities from C: both POSITIVE (active high = 1)
	hSyncPolarity = 1 // C: h_sync_polarity = 1
	vSyncPolarity = 1 // C: v_sync_polarity = 1

	// Clock divider: Must match working color_run example
	// color_run uses divider 4 and produces ~31260 Hz HSYNC
	// This gives 125 MHz / 4 = 31.25 MHz PIO clock
	pixelClockDiv = 4
)

// Pixel format: GVGA format with gap at bit 5
// R: bits 0-4, G: bits 6-10, B: bits 11-15
// Matches PICO_SCANVIDEO_PIXEL_FROM_RGB5 macro in C
func pixelFromRGB5(r, g, b uint32) uint16 {
	return uint16((b << 11) | (g << 6) | (r << 0))
}

// Composable scanline command offsets - MUST match C video_24mhz_composable
const (
	COMPOSABLE_EOL_SKIP_ALIGN = 0
	COMPOSABLE_EOL_ALIGN      = 1
	COMPOSABLE_COLOR_RUN      = 3
	COMPOSABLE_RAW_1P         = 11
)

// Timing state command indices (match C enum)
const (
	SET_IRQ_0          = 0 // Active scanline IRQ
	SET_IRQ_1          = 1 // Vblank scanline IRQ
	SET_IRQ_SCANLINE   = 2 // IRQ 4 - triggers scanline SM
	CLEAR_IRQ_SCANLINE = 3 // Clear IRQ 4
)

var timingInstructions [4]uint16

var timingState struct {
	vActive     int32
	vTotal      int32
	vPulseStart int32
	vPulseEnd   int32

	vsyncBitsPulse   uint32
	vsyncBitsNoPulse uint32

	a, aVblank   uint32
	b1, b2       uint32
	c, cVblank   uint32

	vsyncBits      uint32
	dmaStateIndex  uint16
	timingScanline int32
}

var dmaStates [4]uint32

// Scanline buffer - sized for 32 COLOR_RUN bars + terminator
// Each bar: 3 half-words (cmd=1, color=1, count=1)
// 32 bars * 3 = 96 half-words = 48 words
// Plus terminator: RAW_1P(1) + black(1) + EOL_SKIP_ALIGN(1) + pad(1) = 4 half-words = 2 words
// Total: 50 words (round up to 52 for safety)
const scanlineBufWords = 52

// Pre-build ALL 480 scanlines to eliminate timing jitter
var allScanlines [vActive][scanlineBufWords]uint32
var scanlineLen [vActive]int

// Blank scanline for vblank
var blankScanline [3]uint32
var blankScanlineLen = 3

var (
	videoPIO       *pio.PIO
	timingSM       pio.StateMachine
	scanlineSM     pio.StateMachine
	timingOffset   uint8
	scanlineOffset uint8
)

type dmaChannel struct {
	ReadAddr   volatile.Register32
	WriteAddr  volatile.Register32
	TransCount volatile.Register32
	CtrlTrig   volatile.Register32
}

func getDMAChannel(ch int) *dmaChannel {
	base := uintptr(0x50000000) + uintptr(ch)*0x40
	return (*dmaChannel)(unsafe.Pointer(base))
}

// Invert flag - toggled by spacebar (matches C static bool invert)
var invert volatile.Register32

func main() {
	time.Sleep(3 * time.Second)
	println("=== Test Pattern - Port of C scanvideo/test_pattern ===")
	println("VGA 640x480@60Hz (pico-extras timing)")
	println("7 color bands, 32 vertical bars")
	println("Press SPACE to invert, 'r' to reboot")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	if !initVideo() {
		println("Video init failed!")
		for {
			led.Low()
			time.Sleep(100 * time.Millisecond)
			led.High()
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Force GPIO 0-15 to PIO0 (FUNCSEL=6)
	for i := 0; i < 16; i++ {
		ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
		ctrlAddr.Set(6)
	}

	initBlankScanline()

	// Counters for status display
	var frameCount volatile.Register32
	var hsyncCount volatile.Register32
	var dmaCollisions volatile.Register32

	// Pre-build ALL scanlines BEFORE enabling video
	barWidth := hActive / 32
	println("Pre-building all", vActive, "scanlines...")
	println("Bar width:", barWidth, "pixels")
	println("COLOR_RUN count:", barWidth-3, "(bar_width - 3 per C formula)")
	buildAllScanlines()
	println("Done pre-building")

	// Debug: show first scanline structure
	println("Scanline 0 length:", scanlineLen[0], "words")
	p := (*[scanlineBufWords * 2]uint16)(unsafe.Pointer(&allScanlines[0][0]))
	println("First bar: cmd=", p[0], "color=", hex16(p[1]), "count=", p[2])
	println("Second bar: cmd=", p[3], "color=", hex16(p[4]), "count=", p[5])

	enableVideo(true)
	println("Video enabled")

	// Render loop - tight polling for IRQs
	go func() {
		line := 0
		lastInvert := invert.Get()

		for {
			// Keep timing FIFO fed (critical - do this frequently)
			topUpTimingFIFO()

			// Poll for IRQs
			irqs := rp.PIO0.IRQ.Get()

			// IRQ 0 = active scanline
			if irqs&1 != 0 {
				rp.PIO0.IRQ.Set(1) // Clear IRQ 0
				hsyncCount.Set(hsyncCount.Get() + 1)

				// Start DMA for this scanline
				if line < vActive {
					dma := getDMAChannel(0)
					if dma.CtrlTrig.Get()&(1<<24) != 0 {
						dmaCollisions.Set(dmaCollisions.Get() + 1)
					}
					dma.ReadAddr.Set(uint32(uintptr(unsafe.Pointer(&allScanlines[line][0]))))
					dma.TransCount.Set(uint32(scanlineLen[line]))
					dma.CtrlTrig.SetBits(1)
				}
				line++
			}

			// IRQ 1 = vblank scanline
			if irqs&2 != 0 {
				rp.PIO0.IRQ.Set(2) // Clear IRQ 1
				hsyncCount.Set(hsyncCount.Get() + 1)
				line++
			}

			// Frame complete
			if line >= vTotal {
				line = 0
				frameCount.Set(frameCount.Get() + 1)

				// Rebuild scanlines if invert changed (like C vblank check)
				currentInvert := invert.Get()
				if currentInvert != lastInvert {
					buildAllScanlines()
					lastInvert = currentInvert
				}
			}
		}
	}()

	// Main loop - handle input and status
	lastHsync := uint32(0)
	lastFrame := uint32(0)
	lastTime := time.Now()
	statusInterval := uint32(120) // ~2 seconds at 60Hz

	for {
		// Check for input
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			switch b {
			case ' ':
				if invert.Get() == 0 {
					invert.Set(1)
				} else {
					invert.Set(0)
				}
				println("Inverted:", invert.Get())
			case 'r', 'R':
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}

		// Status every ~2 seconds
		f := frameCount.Get()
		if f > 0 && f%statusInterval == 0 && f != lastFrame {
			now := time.Now()
			elapsedMs := now.Sub(lastTime).Milliseconds()
			if elapsedMs == 0 {
				elapsedMs = 1
			}

			h := hsyncCount.Get()
			hsyncHz := ((h - lastHsync) * 1000) / uint32(elapsedMs)
			vsyncHz := ((f - lastFrame) * 1000) / uint32(elapsedMs)
			linesPerFrame := uint32(0)
			if f > lastFrame {
				linesPerFrame = (h - lastHsync) / (f - lastFrame)
			}

			println("HSYNC:", hsyncHz, "Hz, VSYNC:", vsyncHz, "Hz, lines/frame:", linesPerFrame)
			println("DMA collisions:", dmaCollisions.Get())

			lastHsync = h
			lastFrame = f
			lastTime = now
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// buildAllScanlines pre-builds all scanlines (eliminates timing jitter)
func buildAllScanlines() {
	for line := 0; line < vActive; line++ {
		scanlineLen[line] = drawColorBar(line, allScanlines[line][:])
	}
}

// drawColorBar - EXACT port of C draw_color_bar function from test_pattern.c
//
// C code:
//   uint line_num = scanvideo_scanline_number(buffer->scanline_id);
//   uint32_t primary_color = 1u + (line_num * 7 / vga_mode.height);
//   uint32_t color_mask = PICO_SCANVIDEO_PIXEL_FROM_RGB5(0x1f * (primary_color & 1u),
//                         0x1f * ((primary_color >> 1u) & 1u), 0x1f * ((primary_color >> 2u) & 1u));
//   uint bar_width = vga_mode.width / 32;
//   for (uint bar = 0; bar < 32; bar++) {
//       *p++ = COMPOSABLE_COLOR_RUN;
//       uint32_t color = PICO_SCANVIDEO_PIXEL_FROM_RGB5(bar, bar, bar);
//       *p++ = (color & color_mask) ^ invert_bits;
//       *p++ = bar_width - 3;
//   }
//   *p++ = COMPOSABLE_RAW_1P;
//   *p++ = 0;
//   *p++ = COMPOSABLE_EOL_SKIP_ALIGN;
//   *p++ = 0;
func drawColorBar(lineNum int, buf []uint32) int {
	// Calculate primary color (1-7) based on vertical position
	// C: primary_color = 1u + (line_num * 7 / vga_mode.height)
	primaryColor := uint32(1 + (lineNum * 7 / vActive))

	// Create color mask from primary color bits
	// C: color_mask = PICO_SCANVIDEO_PIXEL_FROM_RGB5(0x1f * (primary_color & 1u), ...)
	rMask := uint32(0x1f) * (primaryColor & 1)
	gMask := uint32(0x1f) * ((primaryColor >> 1) & 1)
	bMask := uint32(0x1f) * ((primaryColor >> 2) & 1)
	colorMask := uint32(pixelFromRGB5(rMask, gMask, bMask))

	// Bar width: C: bar_width = vga_mode.width / 32
	barWidth := hActive / 32 // 640/32 = 20

	// Invert bits: C: invert_bits = invert ? PICO_SCANVIDEO_PIXEL_FROM_RGB5(0x1f,0x1f,0x1f) : 0
	invertBits := uint32(0)
	if invert.Get() != 0 {
		invertBits = uint32(pixelFromRGB5(0x1f, 0x1f, 0x1f))
	}

	// Write as 16-bit values (matches C uint16_t *p)
	p := (*[scanlineBufWords * 2]uint16)(unsafe.Pointer(&buf[0]))
	idx := 0

	// C: for (uint bar = 0; bar < 32; bar++)
	for bar := uint32(0); bar < 32; bar++ {
		// C: *p++ = COMPOSABLE_COLOR_RUN
		p[idx] = COMPOSABLE_COLOR_RUN
		idx++
		// C: color = PICO_SCANVIDEO_PIXEL_FROM_RGB5(bar, bar, bar)
		// C: *p++ = (color & color_mask) ^ invert_bits
		color := uint32(pixelFromRGB5(bar, bar, bar))
		p[idx] = uint16((color & colorMask) ^ invertBits)
		idx++
		// C: *p++ = bar_width - 3
		p[idx] = uint16(barWidth - 3)
		idx++
	}

	// 32 * 3 = 96 half-words (48 words) - should be word aligned
	// C: assert(!(3u & (uintptr_t) p));

	// C: *p++ = COMPOSABLE_RAW_1P; *p++ = 0;  // black pixel to end line
	p[idx] = COMPOSABLE_RAW_1P
	idx++
	p[idx] = 0 // black
	idx++

	// C: *p++ = COMPOSABLE_EOL_SKIP_ALIGN; *p++ = 0;  // end of line with alignment
	p[idx] = COMPOSABLE_EOL_SKIP_ALIGN
	idx++
	p[idx] = 0
	idx++

	// Return number of 32-bit words used
	// idx is half-words, divide by 2 and round up
	return (idx + 1) / 2
}

func initVideo() bool {
	videoPIO = pio.PIO0

	// Build timing instructions (IRQ instructions executed via out exec)
	asm := pio.AssemblerV0{}
	timingInstructions[SET_IRQ_0] = asm.IRQSet(false, 0).Encode()
	timingInstructions[SET_IRQ_1] = asm.IRQSet(false, 1).Encode()
	timingInstructions[SET_IRQ_SCANLINE] = asm.IRQSet(false, 4).Encode()
	timingInstructions[CLEAR_IRQ_SCANLINE] = asm.IRQClear(false, 4).Encode()

	initTimingState()

	// Load scanline program at offset 0 (MUST be at 0 for dispatch jumps)
	scanlineProgram := buildScanlineProgram()
	offset, err := videoPIO.AddProgram(scanlineProgram, 0)
	if err != nil {
		println("Failed to load scanline program:", err.Error())
		return false
	}
	scanlineOffset = offset
	println("Scanline program at offset", scanlineOffset)

	// Load timing program at offset 16
	const timingProgramOffsetVal = 16
	timingProgram := buildTimingProgram()
	offset, err = videoPIO.AddProgram(timingProgram, timingProgramOffsetVal)
	if err != nil {
		println("Failed to load timing program:", err.Error())
		return false
	}
	timingOffset = offset
	println("Timing program at offset", timingOffset)

	// Configure scanline SM (SM0)
	scanlineSM = videoPIO.StateMachine(0)
	scanlineSM.TryClaim()

	scanlineCfg := pio.DefaultStateMachineConfig()
	scanlineCfg.SetOutPins(pinRGBBase, 16)
	scanlineCfg.SetOutShift(true, true, 32) // Shift right, autopull
	scanlineCfg.SetFIFOJoin(pio.FifoJoinTx) // 8-deep TX FIFO
	// Clock divider for 25 MHz pixel clock (125 MHz / 5 = 25 MHz)
	scanlineCfg.SetClkDivIntFrac(pixelClockDiv, 0)
	// Enable sticky output - continuously assert OUT pins
	scanlineCfg.SetOutSpecial(true, false, 0)

	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	}
	scanlineSM.SetPindirsConsecutive(pinRGBBase, 16, true)
	scanlineSM.Init(scanlineOffset+1, scanlineCfg) // Start at wait IRQ

	// Configure timing SM (SM3)
	timingSM = videoPIO.StateMachine(3)
	timingSM.TryClaim()

	timingCfg := pio.DefaultStateMachineConfig()
	timingCfg.SetOutPins(pinHSYNC, 2) // HSYNC, VSYNC
	timingCfg.SetOutShift(true, true, 32)
	// Timing SM uses same clock divider for synchronization
	timingCfg.SetClkDivIntFrac(pixelClockDiv, 0)
	// Wrap: from jmp (offset+5) back to new_state (offset+1)
	timingCfg.SetWrap(timingOffset+1, timingOffset+5)

	pinHSYNC.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	pinVSYNC.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	timingSM.SetPindirsConsecutive(pinHSYNC, 2, true)
	timingSM.Init(timingOffset, timingCfg)

	setupDMA()
	return true
}

// initTimingState initializes timing state values (matches C init_timing_state)
func initTimingState() {
	timingState.vTotal = vTotal
	timingState.vActive = vActive
	timingState.vPulseStart = int32(vActive + vFrontPorch)
	timingState.vPulseEnd = timingState.vPulseStart + vPulse

	// VSYNC bit in timing word (bit 30 -> pin bit 1 after shift)
	const vsyncBit = 0x40000000

	// VSYNC polarity handling (matches C code EXACTLY)
	// C: vsync_bits_pulse = timing->v_sync_polarity ? 0 : vsync_bit;
	// C: vsync_bits_no_pulse = timing->v_sync_polarity ? vsync_bit : 0;
	if vSyncPolarity != 0 {
		// Active high: during pulse output LOW (0), outside pulse output HIGH (vsyncBit)
		timingState.vsyncBitsPulse = 0
		timingState.vsyncBitsNoPulse = vsyncBit
	} else {
		// Active low: during pulse output HIGH (vsyncBit), outside pulse output LOW (0)
		timingState.vsyncBitsPulse = vsyncBit
		timingState.vsyncBitsNoPulse = 0
	}

	// HSYNC bit (bit 29 -> pin bit 0)
	// Match C code logic: polarity affects output during pulse
	var hSyncBit uint32
	if hSyncPolarity == 0 {
		hSyncBit = 1 // Active low: output 1 during pulse
	} else {
		hSyncBit = 0 // Active high: output 0 during pulse
	}
	hSyncNoBit := 1 - hSyncBit

	// Calculate back porch
	hBackPorch := hTotal - hActive - hFrontPorch - hPulse
	hActiveAndFront := hActive + hFrontPorch

	// Encode timing states (matches C timing_encode macro)
	// Format: instruction(16) | (cycles-3)(13) | pins(3)
	const TIMING_CYCLE = 3
	timingState.a = timingEncode(SET_IRQ_0, 4, hSyncBit)
	timingState.aVblank = timingEncode(SET_IRQ_1, 4, hSyncBit)
	timingState.b1 = timingEncode(CLEAR_IRQ_SCANLINE, hPulse-4, hSyncBit)
	timingState.b2 = timingEncode(CLEAR_IRQ_SCANLINE, hBackPorch, hSyncNoBit)
	timingState.c = timingEncode(SET_IRQ_SCANLINE, hActiveAndFront, 4|hSyncNoBit) // DEN bit
	timingState.cVblank = timingEncode(CLEAR_IRQ_SCANLINE, hActiveAndFront, hSyncNoBit)

	// Initialize state
	setupDmaStatesVblank()
	timingState.vsyncBits = timingState.vsyncBitsNoPulse
	timingState.dmaStateIndex = 0
	timingState.timingScanline = 0

	println("Timing: hBackPorch=", hBackPorch, "hActiveAndFront=", hActiveAndFront)
	println("Sync polarity: H=", hSyncPolarity, "V=", vSyncPolarity)
	_ = TIMING_CYCLE
}

func timingEncode(cmd int, cycles int, pins uint32) uint32 {
	const TIMING_CYCLE = 3
	return uint32(timingInstructions[cmd]) | (uint32(cycles-TIMING_CYCLE) << 16) | (pins << 29)
}

func setupDmaStatesVblank() {
	dmaStates[0] = timingState.aVblank
	dmaStates[1] = timingState.b1
	dmaStates[2] = timingState.b2
	dmaStates[3] = timingState.cVblank
}

func setupDmaStatesActive() {
	dmaStates[0] = timingState.a
	dmaStates[1] = timingState.b1
	dmaStates[2] = timingState.b2
	dmaStates[3] = timingState.c
}

// topUpTimingFIFO feeds timing data to timing SM (matches C top_up_timing_pio_fifo)
func topUpTimingFIFO() {
	for !timingSM.IsTxFIFOFull() {
		timingSM.TxPut(dmaStates[timingState.dmaStateIndex] | timingState.vsyncBits)

		timingState.dmaStateIndex++
		if timingState.dmaStateIndex >= 4 {
			timingState.dmaStateIndex = 0
			timingState.timingScanline++

			// Vertical timing state transitions
			if timingState.timingScanline >= timingState.vActive {
				if timingState.timingScanline >= timingState.vTotal {
					timingState.timingScanline = 0
					setupDmaStatesActive()
				} else if timingState.timingScanline == timingState.vActive {
					setupDmaStatesVblank()
				} else if timingState.timingScanline == timingState.vPulseStart {
					timingState.vsyncBits = timingState.vsyncBitsPulse
				} else if timingState.timingScanline == timingState.vPulseEnd {
					timingState.vsyncBits = timingState.vsyncBitsNoPulse
				}
			}
		}
	}
}

// buildTimingProgram creates timing PIO program (matches C timing.pio)
func buildTimingProgram() []uint16 {
	asm := pio.AssemblerV0{}
	return []uint16{
		asm.Pull(false, true).Encode(),        // 0: entry_point - pull block
		// .wrap_target here (relative offset 1)
		asm.Out(pio.OutDestExec, 16).Encode(), // 1: new_state - execute IRQ instruction
		asm.Out(pio.OutDestX, 13).Encode(),    // 2: load cycle count into X
		asm.Out(pio.OutDestPins, 3).Encode(),  // 3: output sync pins
		// loop: 2-cycle delay (nop + jmp) - matches C timing.pio
		asm.Nop().Encode(),                    // 4: nop
		asm.Jmp(pio.JmpXNZeroDec, 4).Encode(), // 5: jmp x-- loop
		// .wrap here
	}
}

// buildScanlineProgram creates composable scanline PIO program
// Matches C pico-extras video_24mhz_composable.pio
func buildScanlineProgram() []uint16 {
	asm := pio.AssemblerV0{}
	const delay1 = 1 << 8 // 1 cycle delay

	return []uint16{
		asm.Out(pio.OutDestNull, 32).Encode(),          // 0: end_of_scanline_skip_ALIGN
		asm.WaitIRQ(true, false, 4).Encode(),           // 1: entry_point - wait for IRQ 4
		asm.Out(pio.OutDestPC, 16).Encode(),            // 2: dispatch

		// color_run (3-6): outputs color, then loops for count cycles
		asm.Out(pio.OutDestPins, 16).Encode(),          // 3: output color
		asm.Out(pio.OutDestX, 16).Encode(),             // 4: load count
		asm.Jmp(pio.JmpXNZeroDec, 5).Encode() | delay1, // 5: color_loop [1]
		asm.Out(pio.OutDestPC, 16).Encode() | delay1,   // 6: next command [1]

		// raw_run (7-10): outputs individual pixels
		asm.Out(pio.OutDestPins, 16).Encode(),          // 7: first pixel
		asm.Out(pio.OutDestX, 16).Encode(),             // 8: load count
		asm.Out(pio.OutDestPins, 16).Encode(),          // 9: pixel_loop
		asm.Jmp(pio.JmpXNZeroDec, 9).Encode(),          // 10: loop

		// raw_1p (11-12): output single pixel then dispatch
		asm.Out(pio.OutDestPins, 16).Encode(),          // 11: output pixel
		asm.Out(pio.OutDestPC, 16).Encode(),            // 12: next command

		// raw_2p (13): output first pixel, wraps to raw_1p
		asm.Out(pio.OutDestPins, 16).Encode() | delay1, // 13: first pixel [1]

		// raw_1p_skip_ALIGN (14-15)
		asm.Out(pio.OutDestPins, 32).Encode(),          // 14: skip align
		asm.Out(pio.OutDestPC, 16).Encode(),            // 15: next command
	}
}

func initBlankScanline() {
	// Black scanline using COLOR_RUN
	blankScanline[0] = uint32(COMPOSABLE_COLOR_RUN) | (0 << 16)
	blankScanline[1] = uint32(hActive-3) | (uint32(COMPOSABLE_RAW_1P) << 16)
	blankScanline[2] = 0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16)
	blankScanlineLen = 3
}

func enableVideo(enable bool) {
	timingSM.SetEnabled(false)
	scanlineSM.SetEnabled(false)

	if enable {
		// Prime timing FIFO
		for i := 0; i < 8; i++ {
			topUpTimingFIFO()
		}

		// Pre-fill scanline FIFO with blank data
		scanlineSM.TxPut(uint32(COMPOSABLE_COLOR_RUN) | (0 << 16))
		scanlineSM.TxPut(uint32(hActive-3) | (uint32(COMPOSABLE_RAW_1P) << 16))
		scanlineSM.TxPut(0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16))

		// Enable IRQ sources
		rp.PIO0.IRQ0_INTE.SetBits(0x03)         // IRQ 0 and IRQ 1
		rp.PIO0.IRQ1_INTE.SetBits(1 << (8 + 3)) // SM3 TX not full
		videoPIO.ClearIRQ(0xFF)

		// Force SMs to correct entry points
		scanlineSM.Exec(encodeJmp(scanlineOffset + 1)) // Wait IRQ
		timingSM.Exec(encodeJmp(timingOffset))         // Pull

		// Enable SMs
		timingSM.SetEnabled(true)
		scanlineSM.SetEnabled(true)
	}
}

func encodeJmp(addr uint8) uint16 {
	return uint16(addr & 0x1F)
}

func setupDMA() {
	// Enable DMA
	rp.RESETS.RESET.ClearBits(rp.RESETS_RESET_DMA)
	for !rp.RESETS.RESET_DONE.HasBits(rp.RESETS_RESET_DONE_DMA) {
	}

	dma := getDMAChannel(0)
	txFifo := uint32(0x50200010) // PIO0 TXF0

	ctrl := uint32(0)
	ctrl |= 1 << 0  // Enable
	ctrl |= 1 << 1  // High priority
	ctrl |= 2 << 2  // 32-bit transfers
	ctrl |= 1 << 4  // Increment read address
	ctrl |= 0 << 15 // DREQ = PIO0_TX0

	dma.WriteAddr.Set(txFifo)
	dma.CtrlTrig.Set(ctrl & ^uint32(1)) // Configure but don't start
}

func hex(v uint32) string {
	digits := "0123456789ABCDEF"
	result := make([]byte, 10)
	result[0] = '0'
	result[1] = 'x'
	for i := 0; i < 8; i++ {
		result[9-i] = digits[v&0xF]
		v >>= 4
	}
	return string(result)
}

func hex16(v uint16) string {
	digits := "0123456789ABCDEF"
	result := make([]byte, 6)
	result[0] = '0'
	result[1] = 'x'
	for i := 0; i < 4; i++ {
		result[5-i] = digits[v&0xF]
		v >>= 4
	}
	return string(result)
}
