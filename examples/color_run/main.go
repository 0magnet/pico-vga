// Hello World demo - VGA 640x480@60Hz using pico-extras scanvideo architecture
// This version closely follows the C pico-extras scanvideo implementation
// Build with: tinygo build -target=pico -o hello.uf2 hello_world.go
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

// VGA timing for 640x480@60Hz (standard VESA timing, matches C pico-extras non-48MHz mode)
const (
	hActive     = 640
	hFrontPorch = 16
	hPulse      = 96  // Standard VGA: 96 pixels
	hTotal      = 800 // hActive(640) + hFrontPorch(16) + hPulse(96) + hBackPorch(48) = 800

	vActive     = 480
	vFrontPorch = 10  // Standard VGA: 10 lines
	vPulse      = 2
	vTotal      = 525 // Standard VGA: 525 lines

	// Standard VGA polarities: NEGATIVE (active low) for both syncs
	hSyncPolarity = 0 // Active low (standard VGA)
	vSyncPolarity = 0 // Active low (standard VGA)
)

// Composable scanline command offsets - MUST match pico-extras video_24mhz_composable
const (
	COMPOSABLE_EOL_SKIP_ALIGN  = 0
	COMPOSABLE_EOL_ALIGN       = 1
	COMPOSABLE_RAW             = 2  // dispatch
	COMPOSABLE_COLOR_RUN       = 3
	COMPOSABLE_RAW_RUN         = 7
	COMPOSABLE_RAW_1P          = 11
	COMPOSABLE_RAW_2P          = 13
	COMPOSABLE_RAW_1P_SKIP_ALIGN = 14
)

// Timing state command indices (match C enum)
const (
	SET_IRQ_0          = 0 // Active scanline IRQ
	SET_IRQ_1          = 1 // Vblank scanline IRQ
	SET_IRQ_SCANLINE   = 2 // IRQ 4 - triggers scanline SM
	CLEAR_IRQ_SCANLINE = 3 // Clear IRQ 4
)

// Pre-encoded timing instructions
var timingInstructions [4]uint16

// Timing state (matches C timing_state struct)
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

// DMA state array - 4 states per scanline (a, b1, b2, c)
var dmaStates [4]uint32

// Frame buffer (1bpp, 640x480 = 38400 bytes)
// Double buffered to avoid tearing (like C gvga example)
const (
	frameWidth  = 640
	frameHeight = 480
)

var frameBuffers [2][frameHeight][frameWidth / 8]byte
var drawBuffer int             // Buffer being drawn to (0 or 1)
var showBuffer volatile.Register32 // Buffer being displayed (atomic for core sync)

// Palette lookup table: byte value (0-255) -> 8 RGB565 colors
var paletteBuf [256 * 8]uint16

// Scanline buffers
// RAW_RUN: 2 header words + 319 pixel pair words (638 pixels) + 1 EOL word = 322 words
// Pixels 1-2 in header, pixels 3-640 in pairs, raw_1p fall-through outputs pixel 640
const scanlineBufWords = 322
var scanlineBufs [2][scanlineBufWords]uint32

// Blank scanline for vblank
var blankScanline [3]uint32
var blankScanlineLen int

// PIO and state machines
var (
	videoPIO       *pio.PIO
	timingSM       pio.StateMachine
	scanlineSM     pio.StateMachine
	timingOffset   uint8 // Program offset for timing SM
	scanlineOffset uint8 // Program offset for scanline SM
)

// DMA channel registers
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

// Animation state
type animation struct {
	x, y   int
	dx, dy int
}

// Animation matches C example: start at (20,20), move by (5,5) per frame
var anim = animation{x: 20, y: 20, dx: 5, dy: 5}

