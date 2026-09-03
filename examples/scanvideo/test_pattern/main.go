// TinyGo port of pico-playground/scanvideo/test_pattern
// Draws 7 colored bars (vertical gradient) for testing resistor DAC
// Press SPACE to invert colors

package main

import (
	"machine"
	"runtime"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

var vgaMode = &scanvideo.Mode640x480_60

var invert bool

var led = machine.LED

func blinkPattern(count int, onMs, offMs time.Duration) {
	for i := 0; i < count; i++ {
		led.High()
		time.Sleep(onMs)
		led.Low()
		time.Sleep(offMs)
	}
}

func main() {
	// Initialize LED first for debug
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Blink 1: Starting (1 long blink)
	blinkPattern(1, 500*time.Millisecond, 500*time.Millisecond)

	// Initialize serial for debug output and reboot
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})

	// Wait longer for USB to enumerate
	time.Sleep(2 * time.Second)

	println("=====================================")
	println("test_pattern starting...")
	println("=====================================")

	// Blink 2: About to call Setup (2 quick blinks)
	blinkPattern(2, 100*time.Millisecond, 100*time.Millisecond)

	println("About to call scanvideo.Setup()...")
	time.Sleep(100 * time.Millisecond)

	// Initialize video
	if !scanvideo.Setup(vgaMode) {
		println("Failed to setup video!")
		// Error: rapid blinking
		for {
			led.High()
			time.Sleep(100 * time.Millisecond)
			led.Low()
			time.Sleep(100 * time.Millisecond)
		}
	}
	println("Setup returned successfully!")

	// Blink 3: Setup succeeded (3 quick blinks)
	blinkPattern(3, 100*time.Millisecond, 100*time.Millisecond)

	// Start video output
	println("Calling TimingEnable...")
	scanvideo.TimingEnable(true)
	println("TimingEnable returned!")

	// Blink 4: TimingEnable succeeded (4 quick blinks)
	blinkPattern(4, 100*time.Millisecond, 100*time.Millisecond)

	println("Color bars ready, press SPACE to invert...")

	// Launch render loop in goroutine (runs on core1)
	go renderLoop()

	// Main loop handles serial input
	for {
		// Use time.Sleep instead of busy-wait WaitForVblank
		// This allows USB CDC to process serial data
		// Sleep ~16ms for ~60Hz frame rate
		time.Sleep(16 * time.Millisecond)

		// Check for input
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			switch b {
			case ' ':
				invert = !invert
				if invert {
					println("Inverted: true")
				} else {
					println("Inverted: false")
				}
			case 'r':
				println("Rebooting to bootloader...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}
	}
}

func renderLoop() {
	for {
		// Get a scanline buffer (blocks until one is available)
		buffer := scanvideo.BeginScanlineGeneration(true)
		if buffer == nil {
			runtime.Gosched() // Yield to other goroutines
			continue
		}

		// Render the scanline
		drawColorBar(buffer)

		// Release buffer for display
		scanvideo.EndScanlineGeneration(buffer)

		// Yield periodically to allow other goroutines to run
		runtime.Gosched()
	}
}

// drawColorBar fills a scanline with 32 vertical color bars
// This matches the C test_pattern.c exactly
func drawColorBar(buffer *scanvideo.ScanlineBuffer) {
	lineNum := scanvideo.ScanlineNumber(buffer.ScanlineID)

	// Figure out primary color based on vertical position (7 color bands)
	// 1 + (line * 7 / height) gives: 1,2,3,4,5,6,7 for each band
	primaryColor := uint32(1 + (uint32(lineNum) * 7 / uint32(vgaMode.Height)))

	// Create color mask from primary color bits
	// bit 0 -> red, bit 1 -> green, bit 2 -> blue
	colorMask := uint16(
		(0x1f * (primaryColor & 1)) | // R: 5 bits
			((0x1f * ((primaryColor >> 1) & 1)) << 5) | // G: 5 bits (shifted by 5 for RGB555)
			((0x1f * ((primaryColor >> 2) & 1)) << 10)) // B: 5 bits (shifted by 10 for RGB555)

	barWidth := uint16(vgaMode.Width / 32)

	// Invert mask
	var invertBits uint16
	if invert {
		invertBits = uint16(0x1f | (0x1f << 5) | (0x1f << 10)) // RGB555 white
	}

	// Build scanline with COLOR_RUN commands for each bar
	idx := 0
	for bar := uint16(0); bar < 32; bar++ {
		// Each bar: [COLOR_RUN | color] [count-3 | next_cmd]
		color := (uint16(bar) | (uint16(bar) << 5) | (uint16(bar) << 10)) // Gray gradient
		color &= colorMask
		color ^= invertBits

		buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(color) << 16)
		idx++
		buffer.Data[idx] = uint32(barWidth - 3)
		if bar < 31 {
			// Next command is COLOR_RUN
			buffer.Data[idx] |= uint32(scanvideo.COMPOSABLE_COLOR_RUN) << 16
		} else {
			// Last bar: next command is RAW_1P
			buffer.Data[idx] |= uint32(scanvideo.COMPOSABLE_RAW_1P) << 16
		}
		idx++
	}

	// Black pixel to end line (RAW_1P already specified above)
	buffer.Data[idx] = 0 // black pixel
	idx++

	// End of line with alignment padding
	buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_EOL_SKIP_ALIGN)
	idx++

	buffer.DataUsed = uint16(idx)
	buffer.Status = scanvideo.ScanlineOK
}
