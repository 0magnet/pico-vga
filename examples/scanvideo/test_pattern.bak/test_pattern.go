// Test Pattern - Port of pico-playground scanvideo/test_pattern
// Draws 7 horizontal color bands with 32 vertical grayscale bars
// Press SPACE to invert colors
//
// Build: tinygo build -target=pico -o test_pattern.uf2 .
//
// Using polling-based approach with optimizations:
// - Pre-build all scanlines before enabling video
// - Use tight polling loop on dedicated core
// - Minimize work in critical path
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

// VGA 640x480@60Hz timing
const (
	vgaWidth  = 640
	vgaHeight = 480

	hActive     = 640
	hFrontPorch = 16
	hPulse      = 96
	hTotal      = 800

	vActive     = 480
	vFrontPorch = 10
	vPulse      = 2
	vTotal      = 525

	hSyncPolarity = 0 // Active low
	vSyncPolarity = 0 // Active low
)

// Pixel format: RGB with gap at bit 5 (matches pico-extras DPI)
// R: bits 0-4, G: bits 6-10, B: bits 11-15
const (
	pixelRShift = 0
	pixelGShift = 6
	pixelBShift = 11
)

func pixelFromRGB5(r, g, b uint32) uint16 {
	return uint16((b << pixelBShift) | (g << pixelGShift) | (r << pixelRShift))
}

// Composable scanline commands
const (
	COMPOSABLE_EOL_SKIP_ALIGN = 0
	COMPOSABLE_EOL_ALIGN      = 1
	COMPOSABLE_COLOR_RUN      = 3
	COMPOSABLE_RAW_RUN        = 7
	COMPOSABLE_RAW_1P         = 11
)