func main() {
	time.Sleep(5 * time.Second)
	println("=== VGA 640x480@60Hz Demo ===")
	println("Following pico-extras scanvideo architecture")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	// DEBUG: Check GPIO function select for pins 0-15 BEFORE video init
	println("GPIO FUNCSEL before init:")
	for i := 0; i < 16; i++ {
		// IO_BANK0 CTRL registers are at 0x40014000 + 8*gpio + 4
		ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
		funcsel := ctrlAddr.Get() & 0x1F
		println("  GPIO", i, "FUNCSEL =", funcsel)
	}

	// Initialize video system
	if !initVideo() {
		println("Video init failed!")
		blinkError(led)
		return
	}

	// DEBUG: Check GPIO function select for pins 0-15 AFTER video init
	println("GPIO FUNCSEL after video init:")
	for i := 0; i < 16; i++ {
		ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
		funcsel := ctrlAddr.Get() & 0x1F
		println("  GPIO", i, "FUNCSEL =", funcsel)
	}

	// FORCE GPIO 0-15 to PIO0 function (FUNCSEL=6) to override any UART initialization
	println("Forcing GPIO 0-15 to PIO0 function (FUNCSEL=6)...")
	for i := 0; i < 16; i++ {
		ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
		// Set FUNCSEL to 6 (PIO0), clear other control bits
		ctrlAddr.Set(6)
	}
	println("GPIO FUNCSEL after force:")
	for i := 0; i < 16; i++ {
		ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
		funcsel := ctrlAddr.Get() & 0x1F
		println("  GPIO", i, "FUNCSEL =", funcsel)
	}

	// Initialize graphics
	initPalette()
	initBlankScanline()
	clearScreen(0)
	drawPattern()

	// Pre-build first scanline
	buildScanline(0, &scanlineBufs[0])

	// Enable video output
	enableVideo(true)
	println("Video enabled")

	// Counters for frequency measurement (shared with goroutine via volatile)
	var frameCount volatile.Register32
	var hsyncCount volatile.Register32
	var goroutineRunning volatile.Register32
	var inVblank volatile.Register32 // Set to 1 during vblank, cleared when entering active
	var activeDmaCount volatile.Register32  // DMA starts during active region
	var vblankDmaCount volatile.Register32  // DMA starts during vblank
	var fifoEmptyCount volatile.Register32  // Times FIFO was empty when we checked

	// Using COLOR_RUN format like C scanvideo_minimal - only 3 words per scanline!
	// This is the key difference from RAW_RUN (322 words) that caused DMA collisions
	const GVGA_WHITE = 0xFFDF // Test with WHITE
	const MIN_COLOR_RUN = 3   // Matches C definition
	println("Using COLOR_RUN format: 3 words/scanline (like C scanvideo_minimal)")

	// single_color_scanline - exact port of C function from scanvideo_minimal.c
	// Returns number of words used (always 3)
	single_color_scanline := func(buf []uint32, width int, color16 uint16) int {
		// | jmp color_run | color | count-3 | COMPOSABLE_RAW_1P | black | EOL_ALIGN |
		buf[0] = uint32(COMPOSABLE_COLOR_RUN) | (uint32(color16) << 16)
		buf[1] = uint32(width-MIN_COLOR_RUN) | (uint32(COMPOSABLE_RAW_1P) << 16)
		// Note: must end with a black pixel (per C comment)
		buf[2] = 0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16)
		return 3
	}

	// Build COLOR_RUN scanline with line-based color (like C example)
	// C uses: bgcolour = (uint16_t) l << 2
	buildColorRunScanline := func(line int, buf []uint32) int {
		// For solid color test, use constant WHITE
		// To match C gradient: color16 := uint16(line << 2)
		color16 := uint16(GVGA_WHITE)
		return single_color_scanline(buf, frameWidth, color16)
	}

	// Pre-build first scanline using COLOR_RUN
	colorRunLen := buildColorRunScanline(0, scanlineBufs[0][:])

	// GPIO sampling at strategic lines to track the gradient
	var gpioLine0 volatile.Register32   // GPIO at line 0 (top)
	var gpioLine100 volatile.Register32 // GPIO at line 100
	var gpioLine200 volatile.Register32 // GPIO at line 200
	var gpioLine300 volatile.Register32 // GPIO at line 300
	var gpioLine400 volatile.Register32 // GPIO at line 400
	var gpioLine479 volatile.Register32 // GPIO at line 479 (bottom)
	gpioIn := (*volatile.Register32)(unsafe.Pointer(uintptr(0xd0000004)))

	// Render loop using COLOR_RUN format (3 words) - matches C scanvideo_minimal
	go func() {
		goroutineRunning.Set(1)
		line := 0
		displayBuf := 0 // Start with scanlineBufs[0] which was pre-built
		dataLen := colorRunLen // 3 words for COLOR_RUN

		for {
			goroutineRunning.Set(goroutineRunning.Get() + 1)
			// Keep timing FIFO fed
			topUpTimingFIFO()

			// Check for scanline IRQs (same as hello_world - both if, not else if)
			irqs := videoPIO.GetIRQ()

			if irqs&1 != 0 {
				// IRQ 0: Active scanline
				videoPIO.ClearIRQ(1)
				hsyncCount.Set(hsyncCount.Get() + 1)
				inVblank.Set(0)

				// Sample GPIO at strategic lines
				switch line {
				case 0:
					gpioLine0.Set(gpioIn.Get() & 0xFFFF)
				case 100:
					gpioLine100.Set(gpioIn.Get() & 0xFFFF)
				case 200:
					gpioLine200.Set(gpioIn.Get() & 0xFFFF)
				case 300:
					gpioLine300.Set(gpioIn.Get() & 0xFFFF)
				case 400:
					gpioLine400.Set(gpioIn.Get() & 0xFFFF)
				case 479:
					gpioLine479.Set(gpioIn.Get() & 0xFFFF)
				}

				// Transfer scanline data via DMA - only 3 words with COLOR_RUN!
				if line >= 0 && line < frameHeight {
					startDMA(unsafe.Pointer(&scanlineBufs[displayBuf][0]), dataLen)
					activeDmaCount.Set(activeDmaCount.Get() + 1)
				} else {
					startDMA(unsafe.Pointer(&blankScanline[0]), blankScanlineLen)
				}

				// Build next scanline into the other buffer while DMA runs
				nextLine := line + 1
				if nextLine >= 0 && nextLine < frameHeight {
					buildBuf := 1 - displayBuf
					dataLen = buildColorRunScanline(nextLine, scanlineBufs[buildBuf][:])
					displayBuf = buildBuf // swap for next iteration
				}

				line++
			}

			if irqs&2 != 0 {
				// IRQ 1: Vblank scanline
				videoPIO.ClearIRQ(2)
				hsyncCount.Set(hsyncCount.Get() + 1)
				inVblank.Set(1)

				// During vblank, just send blank scanlines
				startDMA(unsafe.Pointer(&blankScanline[0]), blankScanlineLen)
				vblankDmaCount.Set(vblankDmaCount.Get() + 1)
				line++
			}

			// Frame boundary
			if line >= vTotal {
				line = 0
				frameCount.Set(frameCount.Get() + 1)
				// Build scanline 0 for next frame using COLOR_RUN
				dataLen = buildColorRunScanline(0, scanlineBufs[displayBuf][:])
			}
		}
	}()

	println("Render loop running on core1")
	println("Press 'r' to reboot to BOOTSEL")

	// Main loop on core0: animation runs every frame, status output every second
	lastHsync := uint32(0)
	lastFrame := uint32(0)
	lastTime := time.Now()
	lastStatusFrame := uint32(0)

	for {
		// Check for reboot command (non-blocking)
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == 'r' || b == 'R' {
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}

		// DISABLED: Animation and buffer swapping for solid color test
		// Just idle - no drawing, no buffer swaps
		_ = inVblank // suppress unused warning

		// Status output every 2 seconds (same as hello_world)
		f := frameCount.Get()
		if f-lastStatusFrame >= 120 {
			now := time.Now()
			elapsedMs := now.Sub(lastTime).Milliseconds()
			if elapsedMs == 0 {
				elapsedMs = 1
			}

			h := hsyncCount.Get()
			hsyncDelta := h - lastHsync
			frameDelta := f - lastFrame

			hsyncHz := (hsyncDelta * 1000) / uint32(elapsedMs)
			vsyncHz := (frameDelta * 1000) / uint32(elapsedMs)
			linesPerFrame := uint32(0)
			if frameDelta > 0 {
				linesPerFrame = hsyncDelta / frameDelta
			}

			println("=== TEST STATUS ===")
			println("HSYNC:", hsyncHz, "Hz, VSYNC:", vsyncHz, "Hz, lines/f:", linesPerFrame)

			activePerFrame := uint32(0)
			vblankPerFrame := uint32(0)
			if f > 0 {
				activePerFrame = activeDmaCount.Get() / f
				vblankPerFrame = vblankDmaCount.Get() / f
			}
			println("DMA/frame: active=", activePerFrame, "vblank=", vblankPerFrame)

			// Read GPIO pins 0-15 to see actual pixel output (current instant)
			gpioInNow := (*volatile.Register32)(unsafe.Pointer(uintptr(0xd0000004)))
			pixelPins := gpioInNow.Get() & 0xFFFF
			r := pixelPins & 0x1F
			g := (pixelPins >> 6) & 0x1F
			b := (pixelPins >> 11) & 0x1F
			println("GPIO now:", hex(pixelPins), "R=", r, "G=", g, "B=", b)

			// GPIO samples from strategic lines (captured during render loop)
			g0 := gpioLine0.Get()
			g100 := gpioLine100.Get()
			g200 := gpioLine200.Get()
			g300 := gpioLine300.Get()
			g400 := gpioLine400.Get()
			g479 := gpioLine479.Get()
			println("GPIO line samples: 0=", hex(g0), "100=", hex(g100), "200=", hex(g200))
			println("  300=", hex(g300), "400=", hex(g400), "479=", hex(g479))

			// Buffer contents
			println("Buffer: [0]=", hex(scanlineBufs[0][0]), "[1]=", hex(scanlineBufs[0][1]), "[2]=", hex(scanlineBufs[0][2]))

			// All 4 State Machines status
			println("--- PIO0 State Machines ---")
			for sm := 0; sm < 4; sm++ {
				addrReg := (*volatile.Register32)(unsafe.Pointer(uintptr(0x502000D4 + uintptr(sm)*24)))
				addr := addrReg.Get()
				println("  SM", sm, "PC=", addr)
			}

			// FSTAT register
			fstat := rp.PIO0.FSTAT.Get()
			txFull := fstat & 0xF
			txEmpty := (fstat >> 8) & 0xF
			rxFull := (fstat >> 16) & 0xF
			rxEmpty := (fstat >> 24) & 0xF
			println("FSTAT: txFull=", hex(txFull), "txEmpty=", hex(txEmpty), "rxFull=", hex(rxFull), "rxEmpty=", hex(rxEmpty))

			// FIFO levels
			flevel := rp.PIO0.FLEVEL.Get()
			println("FLEVEL:", hex(flevel))

			// DMA channel 0 status
			dma := getDMAChannel(0)
			dmaCtrl := dma.CtrlTrig.Get()
			dmaBusy := (dmaCtrl >> 24) & 1
			dmaRead := dma.ReadAddr.Get()
			dmaWrite := dma.WriteAddr.Get()
			dmaCnt := dma.TransCount.Get()
			println("DMA0: busy=", dmaBusy, "read=", hex(dmaRead), "write=", hex(dmaWrite), "cnt=", dmaCnt)
			println("DMA waits:", dmaWaitCount.Get(), "collisions:", dmaCollisionCount.Get())

			// SM0 and SM3 config registers
			sm0Pinctrl := rp.PIO0.SM0_PINCTRL.Get()
			sm0Execctrl := rp.PIO0.SM0_EXECCTRL.Get()
			sm0Shiftctrl := rp.PIO0.SM0_SHIFTCTRL.Get()
			println("SM0: PINCTRL=", hex(sm0Pinctrl), "EXECCTRL=", hex(sm0Execctrl))
			println("SM0: SHIFTCTRL=", hex(sm0Shiftctrl))

			sm3Pinctrl := rp.PIO0.SM3_PINCTRL.Get()
			sm3Execctrl := rp.PIO0.SM3_EXECCTRL.Get()
			println("SM3: PINCTRL=", hex(sm3Pinctrl), "EXECCTRL=", hex(sm3Execctrl))

			// PIO IRQ status
			pioIrq := videoPIO.GetIRQ()
			println("PIO IRQ:", hex(uint32(pioIrq)))

			lastHsync = h
			lastFrame = f
			lastTime = now
			lastStatusFrame = f
		}
		_ = fifoEmptyCount
	}
}

