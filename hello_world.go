// Hello World demo using GVga library with PIO-based VGA output
// Direct port of pico-extras scanvideo approach with DMA
// Build with: tinygo build -target=pico -o hello.uf2 hello_world.go
package main

import (
	"machine"
	"runtime/volatile"
	"time"
	"unsafe"

	pio "github.com/tinygo-org/pio/rp2-pio"
)

const (
	pinHSYNC   = machine.GPIO16
	pinVSYNC   = machine.GPIO17
	pinRGBBase = machine.GPIO0
)

// VGA timing for 640x480@60Hz
const (
	vTotal      = 525
	vSyncStart  = 490
	vSyncEnd    = 492
	frameWidth  = 640
	frameHeight = 480
)

// Color is RGB565 format
type Color uint16

// Standard colors (matching Pimoroni VGA Demo Base wiring)
const (
	colorBlack   Color = 0x0000
	colorWhite   Color = 0xFFFF
	colorRed     Color = 0x001F
	colorGreen   Color = 0x07E0
	colorBlue    Color = 0xF800
	colorYellow  Color = 0x07FF
	colorCyan    Color = 0xFFE0
	colorMagenta Color = 0xF81F
)

// DMA registers
const (
	DMA_BASE = 0x50000000

	// Channel 0 registers
	DMA_CH0_READ_ADDR        = DMA_BASE + 0x000
	DMA_CH0_WRITE_ADDR       = DMA_BASE + 0x004
	DMA_CH0_TRANS_COUNT      = DMA_BASE + 0x008
	DMA_CH0_CTRL_TRIG        = DMA_BASE + 0x00C
	DMA_CH0_AL1_CTRL         = DMA_BASE + 0x010
	DMA_CH0_AL3_TRANS_COUNT  = DMA_BASE + 0x038
	DMA_CH0_AL3_READ_ADDR_TRIG = DMA_BASE + 0x03C

	// DREQ values for PIO0
	DREQ_PIO0_TX0 = 0
	DREQ_PIO0_TX1 = 1
)

// DMA control register bits
const (
	DMA_CTRL_EN          = 1 << 0
	DMA_CTRL_HIGH_PRIO   = 1 << 1
	DMA_CTRL_DATA_SIZE_WORD = 2 << 2 // 32-bit transfers
	DMA_CTRL_INCR_READ   = 1 << 4
	DMA_CTRL_INCR_WRITE  = 0 << 5    // Don't increment write (PIO FIFO)
	DMA_CTRL_TREQ_SEL_SHIFT = 15
)

var (
	dmaCh0ReadAddr   = (*volatile.Register32)(unsafe.Pointer(uintptr(DMA_CH0_READ_ADDR)))
	dmaCh0WriteAddr  = (*volatile.Register32)(unsafe.Pointer(uintptr(DMA_CH0_WRITE_ADDR)))
	dmaCh0TransCount = (*volatile.Register32)(unsafe.Pointer(uintptr(DMA_CH0_TRANS_COUNT)))
	dmaCh0CtrlTrig   = (*volatile.Register32)(unsafe.Pointer(uintptr(DMA_CH0_CTRL_TRIG)))
	dmaCh0Al3TransCount = (*volatile.Register32)(unsafe.Pointer(uintptr(DMA_CH0_AL3_TRANS_COUNT)))
	dmaCh0Al3ReadAddrTrig = (*volatile.Register32)(unsafe.Pointer(uintptr(DMA_CH0_AL3_READ_ADDR_TRIG)))
)

// Composable scanline command offsets (relative to PIO program start)
// These will be adjusted by rgbOffset when building scanlines
const (
	LABEL_ENTRY_POINT   = 0  // entry_point / end_of_scanline_skip_ALIGN
	LABEL_EOL_ALIGN     = 1  // end_of_scanline_ALIGN
	LABEL_COLOR_RUN     = 3  // color_run
	LABEL_RAW_RUN       = 7  // raw_run
	LABEL_RAW_1P        = 11 // raw_1p
	LABEL_RAW_2P        = 13 // raw_2p
)