// Timing state command indices
const (
	SET_IRQ_0          = 0
	SET_IRQ_1          = 1
	SET_IRQ_SCANLINE   = 2
	CLEAR_IRQ_SCANLINE = 3
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

// Scanline buffer - enough for 32 COLOR_RUN bars + terminator
// Each bar: 3 half-words (cmd, color, count) = 1.5 words
// 32 bars * 1.5 = 48 words + 2 words terminator = 50 words
const scanlineBufWords = 52

// Pre-build ALL 480 scanlines to eliminate timing jitter
// This uses ~96KB RAM but ensures DMA can start immediately
var allScanlines [vgaHeight][scanlineBufWords]uint32
var scanlineLen [vgaHeight]int

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

// Invert flag - toggled by spacebar
var invert volatile.Register32

func main() {
	time.Sleep(3 * time.Second)
	println("=== Test Pattern - 640x480@60Hz ===")
	println("7 horizontal color bands, 32 vertical bars")
	println("Press SPACE to invert colors")

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

	// Force GPIO 0-15 to PIO0
	for i := 0; i < 16; i++ {
		ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
		ctrlAddr.Set(6)
	}

	initBlankScanline()

	// Counters
	var frameCount volatile.Register32
	var hsyncCount volatile.Register32
	var dmaCollisions volatile.Register32

	// Pre-build ALL scanlines BEFORE enabling video
	println("Pre-building all 480 scanlines...")
	println("Bar width:", vgaWidth/32, "pixels, COLOR_RUN count:", vgaWidth/32)
	buildAllScanlines()
	println("Done pre-building")

	enableVideo(true)
	println("Video enabled - polling mode")

	// Render loop on core1 - tight polling for IRQs
	go func() {
		line := 0
		lastInvert := invert.Get()

		for {
			// Poll for IRQs - check as fast as possible
			irqs := rp.PIO0.IRQ.Get()

			// IRQ 0 = active scanline
			if irqs&1 != 0 {
				rp.PIO0.IRQ.Set(1) // Clear IRQ 0

				// Start DMA immediately
				if line < vgaHeight {
					dma := getDMAChannel(0)
					if dma.CtrlTrig.Get()&(1<<24) != 0 {
						dmaCollisions.Set(dmaCollisions.Get() + 1)
					}
					dma.ReadAddr.Set(uint32(uintptr(unsafe.Pointer(&allScanlines[line][0]))))
					dma.TransCount.Set(uint32(scanlineLen[line]))
					dma.CtrlTrig.SetBits(1)
				}
				hsyncCount.Set(hsyncCount.Get() + 1)
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

				// Rebuild if invert changed
				currentInvert := invert.Get()
				if currentInvert != lastInvert {
					buildAllScanlines()
					lastInvert = currentInvert
				}
			}

			// Keep timing FIFO fed
			topUpTimingFIFO()
		}
	}()

	// Main loop - handle input and status
	lastHsync := uint32(0)
	lastFrame := uint32(0)
	lastTime := time.Now()

	for {
		// Check for spacebar to invert
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == ' ' {
				if invert.Get() == 0 {
					invert.Set(1)
				} else {
					invert.Set(0)
				}
				println("Inverted:", invert.Get())
			} else if b == 'r' || b == 'R' {
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}

		// Status every 2 seconds
		f := frameCount.Get()
		if f > 0 && f%120 == 0 && f != lastFrame {
			now := time.Now()
			elapsedMs := now.Sub(lastTime).Milliseconds()
			if elapsedMs == 0 {
				elapsedMs = 1
			}

			h := hsyncCount.Get()
			hsyncHz := ((h - lastHsync) * 1000) / uint32(elapsedMs)
			vsyncHz := ((f - lastFrame) * 1000) / uint32(elapsedMs)

			println("HSYNC:", hsyncHz, "Hz, VSYNC:", vsyncHz, "Hz")
			println("DMA collisions:", dmaCollisions.Get())
			println("Buffer[0]:", hex(allScanlines[0][0]), hex(allScanlines[0][1]), hex(allScanlines[0][2]))
			println("scanlineLen[0]=", scanlineLen[0], "scanlineLen[240]=", scanlineLen[240])

			lastHsync = h
			lastFrame = f
			lastTime = now
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// buildAllScanlines pre-builds all 480 scanlines
// This eliminates timing jitter by ensuring buffers are ready before display
func buildAllScanlines() {
	for line := 0; line < vgaHeight; line++ {
		scanlineLen[line] = drawColorBar(line, allScanlines[line][:])
	}
}

// drawColorBar - port of C draw_color_bar function
// Creates 32 vertical bars with color based on horizontal position
// and primary color based on vertical position (7 bands)
func drawColorBar(lineNum int, buf []uint32) int {
	return drawColorBarProper(lineNum, buf)
}

func drawColorBarProper(lineNum int, buf []uint32) int {
	// Calculate primary color (1-7) based on vertical position
	primaryColor := uint32(1 + (lineNum * 7 / vgaHeight))

	// Create color mask from primary color bits
	rMask := uint32(0x1f) * (primaryColor & 1)
	gMask := uint32(0x1f) * ((primaryColor >> 1) & 1)
	bMask := uint32(0x1f) * ((primaryColor >> 2) & 1)
	colorMask := uint32(pixelFromRGB5(rMask, gMask, bMask))

	barWidth := vgaWidth / 32 // 20 pixels per bar

	// COLOR_RUN count: bar_width - 3 accounts for PIO overhead
	// The - 3 matches the C formula from pico-extras
	colorRunCount := uint16(barWidth - 3)

	invertBits := uint32(0)
	if invert.Get() != 0 {
		invertBits = uint32(pixelFromRGB5(0x1f, 0x1f, 0x1f))
	}

	// Write as 16-bit values using a uint16 slice
	p := (*[scanlineBufWords * 2]uint16)(unsafe.Pointer(&buf[0]))
	idx := 0

	for bar := uint32(0); bar < 32; bar++ {
		p[idx] = COMPOSABLE_COLOR_RUN
		idx++
		color := uint32(pixelFromRGB5(bar, bar, bar))
		p[idx] = uint16((color & colorMask) ^ invertBits)
		idx++
		p[idx] = colorRunCount
		idx++
	}

	// 32 * 3 = 96 half-words, should be word aligned
	// Black pixel to end line
	p[idx] = COMPOSABLE_RAW_1P
	idx++
	p[idx] = 0 // black
	idx++

	// End of line with alignment padding
	p[idx] = COMPOSABLE_EOL_SKIP_ALIGN
	idx++
	p[idx] = 0
	idx++

	// Return number of 32-bit words used
	return (idx + 1) / 2
}

func initVideo() bool {
	videoPIO = pio.PIO0

	asm := pio.AssemblerV0{}
	timingInstructions[SET_IRQ_0] = asm.IRQSet(false, 0).Encode()
	timingInstructions[SET_IRQ_1] = asm.IRQSet(false, 1).Encode()
	timingInstructions[SET_IRQ_SCANLINE] = asm.IRQSet(false, 4).Encode()
	timingInstructions[CLEAR_IRQ_SCANLINE] = asm.IRQClear(false, 4).Encode()

	initTimingState()

	// Load scanline program at offset 0
	scanlineProgram := buildScanlineProgram()
	offset, err := videoPIO.AddProgram(scanlineProgram, 0)
	if err != nil {
		println("Failed to load scanline program:", err.Error())
		return false
	}
	scanlineOffset = offset

	// Load timing program at offset 16
	const timingProgramOffset = 16
	timingProgram := buildTimingProgram()
	offset, err = videoPIO.AddProgram(timingProgram, timingProgramOffset)
	if err != nil {
		println("Failed to load timing program:", err.Error())
		return false
	}
	timingOffset = offset

	// Configure scanline SM (SM0)
	scanlineSM = videoPIO.StateMachine(0)
	scanlineSM.TryClaim()

	scanlineCfg := pio.DefaultStateMachineConfig()
	scanlineCfg.SetOutPins(pinRGBBase, 16)
	scanlineCfg.SetOutShift(true, true, 32)
	scanlineCfg.SetFIFOJoin(pio.FifoJoinTx)
	// Scanline SM clock divider: reduce from 4 to 2 to speed up pixel output
	// This makes COLOR_RUN output pixels at 2x rate, filling more of the active region
	scanlineCfg.SetClkDivIntFrac(2, 0)
	scanlineCfg.SetOutSpecial(true, false, 0)

	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	}
	scanlineSM.SetPindirsConsecutive(pinRGBBase, 16, true)
	scanlineSM.Init(scanlineOffset+1, scanlineCfg)

	// Configure timing SM (SM3)
	timingSM = videoPIO.StateMachine(3)
	timingSM.TryClaim()

	timingCfg := pio.DefaultStateMachineConfig()
	timingCfg.SetOutPins(pinHSYNC, 2)
	timingCfg.SetOutShift(true, true, 32)
	timingCfg.SetClkDivIntFrac(4, 0)
	timingCfg.SetWrap(timingOffset+1, timingOffset+5)

	pinHSYNC.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	pinVSYNC.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	timingSM.SetPindirsConsecutive(pinHSYNC, 2, true)
	timingSM.Init(timingOffset, timingCfg)

	setupDMA()
	return true
}

func initTimingState() {
	timingState.vTotal = vTotal
	timingState.vActive = vActive
	timingState.vPulseStart = int32(vActive + vFrontPorch)
	timingState.vPulseEnd = timingState.vPulseStart + vPulse

	const vsyncBit = 0x40000000
	if vSyncPolarity != 0 {
		timingState.vsyncBitsPulse = 0
		timingState.vsyncBitsNoPulse = vsyncBit
	} else {
		timingState.vsyncBitsPulse = vsyncBit
		timingState.vsyncBitsNoPulse = 0
	}

	var hSyncBit uint32
	if hSyncPolarity == 0 {
		hSyncBit = 1
	}
	hSyncNoBit := 1 - hSyncBit

	hBackPorch := hTotal - hActive - hFrontPorch - hPulse
	hActiveAndFront := hActive + hFrontPorch

	timingState.a = timingEncode(SET_IRQ_0, 4, hSyncBit)
	timingState.aVblank = timingEncode(SET_IRQ_1, 4, hSyncBit)
	timingState.b1 = timingEncode(CLEAR_IRQ_SCANLINE, hPulse-4, hSyncBit)
	timingState.b2 = timingEncode(CLEAR_IRQ_SCANLINE, hBackPorch, hSyncNoBit)
	timingState.c = timingEncode(SET_IRQ_SCANLINE, hActiveAndFront, 4|hSyncNoBit)
	timingState.cVblank = timingEncode(CLEAR_IRQ_SCANLINE, hActiveAndFront, hSyncNoBit)

	setupDmaStatesVblank()
	timingState.vsyncBits = timingState.vsyncBitsNoPulse
	timingState.dmaStateIndex = 0
	timingState.timingScanline = 0
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

func topUpTimingFIFO() {
	for !timingSM.IsTxFIFOFull() {
		timingSM.TxPut(dmaStates[timingState.dmaStateIndex] | timingState.vsyncBits)

		timingState.dmaStateIndex++
		if timingState.dmaStateIndex >= 4 {
			timingState.dmaStateIndex = 0
			timingState.timingScanline++

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

func buildTimingProgram() []uint16 {
	asm := pio.AssemblerV0{}
	return []uint16{
		asm.Pull(false, true).Encode(),
		asm.Out(pio.OutDestExec, 16).Encode(),
		asm.Out(pio.OutDestX, 13).Encode(),
		asm.Out(pio.OutDestPins, 3).Encode(),
		asm.Nop().Encode(),
		asm.Jmp(pio.JmpXNZeroDec, 4).Encode(),
	}
}

func buildScanlineProgram() []uint16 {
	asm := pio.AssemblerV0{}
	const delay1 = 1 << 8

	return []uint16{
		asm.Out(pio.OutDestNull, 32).Encode(),
		asm.WaitIRQ(true, false, 4).Encode(),
		asm.Out(pio.OutDestPC, 16).Encode(),
		asm.Out(pio.OutDestPins, 16).Encode(),
		asm.Out(pio.OutDestX, 16).Encode(),
		asm.Jmp(pio.JmpXNZeroDec, 5).Encode() | delay1,
		asm.Out(pio.OutDestPC, 16).Encode() | delay1,
		asm.Out(pio.OutDestPins, 16).Encode(),
		asm.Out(pio.OutDestX, 16).Encode(),
		asm.Out(pio.OutDestPins, 16).Encode(),
		asm.Jmp(pio.JmpXNZeroDec, 9).Encode(),
		asm.Out(pio.OutDestPins, 16).Encode(),
		asm.Out(pio.OutDestPC, 16).Encode(),
		asm.Out(pio.OutDestPins, 16).Encode() | delay1,
		asm.Out(pio.OutDestPins, 32).Encode(),
		asm.Out(pio.OutDestPC, 16).Encode(),
	}
}

func initBlankScanline() {
	blankScanline[0] = uint32(COMPOSABLE_COLOR_RUN) | (0 << 16)
	blankScanline[1] = uint32(vgaWidth-3) | (uint32(COMPOSABLE_RAW_1P) << 16)
	blankScanline[2] = 0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16)
	blankScanlineLen = 3
}

func enableVideo(enable bool) {
	timingSM.SetEnabled(false)
	scanlineSM.SetEnabled(false)

	if enable {
		for i := 0; i < 8; i++ {
			topUpTimingFIFO()
		}

		// Pre-fill scanline FIFO
		scanlineSM.TxPut(uint32(COMPOSABLE_COLOR_RUN) | (0 << 16))
		scanlineSM.TxPut(uint32(vgaWidth-3) | (uint32(COMPOSABLE_RAW_1P) << 16))
		scanlineSM.TxPut(0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16))

		rp.PIO0.IRQ0_INTE.SetBits(0x03)
		rp.PIO0.IRQ1_INTE.SetBits(1 << (8 + 3))
		videoPIO.ClearIRQ(0xFF)

		scanlineSM.Exec(encodeJmp(scanlineOffset + 1))
		timingSM.Exec(encodeJmp(timingOffset))

		timingSM.SetEnabled(true)
		scanlineSM.SetEnabled(true)
	}
}

func encodeJmp(addr uint8) uint16 {
	return uint16(addr & 0x1F)
}

func setupDMA() {
	rp.RESETS.RESET.ClearBits(rp.RESETS_RESET_DMA)
	for !rp.RESETS.RESET_DONE.HasBits(rp.RESETS_RESET_DONE_DMA) {
	}

	dma := getDMAChannel(0)
	txFifo := uint32(0x50200010)

	ctrl := uint32(0)
	ctrl |= 1 << 0
	ctrl |= 1 << 1
	ctrl |= 2 << 2
	ctrl |= 1 << 4
	ctrl |= 0 << 15

	dma.WriteAddr.Set(txFifo)
	dma.CtrlTrig.Set(ctrl & ^uint32(1))
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

func startDMA(buf unsafe.Pointer, count int) {
	dma := getDMAChannel(0)
	dma.ReadAddr.Set(uint32(uintptr(buf)))
	dma.TransCount.Set(uint32(count))
	dma.CtrlTrig.SetBits(1)
}