// initVideo sets up PIO and DMA for video output
func initVideo() bool {
	videoPIO = pio.PIO0

	// Build timing instructions (IRQ instructions executed via out exec)
	asm := pio.AssemblerV0{}
	timingInstructions[SET_IRQ_0] = asm.IRQSet(false, 0).Encode()
	timingInstructions[SET_IRQ_1] = asm.IRQSet(false, 1).Encode()
	timingInstructions[SET_IRQ_SCANLINE] = asm.IRQSet(false, 4).Encode()
	timingInstructions[CLEAR_IRQ_SCANLINE] = asm.IRQClear(false, 4).Encode()

	// Initialize timing state values
	initTimingState()

	// Load composable scanline program (MUST be at offset 0)
	scanlineProgram := buildScanlineProgram()
	println("Scanline program (", len(scanlineProgram), "instructions):")
	for i, instr := range scanlineProgram {
		println("  [", i, "]:", hex(uint32(instr)))
	}
	offset, err := videoPIO.AddProgram(scanlineProgram, 0)
	if err != nil {
		println("Failed to load scanline program at offset 0:", err.Error())
		return false
	}
	scanlineOffset = offset
	println("Scanline program at offset", scanlineOffset)

	// Load timing program at fixed offset 16 (scanline uses 0-15)
	const timingProgramOffsetVal = 16
	timingProgram := buildTimingProgram() // JMP targets are relative, library adds offset
	println("Timing program (", len(timingProgram), "instructions):")
	for i, instr := range timingProgram {
		println("  [", timingProgramOffsetVal+i, "]:", hex(uint32(instr)))
	}
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
	// Clock divider 4 = 31.25 MHz, same as timing SM for synchronization
	// Note: RED channel issue still unresolved - shows CYAN instead of WHITE
	scanlineCfg.SetClkDivIntFrac(4, 0)
	// CRITICAL: Enable sticky output - continuously assert OUT pins (matches C scanvideo)
	scanlineCfg.SetOutSpecial(true, false, 0)

	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	}
	scanlineSM.SetPindirsConsecutive(pinRGBBase, 16, true)
	scanlineSM.Init(scanlineOffset+1, scanlineCfg) // Start at entry point (wait IRQ)

	// Debug: dump SM0 config (no modifications - let SetOutShift handle it like hello_world)
	pinctrl := rp.PIO0.SM0_PINCTRL.Get()
	outBase := pinctrl & 0x1F
	outCount := (pinctrl >> 20) & 0x3F
	println("SM0 PINCTRL: OUT_BASE=", outBase, "OUT_COUNT=", outCount)

	shiftctrl := rp.PIO0.SM0_SHIFTCTRL.Get()
	autopull := (shiftctrl >> 29) & 1
	outShiftDir := (shiftctrl >> 31) & 1
	fjoinTx := (shiftctrl >> 30) & 1
	pullThresh := (shiftctrl >> 20) & 0x3F
	println("SM0 SHIFTCTRL:", hex(shiftctrl), "AUTOPULL=", autopull, "OUT_SHIFTDIR=", outShiftDir, "FJOIN_TX=", fjoinTx, "PULL_THRESH=", pullThresh)

	// Configure timing SM (SM3)
	timingSM = videoPIO.StateMachine(3)
	timingSM.TryClaim()

	timingCfg := pio.DefaultStateMachineConfig()
	timingCfg.SetOutPins(pinHSYNC, 2)         // HSYNC, VSYNC as OUT pins
	timingCfg.SetOutShift(true, true, 32)     // Shift right, autopull
	timingCfg.SetClkDivIntFrac(4, 0)          // 31.25 MHz to compensate for timing loop overhead
	// Set wrap points: wrap from end of jmp (21) back to new_state (17)
	// TinyGo SetWrap takes (target, top) - the address to wrap TO first, then wrap FROM
	wrapBottom := timingOffset + timingWrapTarget // 17
	wrapTop := timingOffset + timingWrapEnd       // 21
	println("Setting timing wrap: bottom=", wrapBottom, "top=", wrapTop)
	timingCfg.SetWrap(wrapBottom, wrapTop)

	pinHSYNC.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	pinVSYNC.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	timingSM.SetPindirsConsecutive(pinHSYNC, 2, true)
	timingSM.Init(timingOffset, timingCfg)

	// Debug: verify wrap was set correctly
	execctrl := rp.PIO0.SM3_EXECCTRL.Get()
	actualTop := (execctrl >> 12) & 0x1F
	actualBottom := (execctrl >> 7) & 0x1F
	println("After Init - SM3 EXECCTRL wrap: bottom=", actualBottom, "top=", actualTop)

	// Debug: verify clock divider
	clkdiv := rp.PIO0.SM3_CLKDIV.Get()
	clkdivInt := (clkdiv >> 16) & 0xFFFF
	clkdivFrac := (clkdiv >> 8) & 0xFF
	println("After Init - SM3 CLKDIV: int=", clkdivInt, "frac=", clkdivFrac)

	// Configure DMA
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
	// C code: vsync_bits_pulse = timing->v_sync_polarity ? 0 : vsync_bit;
	// C code: vsync_bits_no_pulse = timing->v_sync_polarity ? vsync_bit : 0;
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
	var hSyncBit uint32
	if hSyncPolarity == 0 {
		hSyncBit = 1 // Active low: output 1 during pulse (inverted)
	} else {
		hSyncBit = 0 // Active high: output 0 during pulse (inverted)
	}
	hSyncNoBit := 1 - hSyncBit

	// Calculate back porch
	hBackPorch := hTotal - hActive - hFrontPorch - hPulse
	hActiveAndFront := hActive + hFrontPorch

	// Encode timing states (matches C timing_encode macro)
	// Format: instruction(16) | (cycles-3)(13) | pins(3)
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

	println("Timing states (hex):")
	println("  a:", hex(timingState.a), "a_vblank:", hex(timingState.aVblank))
	println("  b1:", hex(timingState.b1), "b2:", hex(timingState.b2))
	println("  c:", hex(timingState.c), "c_vblank:", hex(timingState.cVblank))
	println("IRQ instructions (hex):")
	println("  SET_IRQ_0:", hex(uint32(timingInstructions[SET_IRQ_0])))
	println("  SET_IRQ_1:", hex(uint32(timingInstructions[SET_IRQ_1])))
	println("  SET_IRQ_SCANLINE:", hex(uint32(timingInstructions[SET_IRQ_SCANLINE])))
	println("  CLEAR_IRQ_SCANLINE:", hex(uint32(timingInstructions[CLEAR_IRQ_SCANLINE])))
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