// These are set after PIO program is loaded to include actual offset
var (
	COMPOSABLE_COLOR_RUN      uint16
	COMPOSABLE_RAW_RUN        uint16
	COMPOSABLE_RAW_1P         uint16
	COMPOSABLE_RAW_2P         uint16
	COMPOSABLE_EOL_SKIP_ALIGN uint16
	COMPOSABLE_EOL_ALIGN      uint16
)

// Frame buffer for 1-bit graphics (640x480 = 38400 bytes)
var frameBuffer [frameHeight][frameWidth / 8]byte

// Palette for 1-bit mode
var palette = [2]Color{colorBlack, colorWhite}

// Scanline buffer for composable commands (double buffered)
// Max size: 640 pixels + commands overhead = ~350 words worst case
const scanlineBufSize = 400
var scanlineBuf [2][scanlineBufSize]uint32
var currentBuf int

// Hello World animation state
type helloState struct {
	x, y   int
	dx, dy int
}

var hello = helloState{x: 100, y: 100, dx: 3, dy: 2}

// PIO state machines
var (
	Pio     *pio.PIO
	rgbSM   pio.StateMachine
	hsyncSM pio.StateMachine
)

func main() {
	time.Sleep(2 * time.Second)
	println("=== Hello World VGA Demo (DMA+PIO) ===")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	// Configure sync pins
	pinHSYNC.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pinVSYNC.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pinHSYNC.High()
	pinVSYNC.High()

	// Configure RGB pins
	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: machine.PinOutput})
		pin.Low()
	}

	// Build PIO programs
	Pio = pio.PIO0

	// HSYNC program (generates timing and IRQ)
	hsyncAsm := pio.AssemblerV0{}
	hsyncProgram := []uint16{
		hsyncAsm.Pull(false, true).Encode(),
		hsyncAsm.Mov(pio.MovDestX, pio.MovSrcOSR).Encode(),
		hsyncAsm.Jmp(pio.JmpXNZeroDec, 2).Encode(),
		hsyncAsm.Set(pio.SetDestPins, 0).Delay(31).Encode(),
		hsyncAsm.Set(pio.SetDestPins, 0).Delay(31).Encode(),
		hsyncAsm.Set(pio.SetDestPins, 0).Delay(29).Encode(),
		hsyncAsm.IRQSet(false, 0).Encode(), // IRQ 0 for CPU
		hsyncAsm.IRQSet(false, 4).Encode(), // IRQ 4 for RGB SM
		hsyncAsm.Set(pio.SetDestPins, 1).Encode(),
		hsyncAsm.Jmp(pio.JmpAlways, 1).Encode(),
	}

	hsyncOffset, err := Pio.AddProgram(hsyncProgram, -1)
	if err != nil {
		println("Failed to load HSYNC program:", err.Error())
		blinkError(led)
		return
	}
	println("HSYNC program at offset", hsyncOffset)

	hsyncSM = Pio.StateMachine(0)
	hsyncSM.TryClaim()

	hsyncCfg := pio.DefaultStateMachineConfig()
	hsyncCfg.SetSetPins(pinHSYNC, 1)
	hsyncCfg.SetClkDivIntFrac(5, 0) // 25 MHz

	pinHSYNC.Configure(machine.PinConfig{Mode: Pio.PinMode()})
	hsyncSM.SetPindirsConsecutive(pinHSYNC, 1, true)
	hsyncSM.Init(hsyncOffset, hsyncCfg)
	hsyncSM.TxPut(1172) // Calibrated for ~31.4 kHz HSYNC

	// RGB program - EXACT COPY from working main.go
	rgbAsm := pio.AssemblerV0{}
	rgbProgram := []uint16{
		// Wait for IRQ 4 from HSYNC (start of back porch)
		rgbAsm.WaitIRQ(true, false, 4).Encode(),           // 0: wait 1 irq 4

		// Back porch - ~104 cycles with pins=0 (black)
		rgbAsm.Set(pio.SetDestX, 25).Encode(),             // 1: set x, 25
		rgbAsm.Mov(pio.MovDestPins, pio.MovSrcNull).Encode(), // 2: mov pins, null
		rgbAsm.Jmp(pio.JmpXNZeroDec, 2).Encode(),          // 3: jmp x-- 2
		rgbAsm.Set(pio.SetDestX, 25).Encode(),             // 4: another 50 cycles
		rgbAsm.Nop().Encode(),                             // 5: nop (loop body)
		rgbAsm.Jmp(pio.JmpXNZeroDec, 5).Encode(),          // 6: jmp x-- 5

		// Active video - 7 color bars + 1 black bar
		rgbAsm.Set(pio.SetDestY, 7).Encode(),              // 7: set y, 7 (7 bars)

		rgbAsm.Pull(false, true).Encode(),                 // 8: pull block
		rgbAsm.Mov(pio.MovDestPins, pio.MovSrcOSR).Encode(), // 9: mov pins, osr
		rgbAsm.Set(pio.SetDestX, 6).Encode(),              // 10: set x, 6 (6 iterations)
		rgbAsm.Nop().Delay(15).Encode(),                   // 11: nop [15] (16 cycles)
		rgbAsm.Jmp(pio.JmpXNZeroDec, 11).Encode(),         // 12: jmp x-- 11 (6*17=102)
		rgbAsm.Nop().Delay(2).Encode(),                    // 13: nop [2] (3 cycles)
		rgbAsm.Jmp(pio.JmpYNZeroDec, 8).Encode(),          // 14: jmp y-- 8 (next bar)

		// 8th bar (black) - same timing as color bars
		rgbAsm.Mov(pio.MovDestPins, pio.MovSrcNull).Encode(), // 15: mov pins, null (BLACK)
		rgbAsm.Set(pio.SetDestX, 6).Encode(),              // 16: set x, 6
		rgbAsm.Nop().Delay(15).Encode(),                   // 17: nop [15]
		rgbAsm.Jmp(pio.JmpXNZeroDec, 17).Encode(),         // 18: jmp x-- 17 (102 cycles)
		rgbAsm.Nop().Delay(2).Encode(),                    // 19: nop [2] (3 cycles)

		// Back to wait for next IRQ
		rgbAsm.Jmp(pio.JmpAlways, 0).Encode(),             // 20: jmp 0
	}

	println("RGB program built, length:", len(rgbProgram))

	rgbOffset, err := Pio.AddProgram(rgbProgram, -1)
	if err != nil {
		println("Failed to load RGB program:", err.Error())
		blinkError(led)
		return
	}
	println("RGB program at offset", rgbOffset)

	// Initialize composable command offsets (absolute PIO addresses)
	COMPOSABLE_COLOR_RUN = uint16(rgbOffset) + LABEL_COLOR_RUN
	COMPOSABLE_RAW_RUN = uint16(rgbOffset) + LABEL_RAW_RUN
	COMPOSABLE_RAW_1P = uint16(rgbOffset) + LABEL_RAW_1P
	COMPOSABLE_RAW_2P = uint16(rgbOffset) + LABEL_RAW_2P
	COMPOSABLE_EOL_SKIP_ALIGN = uint16(rgbOffset) + LABEL_ENTRY_POINT
	COMPOSABLE_EOL_ALIGN = uint16(rgbOffset) + LABEL_EOL_ALIGN

	println("Commands: COLOR_RUN=", COMPOSABLE_COLOR_RUN, "RAW_RUN=", COMPOSABLE_RAW_RUN,
		"RAW_1P=", COMPOSABLE_RAW_1P, "RAW_2P=", COMPOSABLE_RAW_2P,
		"EOL_ALIGN=", COMPOSABLE_EOL_ALIGN)

	rgbSM = Pio.StateMachine(1)
	rgbSM.TryClaim()

	rgbCfg := pio.DefaultStateMachineConfig()
	rgbCfg.SetOutPins(pinRGBBase, 16)
	rgbCfg.SetClkDivIntFrac(5, 0) // 25 MHz
	rgbCfg.SetOutShift(true, false, 32) // Shift right, no autopull
	rgbCfg.SetFIFOJoin(pio.FifoJoinTx)

	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: Pio.PinMode()})
	}
	rgbSM.SetPindirsConsecutive(pinRGBBase, 16, true)
	rgbSM.Init(rgbOffset, rgbCfg)

	// Setup DMA channel 0 for scanline transfer
	setupDMA()

	// Debug: check frame buffer content
	println("Checking frame buffer after draw...")
	nonZeroBytes := 0
	for y := 0; y < 10; y++ {
		for x := 0; x < frameWidth/8; x++ {
			if frameBuffer[y][x] != 0 {
				nonZeroBytes++
			}
		}
	}
	println("Non-zero bytes in first 10 lines:", nonZeroBytes)
	println("Sample FB[5][0]:", frameBuffer[5][0], "FB[100][10]:", frameBuffer[100][10])
	println("Palette[0]=", palette[0], "Palette[1]=", palette[1])

	// Start both state machines
	hsyncSM.SetEnabled(true)
	rgbSM.SetEnabled(true)
	println("PIO state machines started")

	// Clear frame buffer
	clearScreen(0)

	// Draw initial content
	drawHelloWorld()

	// Shared counters
	var frameCount volatile.Register32
	var lineCounter volatile.Register32

	// Render loop on core1 via goroutine
	go func() {
		lineCount := 0

		for {
			// Wait for PIO IRQ 0 (line complete)
			for Pio.GetIRQ()&1 == 0 {
			}
			Pio.ClearIRQ(1)

			lineCount++
			lineCounter.Set(lineCounter.Get() + 1)

			// Sample frame buffer at 7 horizontal positions + black
			if lineCount > 0 && lineCount <= frameHeight {
				scanline := lineCount - 1
				fbRow := frameBuffer[scanline][:]
				// Sample at 7 positions across the line
				for i := 0; i < 7; i++ {
					bytePos := i * 10 // Sample every ~80 pixels
					b := fbRow[bytePos]
					bit := (b >> 7) & 1
					rgbSM.TxPut(uint32(palette[bit]))
				}
				rgbSM.TxPut(0) // 8th = black
			} else {
				// Vertical blanking - all black
				for i := 0; i < 8; i++ {
					rgbSM.TxPut(0)
				}
			}

			// Handle VSYNC
			if lineCount == vSyncStart {
				pinVSYNC.Low()
			} else if lineCount == vSyncEnd {
				pinVSYNC.High()
			} else if lineCount >= vTotal {
				lineCount = 0
				frameCount.Set(frameCount.Get() + 1)
				// Swap buffers at frame boundary
				currentBuf = 1 - currentBuf
			}
		}
	}()

	println("Render loop started")

	// Main loop - animation and status
	lastFrame := uint32(0)
	var lastLineCount volatile.Register32

	// Track line rate in render goroutine
	go func() {
		for {
			time.Sleep(time.Second)
			lastLineCount.Set(lineCounter.Get())
			lineCounter.Set(0)
		}
	}()

	for {
		time.Sleep(time.Second)

		// Check for reboot command
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == 'r' || b == 'R' {
				println("Rebooting to BOOTSEL...")
				time.Sleep(100 * time.Millisecond)
				rebootToBootsel()
			}
		}

		// Animate
		eraseHelloWorld()
		moveHelloWorld()
		drawHelloWorld()

		// Report timing info
		f := frameCount.Get()
		lines := lastLineCount.Get()

		// Sample what we'd push for line 100
		fbRow := frameBuffer[100][:]
		var sampleColors [7]uint32
		for i := 0; i < 7; i++ {
			bytePos := i * 10
			b := fbRow[bytePos]
			bit := (b >> 7) & 1
			sampleColors[i] = uint32(palette[bit])
		}

		println("Frame:", f, "Lines/sec:", lines, "VSYNC~", f-lastFrame, "Hz")
		println("Line100 colors:", sampleColors[0], sampleColors[1], sampleColors[2], sampleColors[3])
		lastFrame = f
	}
}

