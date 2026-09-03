// Test Pattern - Color bars demo ported from pico-extras scanvideo
// 640x480@60Hz using pico-extras scanvideo architecture
//
// Draws 7 horizontal color bands (red, green, yellow, blue, magenta, cyan, white)
// with 32 vertical grayscale bars masked by the band color.
// Press SPACE to invert colors.
//
// Build with: tinygo build -target=pico -o test_pattern.uf2 scanvideo/test_pattern.go
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

// VGA timing for 640x480@60Hz
const (
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

// Composable scanline command offsets
const (
	COMPOSABLE_EOL_SKIP_ALIGN = 0
	COMPOSABLE_EOL_ALIGN      = 1
	COMPOSABLE_COLOR_RUN      = 3
	COMPOSABLE_RAW_RUN        = 7
	COMPOSABLE_RAW_1P         = 11
)

// Color format: GVGA (R:0-4, gap:5, G:6-10, B:11-15)
func pixelFromRGB5(r, g, b uint32) uint16 {
	return uint16((b << 11) | (g << 6) | (r << 0))
}

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

// Scanline buffer - enough for COLOR_RUN commands
// 32 bars * 3 words each + 4 words for EOL = 100 words max
const scanlineBufWords = 100

var scanlineBufs [2][scanlineBufWords]uint32

// Blank scanline for vblank
var blankScanline [3]uint32

// PIO state machines
var (
	videoPIO       *pio.PIO
	timingSM       pio.StateMachine
	scanlineSM     pio.StateMachine
	timingOffset   uint8
	scanlineOffset uint8
)

// Invert flag (toggled with SPACE)
var invert bool

// DMA channel
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

func main() {
	time.Sleep(1 * time.Second)
	println("=== Test Pattern 640x480@60Hz ===")
	println("Color bars demo - press SPACE to invert")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	// Force GPIO 0-15 to PIO0 function
	println("Forcing GPIO 0-15 to PIO0 function...")
	for i := 0; i < 16; i++ {
		ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
		ctrlAddr.Set(6) // PIO0 = FUNCSEL 6
	}

	if !initVideo() {
		println("Video init failed!")
		for {
			led.Low()
			time.Sleep(100 * time.Millisecond)
			led.High()
			time.Sleep(100 * time.Millisecond)
		}
	}

	initBlankScanline()

	// Pre-build first scanline
	buildScanline(0, &scanlineBufs[0])

	enableVideo(true)
	println("Video enabled")
	println("Press 'r' to reboot, SPACE to invert colors")

	// Counters
	var frameCount volatile.Register32
	var hsyncCount volatile.Register32

	// Pre-build first scanline
	buildScanline(0, &scanlineBufs[0])

	// Render loop on core1
	var inVblank volatile.Register32

	go func() {
		line := 0
		displayBuf := 0

		for {
			topUpTimingFIFO()

			irqs := videoPIO.GetIRQ()

			if irqs&1 != 0 {
				videoPIO.ClearIRQ(1)
				hsyncCount.Set(hsyncCount.Get() + 1)
				inVblank.Set(0) // In active region

				if line >= 0 && line < vActive {
					startDMA(unsafe.Pointer(&scanlineBufs[displayBuf][0]), 3)
				} else {
					startDMA(unsafe.Pointer(&blankScanline[0]), 3)
				}

				nextLine := line + 1
				if nextLine >= 0 && nextLine < vActive {
					buildBuf := 1 - displayBuf
					buildScanline(nextLine, &scanlineBufs[buildBuf])
					displayBuf = buildBuf
				}

				line++
			}

			if irqs&2 != 0 {
				videoPIO.ClearIRQ(2)
				hsyncCount.Set(hsyncCount.Get() + 1)
				inVblank.Set(1) // In vblank - safe to use serial
				startDMA(unsafe.Pointer(&blankScanline[0]), 3)
				line++
			}

			if line >= vTotal {
				line = 0
				frameCount.Set(frameCount.Get() + 1)
				buildScanline(0, &scanlineBufs[displayBuf])
			}
		}
	}()

	// Helper to force GPIO back to PIO after serial output
	forceGPIOtoPIO := func() {
		for i := 0; i < 16; i++ {
			ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
			ctrlAddr.Set(6)
		}
	}

	// Main loop - handle input and status
	// IMPORTANT: Only do serial I/O during vblank to avoid GPIO conflict with red channel
	lastHsync := uint32(0)
	lastFrame := uint32(0)
	lastTime := time.Now()
	lastStatusFrame := uint32(0)

	for {
		// Wait for vblank before any serial I/O
		for inVblank.Get() == 0 {
			time.Sleep(100 * time.Microsecond)
		}

		// Now in vblank - safe to do serial I/O
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			switch b {
			case 'r', 'R':
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			case ' ':
				invert = !invert
				println("Invert:", invert)
				forceGPIOtoPIO() // Re-force GPIO after serial
			}
		}

		// Status every 2 seconds
		f := frameCount.Get()
		if f-lastStatusFrame >= 120 {
			now := time.Now()
			elapsedMs := now.Sub(lastTime).Milliseconds()
			if elapsedMs == 0 {
				elapsedMs = 1
			}

			h := hsyncCount.Get()
			hsyncHz := ((h - lastHsync) * 1000) / uint32(elapsedMs)
			vsyncHz := ((f - lastFrame) * 1000) / uint32(elapsedMs)

			// Show color values for different bands
			white := pixelFromRGB5(0x1F, 0x1F, 0x1F) // Band 6 (white)
			red := pixelFromRGB5(0x1F, 0, 0)         // Band 0 (red)
			cyan := pixelFromRGB5(0, 0x1F, 0x1F)     // Band 5 (cyan)

			println("HSYNC:", hsyncHz, "VSYNC:", vsyncHz)
			println("Colors: WHITE=", white, "RED=", red, "CYAN=", cyan)

			// Check GPIO FUNCSEL
			gpio0Func := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014004))).Get() & 0x1F
			gpio5Func := (*volatile.Register32)(unsafe.Pointer(uintptr(0x4001402C))).Get() & 0x1F
			gpio10Func := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014054))).Get() & 0x1F
			println("GPIO FUNCSEL: 0=", gpio0Func, "5=", gpio5Func, "10=", gpio10Func, "(expect 6)")

			// Check PINCTRL
			pinctrl := rp.PIO0.SM0_PINCTRL.Get()
			outBase := pinctrl & 0x1F
			outCount := (pinctrl >> 20) & 0x3F
			println("SM0 PINCTRL: OUT_BASE=", outBase, "OUT_COUNT=", outCount)

			// Check EXECCTRL for OUT_STICKY (bit 17)
			execctrl := rp.PIO0.SM0_EXECCTRL.Get()
			outSticky := (execctrl >> 17) & 1
			println("SM0 EXECCTRL: OUT_STICKY=", outSticky, "(expect 1)")

			// Debug: Check DMA and FIFO status
			fstat := rp.PIO0.FSTAT.Get()
			txfull := (fstat >> 16) & 0xF  // TX full flags
			txempty := (fstat >> 24) & 0xF // TX empty flags
			println("FIFO: txfull=", txfull, "txempty=", txempty)

			fdebug := rp.PIO0.FDEBUG.Get()
			txstall := fdebug & 0xF         // TX stall flags
			txover := (fdebug >> 16) & 0xF  // TX overflow flags
			println("FDEBUG: txstall=", txstall, "txover=", txover)

			// Check DMA status
			dma := getDMAChannel(0)
			dmactrl := dma.CtrlTrig.Get()
			dmabusy := (dmactrl >> 24) & 1
			println("DMA: busy=", dmabusy, "transcount=", dma.TransCount.Get())

			// Check GPIO output state (sample pins 0-4 for red)
			gpio0In := rp.IO_BANK0.GPIO0_STATUS.Get()
			outVal := (gpio0In >> 8) & 1 // OUTFROMPERI - what PIO is outputting
			println("GPIO0 OUTFROMPERI=", outVal)

			forceGPIOtoPIO() // Re-force GPIO after serial output

			lastHsync = h
			lastFrame = f
			lastTime = now
			lastStatusFrame = f
		}

		// Wait for active region before next iteration
		for inVblank.Get() != 0 {
			time.Sleep(100 * time.Microsecond)
		}
	}
}