func timingEncode(cmd int, cycles int, pins uint32) uint32 {
	// TIMING_CYCLE = 3 matches C pico-extras exactly
	// The timing formula is complex due to the 2-cycle loop
	const TIMING_CYCLE = 3
	return uint32(timingInstructions[cmd]) | (uint32(cycles-TIMING_CYCLE) << 16) | (pins << 29)
}

// encodeJmp creates a PIO JMP always instruction to the given address
func encodeJmp(addr uint8) uint16 {
	// JMP instruction: bits 15-13=000, bits 12-8=condition (000=always), bits 4-0=address
	return uint16(addr & 0x1F)
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
	// Fill while TX FIFO has space
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

// buildTimingProgram creates timing PIO program
// Note: JMP targets are RELATIVE to program start (0-based within program)
// The TinyGo PIO library automatically adds the load offset to JMP targets
//
// Match C pico-extras timing.pio EXACTLY:
// - 2-cycle loop (nop + jmp) like the C code
// - This ensures timing matches the C implementation
func buildTimingProgram() []uint16 {
	asm := pio.AssemblerV0{}
	return []uint16{
		asm.Pull(false, true).Encode(),          // 0: entry_point - pull block
		// .wrap_target here (relative offset 1)
		asm.Out(pio.OutDestExec, 16).Encode(),   // 1: new_state - execute IRQ instruction
		asm.Out(pio.OutDestX, 13).Encode(),      // 2: load cycle count into X
		asm.Out(pio.OutDestPins, 3).Encode(),    // 3: output sync pins (HSYNC, VSYNC)
		// loop: 2-cycle delay (nop + jmp) - matches C timing.pio exactly
		asm.Nop().Encode(),                      // 4: nop
		asm.Jmp(pio.JmpXNZeroDec, 4).Encode(),   // 5: jmp x-- loop (to nop at relative offset 4)
		// .wrap here (back to relative offset 1)
	}
}

// Timing program wrap points (relative to program start)
const (
	timingWrapTarget = 1 // new_state (out exec) - where wrap goes TO
	timingWrapEnd    = 5 // jmp x-- loop - where wrap happens FROM (6-instruction program: 0-5)
)

// buildScanlineProgram creates composable scanline PIO program
// This must match the C pico-extras video_24mhz_composable program
func buildScanlineProgram() []uint16 {
	asm := pio.AssemblerV0{}

	// PIO delay is encoded in bits 8-12 of the instruction
	// To add delay N: instruction | (N << 8)
	const delay1 = 1 << 8 // 1 cycle delay

	return []uint16{
		asm.Out(pio.OutDestNull, 32).Encode(),      // 0: end_of_scanline_skip_ALIGN
		asm.WaitIRQ(true, false, 4).Encode(),       // 1: entry_point - wait for IRQ 4
		asm.Out(pio.OutDestPC, 16).Encode(),        // 2: dispatch (nop_raw)

		// color_run (3-6): outputs color, then loops for count cycles
		asm.Out(pio.OutDestPins, 16).Encode(),           // 3: output color
		asm.Out(pio.OutDestX, 16).Encode(),              // 4: load count
		asm.Jmp(pio.JmpXNZeroDec, 5).Encode() | delay1,  // 5: color_loop [1]
		asm.Out(pio.OutDestPC, 16).Encode() | delay1,    // 6: next command [1]

		// raw_run (7-10): outputs individual pixels - NO delay for speed
		asm.Out(pio.OutDestPins, 16).Encode(),      // 7: first pixel
		asm.Out(pio.OutDestX, 16).Encode(),         // 8: load count
		asm.Out(pio.OutDestPins, 16).Encode(),      // 9: pixel_loop - 1 cycle
		asm.Jmp(pio.JmpXNZeroDec, 9).Encode(),      // 10: loop - 1 cycle (total 2/pixel)

		// raw_1p (11-12): output single pixel then dispatch
		asm.Out(pio.OutDestPins, 16).Encode(),      // 11: output pixel
		asm.Out(pio.OutDestPC, 16).Encode(),        // 12: next command

		// raw_2p (13): output first pixel, wraps to raw_1p
		asm.Out(pio.OutDestPins, 16).Encode() | delay1, // 13: first pixel [1]

		// raw_1p_skip_ALIGN (14-15)
		asm.Out(pio.OutDestPins, 32).Encode(),      // 14: skip align
		asm.Out(pio.OutDestPC, 16).Encode(),        // 15: next command
	}
}

// enableVideo starts/stops video output
func enableVideo(enable bool) {
	// Disable SMs first
	timingSM.SetEnabled(false)
	scanlineSM.SetEnabled(false)

	if enable {
		println("Enabling video...")
		println("  timingOffset:", timingOffset, "scanlineOffset:", scanlineOffset)

		// Prime timing FIFO
		for i := 0; i < 8; i++ {
			topUpTimingFIFO()
		}
		println("  Timing FIFO primed, TxFull:", timingSM.IsTxFIFOFull())

		// Pre-fill scanline FIFO with blank data (match hello_world approach)
		// Single blank scanline to prime the FIFO
		println("  Pre-filling scanline FIFO with blank data...")
		const blankColor = 0 // BLACK - match hello_world
		scanlineSM.TxPut(uint32(COMPOSABLE_COLOR_RUN) | (blankColor << 16))
		scanlineSM.TxPut(uint32(frameWidth-3) | (uint32(COMPOSABLE_RAW_1P) << 16))
		scanlineSM.TxPut(blankColor | (uint32(COMPOSABLE_EOL_ALIGN) << 16))
		// Check FIFO level after pre-fill
		flevel := rp.PIO0.FLEVEL.Get()
		sm0TxLevel := (flevel >> 0) & 0xF
		println("  Scanline FIFO level after pre-fill:", sm0TxLevel)

		// Enable IRQ sources first
		rp.PIO0.IRQ0_INTE.SetBits(0x03)                  // IRQ 0 and IRQ 1
		rp.PIO0.IRQ1_INTE.SetBits(1 << (8 + 3))          // SM3 TX not full

		// Clear any pending IRQs
		videoPIO.ClearIRQ(0xFF)

		// Force SMs to correct entry points before enabling
		println("  Before Exec: SM3 PC =", rp.PIO0.SM3_ADDR.Get())
		println("  JMP target:", timingOffset, "encoded:", hex(uint32(encodeJmp(timingOffset))))

		scanlineSM.Exec(encodeJmp(scanlineOffset + 1)) // Wait IRQ instruction at offset 1
		timingSM.Exec(encodeJmp(timingOffset))         // Pull instruction at timing offset

		println("  After Exec: SM3 PC =", rp.PIO0.SM3_ADDR.Get())

		// Enable SMs
		timingSM.SetEnabled(true)
		scanlineSM.SetEnabled(true)

		println("  After Enable: SM3 PC =", rp.PIO0.SM3_ADDR.Get())

		println("  SMs enabled, checking IRQs...")
		time.Sleep(10 * time.Millisecond)
		println("  PIO IRQ after 10ms:", videoPIO.GetIRQ())
		println("  TxFull after 10ms:", timingSM.IsTxFIFOFull())

		// Read PIO control register directly
		pioCtrl := rp.PIO0.CTRL.Get()
		println("  PIO0 CTRL:", hex(pioCtrl))
		println("  SM3 enabled bit:", (pioCtrl>>3)&1)

		// Read SM3 ADDR (current PC)
		pioAddr := rp.PIO0.SM3_ADDR.Get()
		println("  SM3 ADDR (PC):", pioAddr)
	}
}

// setupDMA configures DMA channel 0 for scanline transfer
func setupDMA() {
	// Enable DMA
	rp.RESETS.RESET.ClearBits(rp.RESETS_RESET_DMA)
	for !rp.RESETS.RESET_DONE.HasBits(rp.RESETS_RESET_DONE_DMA) {
	}

	dma := getDMAChannel(0)
	txFifo := uint32(0x50200010) // PIO0 TXF0 (SM0)

	ctrl := uint32(0)
	ctrl |= 1 << 0                 // Enable
	ctrl |= 1 << 1                 // High priority
	ctrl |= 2 << 2                 // 32-bit transfers
	ctrl |= 1 << 4                 // Increment read address
	ctrl |= 0 << 15                // DREQ = PIO0_TX0

	dma.WriteAddr.Set(txFifo)
	dma.CtrlTrig.Set(ctrl & ^uint32(1)) // Configure but don't start

	println("DMA configured for SM0")
}

func waitDMA() {
	dma := getDMAChannel(0)
	// Wait for DMA to complete (BUSY bit clears)
	for dma.CtrlTrig.Get()&(1<<24) != 0 {
		// busy wait
	}
}

var dmaCollisionCount volatile.Register32
var dmaWaitCount volatile.Register32

func startDMA(buf unsafe.Pointer, count int) {
	dma := getDMAChannel(0)

	// Check if previous DMA still running (collision detection only, no blocking)
	// Blocking here breaks timing - we pace with delay in buildSolidScanline instead
	if dma.CtrlTrig.Get()&(1<<24) != 0 {
		dmaCollisionCount.Set(dmaCollisionCount.Get() + 1)
	}

	dma.ReadAddr.Set(uint32(uintptr(buf)))
	dma.TransCount.Set(uint32(count))
	dma.CtrlTrig.SetBits(1) // Start
}

// Palette and scanline building

func initPalette() {
	// SOLID COLOR TEST: All pixels are WHITE
	const WHITE = 0xFFDF // R=31, G=31, B=31 in GVGA format
	println("SOLID COLOR TEST: WHITE=", hex(uint32(WHITE)))

	// Set ALL palette entries to WHITE
	for i := 0; i < 256*8; i++ {
		paletteBuf[i] = WHITE
	}
	println("Palette initialized: ALL WHITE")
}

// Test scanline buffers for solid color output
var redScanline [4]uint32

func initBlankScanline() {
	// Use COLOR_RUN format matching hello_world (which works!)
	// hello_world uses frameWidth-3 = 637
	colorRunCount := uint32(frameWidth - 3) // 640 - 3 = 637

	blankScanline[0] = uint32(COMPOSABLE_COLOR_RUN) | (0 << 16)                    // COLOR_RUN | BLACK
	blankScanline[1] = colorRunCount | (uint32(COMPOSABLE_RAW_1P) << 16)           // count | RAW_1P
	blankScanline[2] = 0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16)                    // BLACK | EOL_ALIGN
	blankScanlineLen = 3
	println("Blank scanline: count=", colorRunCount)

	// Red test scanline - same format, but RED color
	const GVGA_RED = 0x001F
	redScanline[0] = uint32(COMPOSABLE_COLOR_RUN) | (uint32(GVGA_RED) << 16)       // COLOR_RUN | RED
	redScanline[1] = colorRunCount | (uint32(COMPOSABLE_RAW_1P) << 16)             // count | RAW_1P
	redScanline[2] = uint32(GVGA_RED) | (uint32(COMPOSABLE_EOL_ALIGN) << 16)       // RED | EOL_ALIGN
}