// rebootToBootsel reboots the Pico into BOOTSEL mode
func rebootToBootsel() {
	// RP2040 ROM provides reset_usb_boot at a known location
	// We can find it via rom_hword_as_ptr(0x14) to get function table,
	// then rom_hword_as_ptr(0x18) to get lookup function

	// Read pointers from ROM header
	fnTable := uintptr(*(*uint16)(unsafe.Pointer(uintptr(0x14))))
	lookupFn := uintptr(*(*uint16)(unsafe.Pointer(uintptr(0x18))))

	println("ROM fn table:", fnTable, "lookup fn:", lookupFn)

	// The lookup function: void* rom_table_lookup(uint16_t *table, uint32_t code)
	// Code for reset_usb_boot is 'UB' = 0x4255

	// Instead of complex function pointer casting, use rom_func_lookup directly
	// by reading rom_table_lookup's code and calling it

	// Actually, let's try the simplest approach: the ROM has reset_usb_boot
	// and we can scan for it or use a known offset

	// For RP2040, reset_usb_boot is typically near the start of ROM functions
	// Try calling rom_table_lookup manually

	// Build the lookup call using raw pointers
	type lookupType func(table uintptr, code uint32) uintptr

	// The lookup function pointer needs Thumb bit set
	lookupPtr := lookupFn | 1
	lookup := *(*lookupType)(unsafe.Pointer(&lookupPtr))

	// Look up reset_usb_boot (code 'UB' = 0x4255)
	code := uint32('U') | (uint32('B') << 8)
	resetFnAddr := lookup(fnTable, code)

	println("reset_usb_boot found at:", resetFnAddr)

	if resetFnAddr == 0 {
		println("ERROR: Could not find reset_usb_boot")
		// Try direct watchdog reset as fallback
		watchdogReset()
		return
	}

	// Call reset_usb_boot(0, 0)
	type resetType func(gpioMask, disableMask uint32)
	resetPtr := resetFnAddr | 1 // Thumb bit
	reset := *(*resetType)(unsafe.Pointer(&resetPtr))

	println("Calling reset_usb_boot(0, 0)...")
	reset(0, 0)

	for {
	}
}