func initVideo() bool {
	videoPIO = pio.PIO0

	// Build timing instructions
	asm := pio.AssemblerV0{}
	timingInstructions[SET_IRQ_0] = asm.IRQSet(false, 0).Encode()
	timingInstructions[SET_IRQ_1] = asm.IRQSet(false, 1).Encode()
	timingInstructions[SET_IRQ_SCANLINE] = asm.IRQSet(false, 4).Encode()
	timingInstructions[CLEAR_IRQ_SCANLINE] = asm.IRQClear(false, 4).Encode()

	// Debug: Print PIO instructions
	println("IRQ instructions:")
	println("  SET_IRQ_0:", timingInstructions[SET_IRQ_0])
	println("  SET_IRQ_1:", timingInstructions[SET_IRQ_1])
	println("  SET_IRQ_SCANLINE:", timingInstructions[SET_IRQ_SCANLINE])
	println("  CLEAR_IRQ_SCANLINE:", timingInstructions[CLEAR_IRQ_SCANLINE])

	initTimingState()

	// Load scanline program at offset 0
	scanlineProgram := buildScanlineProgram()
	println("Scanline program:")
	for i, instr := range scanlineProgram {
		println("  ", i, ":", instr)
	}
	offset, err := videoPIO.AddProgram(scanlineProgram, 0)
	if err != nil {
		println("Failed to load scanline program:", err.Error())
		return false
	}
	scanlineOffset = offset

	// Load timing program at offset 16
	timingProgram := buildTimingProgram()
	offset, err = videoPIO.AddProgram(timingProgram, 16)
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
	scanlineCfg.SetClkDivIntFrac(4, 0)
	scanlineCfg.SetOutSpecial(true, false, 0) // Sticky output

	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	}
	scanlineSM.SetPindirsConsecutive(pinRGBBase, 16, true)
	scanlineSM.Init(scanlineOffset+1, scanlineCfg)

	// Verify PINCTRL
	pinctrl := rp.PIO0.SM0_PINCTRL.Get()
	outCount := (pinctrl >> 20) & 0x3F
	if outCount != 16 {
		newPinctrl := (pinctrl & ^uint32(0x3F<<20)) | (16 << 20)
		rp.PIO0.SM0_PINCTRL.Set(newPinctrl)
	}

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

	// For active low (standard VGA 640x480):
	// During pulse: output LOW (0)
	// Outside pulse: output HIGH (vsyncBit)
	if vSyncPolarity != 0 {
		// Active high
		timingState.vsyncBitsPulse = vsyncBit
		timingState.vsyncBitsNoPulse = 0
	} else {
		// Active low (standard VGA)
		timingState.vsyncBitsPulse = 0
		timingState.vsyncBitsNoPulse = vsyncBit
	}

	// For active low HSYNC:
	// During pulse: output LOW (0)
	// Outside pulse: output HIGH (1)
	var hSyncBit uint32
	if hSyncPolarity == 0 {
		// Active low - during pulse output 0, outside pulse output 1
		hSyncBit = 0
	} else {
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
		asm.Out(pio.OutDestNull, 32).Encode(),           // 0: EOL_SKIP_ALIGN
		asm.WaitIRQ(true, false, 4).Encode(),            // 1: entry - wait IRQ 4
		asm.Out(pio.OutDestPC, 16).Encode(),             // 2: dispatch

		// color_run (3-6)
		asm.Out(pio.OutDestPins, 16).Encode(),           // 3: output color
		asm.Out(pio.OutDestX, 16).Encode(),              // 4: load count
		asm.Jmp(pio.JmpXNZeroDec, 5).Encode() | delay1,  // 5: loop [1]
		asm.Out(pio.OutDestPC, 16).Encode() | delay1,    // 6: next cmd [1]

		// raw_run (7-10)
		asm.Out(pio.OutDestPins, 16).Encode(),           // 7: first pixel
		asm.Out(pio.OutDestX, 16).Encode(),              // 8: load count
		asm.Out(pio.OutDestPins, 16).Encode(),           // 9: pixel loop
		asm.Jmp(pio.JmpXNZeroDec, 9).Encode(),           // 10: loop

		// raw_1p (11-12)
		asm.Out(pio.OutDestPins, 16).Encode(),           // 11: output pixel
		asm.Out(pio.OutDestPC, 16).Encode(),             // 12: next cmd

		// raw_2p (13)
		asm.Out(pio.OutDestPins, 16).Encode() | delay1,  // 13: first pixel [1]

		// raw_1p_skip_align (14-15)
		asm.Out(pio.OutDestPins, 32).Encode(),           // 14: skip align
		asm.Out(pio.OutDestPC, 16).Encode(),             // 15: next cmd
	}
}