func buildScanline(line int, buf *[scanlineBufWords]uint32) {
	// Read from the show buffer (the one currently being displayed)
	row := frameBuffers[showBuffer.Get()][line][:]
	ptr := 0

	// RAW_RUN format: | jmp raw_run | pixel1 | count | pixel2 | <count more pixels> |
	// With count = frameWidth-3 = 637, RAW_RUN outputs 639 pixels (1 + 638 from loop)
	// After loop, raw_1p fall-through outputs pixel 640 from OSR, then dispatches to EOL

	// First byte provides pixels 1-8
	b := row[0]
	colors := paletteBuf[int(b)*8:]

	// Word 0: RAW_RUN cmd | pixel 1
	buf[ptr] = uint32(COMPOSABLE_RAW_RUN) | (uint32(colors[0]) << 16)
	ptr++
	// Word 1: count | pixel 2
	buf[ptr] = uint32(frameWidth-3) | (uint32(colors[1]) << 16)
	ptr++
	// Words 2-3: pixels 3-6
	buf[ptr] = uint32(colors[2]) | (uint32(colors[3]) << 16)
	ptr++
	buf[ptr] = uint32(colors[4]) | (uint32(colors[5]) << 16)
	ptr++
	// Word 4: pixels 7-8
	buf[ptr] = uint32(colors[6]) | (uint32(colors[7]) << 16)
	ptr++

	// Remaining 79 bytes provide pixels 9-640 (632 pixels = 316 words)
	for i := 1; i < frameWidth/8; i++ {
		b = row[i]
		colors = paletteBuf[int(b)*8:]
		buf[ptr] = uint32(colors[0]) | (uint32(colors[1]) << 16)
		ptr++
		buf[ptr] = uint32(colors[2]) | (uint32(colors[3]) << 16)
		ptr++
		buf[ptr] = uint32(colors[4]) | (uint32(colors[5]) << 16)
		ptr++
		buf[ptr] = uint32(colors[6]) | (uint32(colors[7]) << 16)
		ptr++
	}

	// End of line: after raw_1p fall-through outputs pixel 640,
	// out pc reads this word's low 16 bits as the EOL command
	buf[ptr] = uint32(COMPOSABLE_EOL_SKIP_ALIGN) // cmd in low 16, high 16 ignored
}

