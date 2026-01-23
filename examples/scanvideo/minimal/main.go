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
	time.Sleep(100 * time.Millisecond)
	println("scanvideo_minimal starting...")

	// Initialize video
	if !scanvideo.Setup(vgaMode) {
		println("Failed to setup video!")
		for {
			time.Sleep(time.Second)
		}
	}

	// Start video output (launches render goroutine internally)
	scanvideo.TimingEnable(true)
	println("Video started")

	// Launch render loop in goroutine (runs on core1)
	go renderLoop()

	// Main loop handles serial input
	for {
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