func enableVideo(enable bool) {
	timingSM.SetEnabled(false)
	scanlineSM.SetEnabled(false)

	if enable {
		// Prime timing FIFO (no serial output here - video is about to start)
		for i := 0; i < 8; i++ {
			topUpTimingFIFO()
		}

		// Pre-fill scanline FIFO with multiple blank scanlines
		for i := 0; i < 2; i++ {
			scanlineSM.TxPut(uint32(COMPOSABLE_COLOR_RUN) | (0 << 16))
			scanlineSM.TxPut(uint32(hActive-3) | (uint32(COMPOSABLE_RAW_1P) << 16))
			scanlineSM.TxPut(0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16))
		}

		rp.PIO0.IRQ0_INTE.SetBits(0x03)
		rp.PIO0.IRQ1_INTE.SetBits(1 << (8 + 3)) // SM3 TX not full

		videoPIO.ClearIRQ(0xFF)

		scanlineSM.Exec(encodeJmp(scanlineOffset + 1))
		timingSM.Exec(encodeJmp(timingOffset))

		// Force GPIO to PIO right before enabling SMs
		for i := 0; i < 16; i++ {
			ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
			ctrlAddr.Set(6)
		}

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
	ctrl |= 1 << 0  // Enable
	ctrl |= 1 << 1  // High priority
	ctrl |= 2 << 2  // 32-bit
	ctrl |= 1 << 4  // Increment read

	dma.WriteAddr.Set(txFifo)
	dma.CtrlTrig.Set(ctrl & ^uint32(1))
}

