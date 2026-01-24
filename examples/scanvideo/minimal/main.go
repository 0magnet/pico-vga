// TinyGo port of pico-playground/scanvideo/scanvideo_minimal
// Simple example showing the minimum code to use scanvideo library
// Displays a horizontal gradient that changes color per line

package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

var vgaMode = &scanvideo.Mode320x240_60

func main() {
	// Initialize serial for debug output and reboot
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})

	// Wait for USB to enumerate (important for serial output)
	time.Sleep(2 * time.Second)

	println("scanvideo_minimal starting...")

	// Initialize video
	if !scanvideo.Setup(vgaMode) {
		println("Failed to setup video!")
		for {
			time.Sleep(time.Second)
		}
	}

	// Launch render loop in goroutine BEFORE enabling video
	// This gives it time to pre-generate some scanlines
	go renderLoop()

	// Small delay to let render loop start
	time.Sleep(50 * time.Millisecond)

	// Start video output
	scanvideo.TimingEnable(true)
	println("Video started")

	// LED for visual feedback
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Serial debug must run in separate goroutine due to TinyGo scheduler
	// The render loop blocks the main goroutine's serial output otherwise
	go func() {
		frameCount := 0
		for {
			frameCount++
			println("Running, frame:", frameCount)
			time.Sleep(1 * time.Second)
		}
	}()

	// Main loop handles serial input and LED
	counter := 0
	for {
		// Toggle LED every ~1 second to show loop is running
		counter++
		if counter%100 == 0 {
			led.Set(!led.Get())
		}

		// Check for reboot command
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == 'r' {
				println("Rebooting to bootloader...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func renderLoop() {
	for {
		// Get a scanline buffer (blocks until one is available)
		buffer := scanvideo.BeginScanlineGeneration(true)
		if buffer == nil {
			continue
		}

		// Render the scanline
		renderScanline(buffer)

		// Release buffer for display
		scanvideo.EndScanlineGeneration(buffer)
	}
}

// renderScanline fills a buffer with a solid color based on line number
// This matches the C scanvideo_minimal example exactly
func renderScanline(buffer *scanvideo.ScanlineBuffer) {
	lineNum := scanvideo.ScanlineNumber(buffer.ScanlineID)

	// Create color from line number - shifts green/red gradient
	bgColor := uint16(lineNum << 2)

	// Fill scanline with single color using COLOR_RUN
	// Format: [COLOR_RUN | color] [count-3 | RAW_1P] [0 | EOL_ALIGN]
	buffer.Data[0] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(bgColor) << 16)
	buffer.Data[1] = uint32(vgaMode.Width-3) | (uint32(scanvideo.COMPOSABLE_RAW_1P) << 16) //nolint
	buffer.Data[2] = 0 | (uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16)
	buffer.DataUsed = 3
	buffer.Status = scanvideo.ScanlineOK
}