// watchdogReset triggers a watchdog reset as fallback
func watchdogReset() {
	println("Trying watchdog reset...")

	// Watchdog registers
	const WATCHDOG_BASE = 0x40058000
	ctrl := (*volatile.Register32)(unsafe.Pointer(uintptr(WATCHDOG_BASE + 0x00)))
	scratch0 := (*volatile.Register32)(unsafe.Pointer(uintptr(WATCHDOG_BASE + 0x0C)))

	// Set magic value that bootrom checks for USB boot
	scratch0.Set(0xB007C0DE)

	// Trigger watchdog with very short timeout
	// CTRL: bits 30:31 = TRIGGER, bit 24 = ENABLE
	ctrl.Set(1 << 30) // Trigger

	for {
	}
}

// setupDMA configures DMA channel 0 for PIO TX transfers
func setupDMA() {
	// Get PIO0 TX FIFO address for SM1
	pioTxFifo := uintptr(0x50200000 + 0x10 + 1*4) // PIO0_BASE + TXF0 + SM1 offset

	// Configure DMA channel 0
	ctrl := uint32(DMA_CTRL_EN |
		DMA_CTRL_DATA_SIZE_WORD |
		DMA_CTRL_INCR_READ |
		(DREQ_PIO0_TX1 << DMA_CTRL_TREQ_SEL_SHIFT))

	dmaCh0WriteAddr.Set(uint32(pioTxFifo))
	dmaCh0CtrlTrig.Set(ctrl & ^uint32(DMA_CTRL_EN)) // Configure but don't enable yet

	println("DMA configured, write addr:", pioTxFifo)
}

