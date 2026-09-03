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
	time.Sleep(500 * time.Millisecond)
	println("scanvideo_minimal starting...")
	time.Sleep(100 * time.Millisecond)

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

	println("About to enable timing...")
	time.Sleep(100 * time.Millisecond)

	// Start video output (launches video loop goroutine internally)
	scanvideo.TimingEnable(true)

	time.Sleep(100 * time.Millisecond)
	println("Video started")

	// LED for visual feedback
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Main loop handles serial input
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

// renderScanline fills a buffer with pixel data using RAW_RUN
// Uses same solid color per line as COLOR_RUN version for fair comparison
func renderScanline(buffer *scanvideo.ScanlineBuffer) {
	lineNum := scanvideo.ScanlineNumber(buffer.ScanlineID)
	width := int(vgaMode.Width)

	// Create color from line number (same as COLOR_RUN version)
	color := uint16(lineNum << 2)

	ptr := 0

	// RAW_RUN header: command + first pixel
	buffer.Data[ptr] = uint32(scanvideo.COMPOSABLE_RAW_RUN) | (uint32(color) << 16)
	ptr++

	// Word 1: count + second pixel
	// Count = width - 2 (pixel0 and pixel1 are in header, rest in loop)
	buffer.Data[ptr] = uint32(width-2) | (uint32(color) << 16)
	ptr++

	// Remaining pixels in pairs (all same color)
	colorPair := uint32(color) | (uint32(color) << 16)
	for i := 2; i < width; i += 2 {
		buffer.Data[ptr] = colorPair
		ptr++
	}

	// End of line: RAW_1P + black pixel
	buffer.Data[ptr] = uint32(scanvideo.COMPOSABLE_RAW_1P) | (0 << 16)
	ptr++
	// EOL_ALIGN in high bits (matching C gvga pattern)
	// Low bits = 0 works because offset 0 flows to offset 1
	buffer.Data[ptr] = uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16

	buffer.DataUsed = uint16(ptr)
	buffer.Status = scanvideo.ScanlineOK
}