func startDMA(buf unsafe.Pointer, count int) {
	dma := getDMAChannel(0)
	dma.ReadAddr.Set(uint32(uintptr(buf)))
	dma.TransCount.Set(uint32(count))
	dma.CtrlTrig.SetBits(1)
}

func initBlankScanline() {
	blankScanline[0] = uint32(COMPOSABLE_COLOR_RUN) | (0 << 16)
	blankScanline[1] = uint32(hActive-3) | (uint32(COMPOSABLE_RAW_1P) << 16)
	blankScanline[2] = 0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16)
}

// buildScanline creates a solid color scanline using COLOR_RUN
func buildScanline(line int, buf *[scanlineBufWords]uint32) {
	// Calculate color based on line for horizontal bands
	// 7 bands across 480 lines
	band := (line * 7) / vActive
	if band > 6 {
		band = 6
	}

	// Band colors: 1=red, 2=green, 3=yellow, 4=blue, 5=magenta, 6=cyan, 7=white
	primaryColor := uint32(band + 1)
	r := uint32(0x1F) * (primaryColor & 1)
	g := uint32(0x1F) * ((primaryColor >> 1) & 1)
	b := uint32(0x1F) * ((primaryColor >> 2) & 1)

	var color uint16
	if invert {
		color = pixelFromRGB5(0x1F-r, 0x1F-g, 0x1F-b)
	} else {
		color = pixelFromRGB5(r, g, b)
	}

	// COLOR_RUN format
	buf[0] = uint32(COMPOSABLE_COLOR_RUN) | (uint32(color) << 16)
	buf[1] = uint32(hActive-3) | (uint32(COMPOSABLE_RAW_1P) << 16)
	buf[2] = uint32(color) | (uint32(COMPOSABLE_EOL_ALIGN) << 16)
}