// startDMATransfer starts a DMA transfer of the scanline buffer
func startDMATransfer(wordCount int) {
	buf := &scanlineBuf[currentBuf]
	readAddr := uintptr(unsafe.Pointer(&buf[0]))

	// Set read address and count, then trigger
	dmaCh0ReadAddr.Set(uint32(readAddr))
	dmaCh0TransCount.Set(uint32(wordCount))

	// Enable and trigger
	ctrl := dmaCh0CtrlTrig.Get()
	dmaCh0CtrlTrig.Set(ctrl | DMA_CTRL_EN)
}

// buildScanlineComposable builds a composable scanline from frame buffer
// Returns the number of 32-bit words in the buffer
func buildScanlineComposable(line int) int {
	buf := &scanlineBuf[currentBuf]
	idx := 0
	fbRow := frameBuffer[line][:]

	// Build pixel array from frame buffer
	var pixels [frameWidth]Color
	for byteIdx := 0; byteIdx < frameWidth/8; byteIdx++ {
		b := fbRow[byteIdx]
		basePixel := byteIdx * 8
		for bit := 0; bit < 8; bit++ {
			pixelBit := (b >> (7 - bit)) & 1
			pixels[basePixel+bit] = palette[pixelBit]
		}
	}

	// Use RAW_RUN for the entire line (simplest approach)
	// Format: | RAW_RUN | first_color | count | second_color | ... pairs ... |

	// RAW_RUN: | cmd | first_color |
	buf[idx] = uint32(COMPOSABLE_RAW_RUN) | (uint32(pixels[0]) << 16)
	idx++

	// | count-1 | second_color |
	// RAW_RUN outputs 'count' additional pixels after the first
	// count value is actually count-1 in the loop (jmp x-- 9)
	buf[idx] = uint32(frameWidth-3) | (uint32(pixels[1]) << 16)
	idx++

	// Output remaining pixels in pairs (pixels 2 through frameWidth-2)
	for pixelIdx := 2; pixelIdx < frameWidth-1; pixelIdx += 2 {
		buf[idx] = uint32(pixels[pixelIdx]) | (uint32(pixels[pixelIdx+1]) << 16)
		idx++
	}

	// End with RAW_1P for last pixel and EOL
	buf[idx] = uint32(COMPOSABLE_RAW_1P) | (uint32(pixels[frameWidth-1]) << 16)
	idx++
	buf[idx] = uint32(COMPOSABLE_EOL_ALIGN) | (uint32(0) << 16)
	idx++

	return idx
}

