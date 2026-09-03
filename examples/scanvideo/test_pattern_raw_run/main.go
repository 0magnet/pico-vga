// TinyGo port of pico-playground/scanvideo/demo1 render_scanline_test_pattern function
// This example tests COMPOSABLE_RAW_RUN functionality
// Displays the "infamous grey/white triangle" pattern with RAW_RUN at end of each line
//
// Port of: pico-playground/scanvideo/demo1/demo1.c render_scanline_test_pattern()

package main

import (
	"machine"
	"runtime/volatile"
	"time"
	"unsafe"

	"github.com/0magnet/pico-vga/scanvideo"
)

var vgaMode = &scanvideo.Mode320x240_60

func main() {
	// Initialize serial first - give USB time to enumerate
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})

	// Wait for USB to be ready and flush any pending data
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
	}

	println("")
	println("========================================")
	println("test_pattern_raw_run starting...")
	println("This tests COMPOSABLE_RAW_RUN from demo1")
	println("========================================")
	println("")
	time.Sleep(100 * time.Millisecond)

	println("Setting up video mode 320x240...")

	// Initialize video
	if !scanvideo.Setup(vgaMode) {
		println("ERROR: Failed to setup video!")
		for {
			time.Sleep(time.Second)
		}
	}
	println("Video setup complete")
	time.Sleep(100 * time.Millisecond)

	// Launch render loop in goroutine BEFORE enabling video
	println("Starting render loop goroutine...")
	go renderLoop()

	// Small delay to let render loop start
	time.Sleep(100 * time.Millisecond)
	println("Render loop started")

	println("Enabling video timing...")
	time.Sleep(100 * time.Millisecond)

	// Start video output
	scanvideo.TimingEnable(true)

	time.Sleep(200 * time.Millisecond)
	println("Video timing enabled!")
	println("You should see a grey/white triangle pattern")
	println("with 4 colored pixels (black,blue,red,green)")
	println("on the right side of each line")

	// LED for visual feedback
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Start with RAW_RUN mode by default
	setTestMode(1)

	println("Entering main loop")
	println("Commands: r=reboot, s=status, p=PIO, d=debug, 0=COLOR_RUN, 1=RAW_RUN, 2=demo1")
	println("Starting with mode 1 (RAW_RUN - gradient with 4 colored pixels on right)")

	// Main loop handles serial input
	counter := 0
	for {
		counter++
		if counter%100 == 0 {
			led.Set(!led.Get())
		}
		// Periodic status every ~10 seconds
		if counter%1000 == 0 {
			stats := scanvideo.GetDebugStats()
			println("loop:", counter, "mode:", getTestMode())
			println("  IRQ0:", stats.IRQ0Count, "IRQ1:", stats.IRQ1Count, "Frames:", stats.FramesComplete)
			println("  DMA:", stats.DMAStarts, "Hits:", stats.BufferHits, "Miss:", stats.BufferMisses)
			println("  LastLine:", stats.LastScanlineNum, "LastUsed:", stats.LastDataUsed)
		}

		// Print captured debug info from render loop
		if debugCapture.ready {
			println("=== Captured Scanline", debugCapture.lineNum, "(mode", getTestMode(), ") ===")
			println("DataUsed:", debugCapture.dataUsed)
			max := int(debugCapture.dataUsed)
			if max > 20 {
				max = 20
			}
			for i := 0; i < max; i++ {
				println("  buf[", i, "] =", hexU32(debugCapture.words[i]))
			}
			debugCapture.ready = false
		}

		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			switch b {
			case 'r':
				println("Rebooting to bootloader...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			case 's':
				stats := scanvideo.GetDebugStats()
				println("=== Full Status ===")
				println("Loop:", counter, "Mode:", getTestMode())
				println("IRQ0 (active):", stats.IRQ0Count)
				println("IRQ1 (vblank):", stats.IRQ1Count)
				println("Frames complete:", stats.FramesComplete)
				println("DMA starts:", stats.DMAStarts)
				println("Buffer hits:", stats.BufferHits)
				println("Buffer misses:", stats.BufferMisses)
				println("Last scanline:", stats.LastScanlineNum)
				println("Last data used:", stats.LastDataUsed)
				println("==================")
			case '0':
				setTestMode(0)
				debugCapture.requested = true
				debugCapture.ready = false
				println("Switched to mode 0: COLOR_RUN (simple gradient)")
			case '1':
				setTestMode(1)
				debugCapture.requested = true
				debugCapture.ready = false
				println("Switched to mode 1: RAW_RUN test")
			case '2':
				setTestMode(2)
				debugCapture.requested = true
				debugCapture.ready = false
				println("Switched to mode 2: demo1 test pattern")
			case 'd':
				// Force debug capture on next frame
				debugCapture.requested = true
				debugCapture.ready = false
				println("Debug capture requested...")
			case 'p':
				// Print PIO status
				pio := scanvideo.GetPIOStatus()
				println("=== PIO Status ===")
				println("Scanline SM enabled:", pio.ScanlineSMEnabled)
				println("Timing SM enabled:", pio.TimingSMEnabled)
				println("Scanline addr:", pio.ScanlineAddr)
				println("Timing addr:", pio.TimingAddr)
				println("FSTAT:", hexU32(pio.ScanlineFSTAT))
				println("IRQ raw:", pio.IRQRaw)
				println("==================")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Test mode: 0 = simple (COLOR_RUN), 1 = RAW_RUN only, 2 = full demo1 pattern
// Using volatile to avoid race between main loop and render goroutine
var testModeReg volatile.Register32

func getTestMode() int {
	return int(testModeReg.Get())
}

func setTestMode(mode int) {
	testModeReg.Set(uint32(mode))
}

// Debug capture - store one scanline's data for printing from main loop
var debugCapture struct {
	requested bool
	ready     bool
	lineNum   uint16
	dataUsed  uint16
	words     [20]uint32
}

func renderLoop() {
	for {
		buffer := scanvideo.BeginScanlineGeneration(true)
		if buffer == nil {
			continue
		}

		// Choose render method based on test mode
		switch getTestMode() {
		case 0:
			simpleTestScanline(buffer) // Known working COLOR_RUN
		case 1:
			testRawRunScanline(buffer) // Test RAW_RUN specifically
		case 2:
			renderScanlineTestPattern(buffer) // Full demo1 pattern
		}

		// Capture first scanline for debug (only when requested via 'd' command)
		lineNum := scanvideo.ScanlineNumber(buffer.ScanlineID)
		if lineNum == 0 && debugCapture.requested && !debugCapture.ready {
			debugCapture.lineNum = lineNum
			debugCapture.dataUsed = buffer.DataUsed
			// Copy first 20 words
			for i := 0; i < 20 && i < int(buffer.DataUsed); i++ {
				debugCapture.words[i] = buffer.Data[i]
			}
			debugCapture.ready = true
			debugCapture.requested = false
		}

		scanvideo.EndScanlineGeneration(buffer)
	}
}

// hexU32 formats a uint32 as hex string
func hexU32(v uint32) string {
	const hexChars = "0123456789ABCDEF"
	return "0x" + string(hexChars[(v>>28)&0xF]) + string(hexChars[(v>>24)&0xF]) +
		string(hexChars[(v>>20)&0xF]) + string(hexChars[(v>>16)&0xF]) +
		string(hexChars[(v>>12)&0xF]) + string(hexChars[(v>>8)&0xF]) +
		string(hexChars[(v>>4)&0xF]) + string(hexChars[v&0xF])
}

// singleColorScanline fills buffer with a single color
// Matches C: single_color_scanline() from demo1.c
func singleColorScanline(buf []uint32, bufLength int, width int, color16 uint32) int {
	const minColorRun = 3
	// | jmp color_run | color | count-3 |
	buf[0] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (color16 << 16)
	buf[1] = uint32(width-minColorRun) | (uint32(scanvideo.COMPOSABLE_RAW_1P) << 16)
	// note we must end with a black pixel
	buf[2] = 0 | (uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16)
	return 3
}

// simpleTestScanline - simple test using just COLOR_RUN (known working)
// If this works, the issue is with RAW_RUN specifically
func simpleTestScanline(dest *scanvideo.ScanlineBuffer) {
	lineNum := scanvideo.ScanlineNumber(dest.ScanlineID)
	color := uint16(lineNum << 2) // gradient based on line

	dest.Data[0] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(color) << 16)
	dest.Data[1] = uint32(vgaMode.Width-3) | (uint32(scanvideo.COMPOSABLE_RAW_1P) << 16)
	dest.Data[2] = 0 | (uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16)
	dest.DataUsed = 3
	dest.Status = scanvideo.ScanlineOK
}

// testRawRunScanline - simpler RAW_RUN test matching demo1 structure
// Uses COLOR_RUN for most of line, RAW_RUN for 4 pixels at end (like demo1)
func testRawRunScanline(dest *scanvideo.ScanlineBuffer) {
	buf := dest.Data
	buf16 := (*[1024]uint16)(unsafe.Pointer(&buf[0]))[:]

	lineNum := scanvideo.ScanlineNumber(dest.ScanlineID)
	// Background color - gradient based on line number
	bgColor := uint16(lineNum << 2)

	pos := 0
	width := int(vgaMode.Width)

	// Most of line: COLOR_RUN with gradient color (width - 6 pixels)
	// Leave 6 pixels for: 4 RAW_RUN pixels + 1 RAW_1P black + some margin
	colorRunLen := width - 6
	if colorRunLen < 3 {
		colorRunLen = 3
	}

	buf16[pos] = scanvideo.COMPOSABLE_COLOR_RUN
	pos++
	buf16[pos] = bgColor
	pos++
	buf16[pos] = uint16(colorRunLen - 3)
	pos++

	// RAW_RUN for 4 pixels: black, blue, red, green (exactly like demo1)
	// Format: | RAW_RUN | p0 | count-3 | p1 | p2 | p3 |
	buf16[pos] = scanvideo.COMPOSABLE_RAW_RUN
	pos++
	buf16[pos] = 0x0000 // p0 (black)
	pos++
	buf16[pos] = 4 - 3 // count = 1 (outputs 4 pixels total)
	pos++
	buf16[pos] = scanvideo.RGB565_5bit(0, 0, 0x1f) // p1 (blue)
	pos++
	buf16[pos] = scanvideo.RGB565_5bit(0x1f, 0, 0) // p2 (red)
	pos++
	buf16[pos] = scanvideo.RGB565_5bit(0, 0x1f, 0) // p3 (green)
	pos++

	// black for EOL
	buf16[pos] = scanvideo.COMPOSABLE_RAW_1P
	pos++
	buf16[pos] = 0
	pos++

	if pos&1 != 0 {
		buf16[pos] = scanvideo.COMPOSABLE_EOL_ALIGN
		pos++
	} else {
		buf16[pos] = scanvideo.COMPOSABLE_EOL_SKIP_ALIGN
		pos++
		pos++ // padding
	}

	dest.DataUsed = uint16(pos >> 1)
	dest.Status = scanvideo.ScanlineOK
}

// renderScanlineTestPattern - the infamous grey/white triangle
// Faithful port of render_scanline_test_pattern() from demo1.c
func renderScanlineTestPattern(dest *scanvideo.ScanlineBuffer) {
	// 1 + line_num red, then white
	buf := dest.Data
	pos := 0
	y := int(scanvideo.ScanlineNumber(dest.ScanlineID))

	if y > int(vgaMode.Width)-10 {
		y = int(vgaMode.Width) - 10
	}
	yy := int(vgaMode.Height) - y

	if yy < 4 {
		// Last 4 lines: solid colors
		colors := []uint32{0xffff, 0x03e0, 0x7c1ef, 0}
		pos = singleColorScanline(buf, int(dest.DataMax), int(vgaMode.Width), colors[yy])
	} else {
		var w int
		if y+1 < int(vgaMode.Width)-6 {
			w = y + 1
		} else {
			w = int(vgaMode.Width) - 6
		}

		var red uint32
		if ((y * 8) / int(vgaMode.Height) & 1) != 0 {
			red = 0x001f
		} else {
			red = 0x021f
		}

		// Cast buf to uint16 slice for 16-bit access
		buf16 := (*[1024]uint16)(unsafe.Pointer(&buf[0]))[:]

		if w > 0 {
			if w == 1 {
				buf16[pos] = scanvideo.COMPOSABLE_RAW_1P
				pos++
			} else if w == 2 {
				buf16[pos] = scanvideo.COMPOSABLE_RAW_2P
				pos++
				buf16[pos] = uint16(red)
				pos++
			} else {
				buf16[pos] = scanvideo.COMPOSABLE_COLOR_RUN
				pos++
			}
			buf16[pos] = uint16(red)
			pos++
			if w > 2 {
				buf16[pos] = uint16(w - 3)
				pos++
			}
		}

		w = int(vgaMode.Width) - w - 6
		if w > 0 {
			buf16[pos] = scanvideo.COMPOSABLE_RAW_2P
			pos++
			buf16[pos] = 0xfc00
			pos++
			buf16[pos] = 0xffe0
			pos++
			c := uint16(0xffff) // white
			if w == 1 {
				buf16[pos] = scanvideo.COMPOSABLE_RAW_1P
				pos++
			} else if w == 2 {
				buf16[pos] = scanvideo.COMPOSABLE_RAW_2P
				pos++
				buf16[pos] = c
				pos++
			} else {
				buf16[pos] = scanvideo.COMPOSABLE_COLOR_RUN
				pos++
			}
			buf16[pos] = c
			pos++
			if w > 2 {
				buf16[pos] = uint16(w - 3)
				pos++
			}
		}

		// RAW_RUN: 4 colored pixels (black, blue, red, green)
		// This is the key test of RAW_RUN functionality
		// Format: | RAW_RUN | p0 | count-3 | p1 | p2 | p3 |
		// RAW_RUN outputs count+3 pixels total, so count=1 means 4 pixels
		buf16[pos] = scanvideo.COMPOSABLE_RAW_RUN
		pos++
		buf16[pos] = 0x0000 // p0 (black)
		pos++
		buf16[pos] = 4 - 3 // len - 3 = 1 (outputs 4 pixels total)
		pos++
		buf16[pos] = scanvideo.RGB565_5bit(0, 0, 0x1f) // p1 (blue)
		pos++
		buf16[pos] = scanvideo.RGB565_5bit(0x1f, 0, 0) // p2 (red)
		pos++
		buf16[pos] = scanvideo.RGB565_5bit(0, 0x1f, 0) // p3 (green)
		pos++

		// black for eol
		buf16[pos] = scanvideo.COMPOSABLE_RAW_1P
		pos++
		buf16[pos] = 0
		pos++

		if pos&1 != 0 {
			buf16[pos] = scanvideo.COMPOSABLE_EOL_ALIGN
			pos++
		} else {
			buf16[pos] = scanvideo.COMPOSABLE_EOL_SKIP_ALIGN
			pos++
			pos++ // skip padding
		}
		// pos is now in 16-bit units, convert to 32-bit
		pos >>= 1
	}

	dest.Status = scanvideo.ScanlineOK
	dest.DataUsed = uint16(pos)
}