// Graphics primitives

func setPixel(x, y int, pen byte) {
	if x < 0 || x >= frameWidth || y < 0 || y >= frameHeight {
		return
	}
	byteIdx := x / 8
	bit := byte(1 << (7 - (x % 8)))
	if pen != 0 {
		frameBuffers[drawBuffer][y][byteIdx] |= bit
	} else {
		frameBuffers[drawBuffer][y][byteIdx] &^= bit
	}
}

func drawLine(x0, y0, x1, y1 int, pen byte) {
	dx := abs(x1 - x0)
	dy := abs(y1 - y0)
	sx, sy := 1, 1
	if x0 >= x1 {
		sx = -1
	}
	if y0 >= y1 {
		sy = -1
	}
	err := dx - dy
	for {
		setPixel(x0, y0, pen)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

func drawRect(x0, y0, x1, y1 int, pen byte) {
	drawLine(x0, y0, x1, y0, pen)
	drawLine(x1, y0, x1, y1, pen)
	drawLine(x1, y1, x0, y1, pen)
	drawLine(x0, y1, x0, y0, pen)
}

func clearScreen(pen byte) {
	var fill byte
	if pen != 0 {
		fill = 0xFF
	}
	for y := 0; y < frameHeight; y++ {
		for x := 0; x < frameWidth/8; x++ {
			frameBuffers[drawBuffer][y][x] = fill
		}
	}
}

// swapBuffers swaps the draw and show buffers (call during vblank)
func swapBuffers() {
	showBuffer.Set(uint32(drawBuffer))
	drawBuffer = 1 - drawBuffer
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func drawPattern() {
	// Border - matches C example: 15 boxes with alternating colors
	// In 1bpp mode, color alternates between 0 (background) and 1 (foreground)
	for i := 1; i < 16; i++ {
		color := byte(i % 2) // Alternates 1, 0, 1, 0, ...
		drawRect(i-1, i-1, frameWidth-i, frameHeight-i, color)
	}

	// Text at animated position - matches C example exactly
	// C uses: h=20, w=10, dx=w+10=20, dy=h+10=30
	drawText(anim.x, anim.y, "HELLO", 1)
	drawText(anim.x, anim.y+30, "WORLD!", 1) // dy=30 matches C example (h+10 = 20+10)
}

func drawText(x, y int, text string, pen byte) {
	// C example: dx = w + 10 = 10 + 10 = 20
	for _, c := range text {
		drawChar(x, y, byte(c), pen)
		x += 20
	}
}

func drawChar(x, y int, c byte, pen byte) {
	// Match C example EXACTLY: w=10, h=20, H=h/2=10, W=w/2=5
	const w = 10
	const h = 20
	const H = h / 2 // 10
	const W = w / 2 // 5

	switch c {
	case 'H':
		// C: gfx_line(x+0, y+0, x+0, y+h), gfx_line(x+w, y+0, x+w, y+h), gfx_line(x+0, y+H, x+w, y+H)
		drawLine(x, y, x, y+h, pen)
		drawLine(x+w, y, x+w, y+h, pen)
		drawLine(x, y+H, x+w, y+H, pen)
	case 'E':
		// C: vertical, top, middle, bottom
		drawLine(x, y, x, y+h, pen)
		drawLine(x, y, x+w, y, pen)
		drawLine(x, y+H, x+w, y+H, pen)
		drawLine(x, y+h, x+w, y+h, pen)
	case 'L':
		// C: vertical, bottom
		drawLine(x, y, x, y+h, pen)
		drawLine(x, y+h, x+w, y+h, pen)
	case 'O':
		// C: left, right, top, bottom
		drawLine(x, y, x, y+h, pen)
		drawLine(x+w, y, x+w, y+h, pen)
		drawLine(x, y, x+w, y, pen)
		drawLine(x, y+h, x+w, y+h, pen)
	case 'W':
		// C: left, right, bottom-left diagonal, bottom-right diagonal
		drawLine(x, y, x, y+h, pen)
		drawLine(x+w, y, x+w, y+h, pen)
		drawLine(x, y+h, x+W, y+H, pen)
		drawLine(x+w, y+h, x+W, y+H, pen)
	case 'R':
		// C: vertical, top right vertical, top, middle, diagonal leg
		drawLine(x, y, x, y+h, pen)
		drawLine(x+w, y, x+w, y+H, pen)
		drawLine(x, y, x+w, y, pen)
		drawLine(x, y+H, x+w, y+H, pen)
		drawLine(x+W, y+H, x+w, y+h, pen)
	case 'D':
		// C: left vertical (at W/2), top, right, bottom
		drawLine(x+W/2, y, x+W/2, y+h, pen)
		drawLine(x, y, x+w, y, pen)
		drawLine(x+w, y, x+w, y+h, pen)
		drawLine(x, y+h, x+w, y+h, pen)
	case '!':
		// C: vertical line, dot at bottom
		drawLine(x, y, x, y+H+H/2, pen)
		drawLine(x, y+h-2, x, y+h, pen)
	}
}

func blinkError(led machine.Pin) {
	for {
		led.Low()
		time.Sleep(100 * time.Millisecond)
		led.High()
		time.Sleep(100 * time.Millisecond)
	}
}