// buildBlackLine builds a black scanline using COLOR_RUN
func buildBlackLine() int {
	buf := &scanlineBuf[currentBuf]
	idx := 0

	// COLOR_RUN: | cmd | color | count-3 | next_cmd |
	buf[idx] = uint32(COMPOSABLE_COLOR_RUN) | (uint32(colorBlack) << 16)
	idx++
	buf[idx] = uint32(frameWidth-5) | (uint32(COMPOSABLE_RAW_2P) << 16)
	idx++
	buf[idx] = uint32(colorBlack) | (uint32(colorBlack) << 16) // Two black pixels
	idx++
	buf[idx] = uint32(COMPOSABLE_RAW_1P) | (uint32(colorBlack) << 16) // Final pixel
	idx++
	buf[idx] = uint32(COMPOSABLE_EOL_ALIGN) | (uint32(0) << 16) // EOL
	idx++

	return idx
}

// Graphics primitives

func setPixel(x, y int, pen byte) {
	if x < 0 || x >= frameWidth || y < 0 || y >= frameHeight {
		return
	}
	byteIdx := x / 8
	mask := byte(1 << (7 - (x % 8)))
	if pen != 0 {
		frameBuffer[y][byteIdx] |= mask
	} else {
		frameBuffer[y][byteIdx] &^= mask
	}
}

func drawLine(x0, y0, x1, y1 int, pen byte) {
	dx := abs(x1 - x0)
	dy := abs(y1 - y0)
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
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

func drawBox(x0, y0, x1, y1 int, pen byte) {
	drawLine(x0, y0, x1, y0, pen)
	drawLine(x1, y0, x1, y1, pen)
	drawLine(x1, y1, x0, y1, pen)
	drawLine(x0, y1, x0, y0, pen)
}

func clearScreen(pen byte) {
	var fillByte byte
	if pen != 0 {
		fillByte = 0xFF
	}
	for y := 0; y < frameHeight; y++ {
		for x := 0; x < frameWidth/8; x++ {
			frameBuffer[y][x] = fillByte
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Hello World drawing

func drawHelloWorld() {
	x := hello.x
	y := hello.y
	h := 20
	w := 10
	H := h / 2
	W := w / 2
	dx := w + 10

	// Draw border boxes
	for i := 1; i < 8; i++ {
		drawBox(i-1, i-1, frameWidth-i, frameHeight-i, 1)
	}

	// H
	drawLine(x, y, x, y+h, 1)
	drawLine(x+w, y, x+w, y+h, 1)
	drawLine(x, y+H, x+w, y+H, 1)
	x += dx

	// E
	drawLine(x, y, x, y+h, 1)
	drawLine(x, y, x+w, y, 1)
	drawLine(x, y+H, x+w, y+H, 1)
	drawLine(x, y+h, x+w, y+h, 1)
	x += dx

	// L
	drawLine(x, y, x, y+h, 1)
	drawLine(x, y+h, x+w, y+h, 1)
	x += dx

	// L
	drawLine(x, y, x, y+h, 1)
	drawLine(x, y+h, x+w, y+h, 1)
	x += dx

	// O
	drawLine(x, y, x, y+h, 1)
	drawLine(x+w, y, x+w, y+h, 1)
	drawLine(x, y, x+w, y, 1)
	drawLine(x, y+h, x+w, y+h, 1)
	x = hello.x
	y += h + 10

	// W
	drawLine(x, y, x, y+h, 1)
	drawLine(x+w, y, x+w, y+h, 1)
	drawLine(x, y+h, x+W, y+H, 1)
	drawLine(x+w, y+h, x+W, y+H, 1)
	x += dx

	// O
	drawLine(x, y, x, y+h, 1)
	drawLine(x+w, y, x+w, y+h, 1)
	drawLine(x, y, x+w, y, 1)
	drawLine(x, y+h, x+w, y+h, 1)
	x += dx

	// R
	drawLine(x, y, x, y+h, 1)
	drawLine(x+w, y, x+w, y+H, 1)
	drawLine(x, y, x+w, y, 1)
	drawLine(x, y+H, x+w, y+H, 1)
	drawLine(x+W, y+H, x+w, y+h, 1)
	x += dx

	// L
	drawLine(x, y, x, y+h, 1)
	drawLine(x, y+h, x+w, y+h, 1)
	x += dx

	// D
	drawLine(x+W/2, y, x+W/2, y+h, 1)
	drawLine(x, y, x+w, y, 1)
	drawLine(x+w, y, x+w, y+h, 1)
	drawLine(x, y+h, x+w, y+h, 1)
	x += dx

	// !
	drawLine(x, y, x, y+H+H/2, 1)
	drawLine(x, y+h-2, x, y+h, 1)
}

func eraseHelloWorld() {
	clearScreen(0)
}

func moveHelloWorld() {
	hello.x += hello.dx
	hello.y += hello.dy
	if hello.x > frameWidth-150 || hello.x < 20 {
		hello.dx = -hello.dx
	}
	if hello.y > frameHeight-80 || hello.y < 20 {
		hello.dy = -hello.dy
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
