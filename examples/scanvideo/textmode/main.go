// TinyGo rough translation of pico-playground/scanvideo/textmode
// NOTE: Full implementation requires porting the font rendering system.
// This is a simplified version showing the structure with a basic 8x8 font.
//
// The original textmode shows:
// - Font rendering with 4-bit grayscale antialiasing
// - Fragment DMA for efficient character rendering
// - Horizontal scrolling text

package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

var vgaMode = &scanvideo.Mode640x480_60

const (
	fontWidth  = 8
	fontHeight = 8
	charsPerRow = 80
	numRows     = 60
)

// Simple 8x8 font (subset of ASCII 32-126)
// Each character is 8 bytes, one byte per row
var font8x8 = [95][8]uint8{
	// Space (32)
	{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	// ! (33)
	{0x18, 0x3C, 0x3C, 0x18, 0x18, 0x00, 0x18, 0x00},
	// More characters would go here...
	// For brevity, we'll just define a few
}

var (
	hpos       int16 = 0
	textBuffer [numRows][charsPerRow]byte
)

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(100 * time.Millisecond)
	println("textmode starting (simplified version)...")

	// Fill text buffer with sample text
	initTextBuffer()

	if !scanvideo.Setup(vgaMode) {
		println("Failed to setup video!")
		for {
			time.Sleep(time.Second)
		}
	}

	scanvideo.TimingEnable(true)
	println("Video started")

	go renderLoop()

	// Animation loop
	for {
		scanvideo.WaitForVblank()
		hpos++ // Scroll text

		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == 'r' {
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}
	}
}

func initTextBuffer() {
	msg := "TINYGO TEXTMODE DEMO - PICO VGA "
	for y := 0; y < numRows; y++ {
		for x := 0; x < charsPerRow; x++ {
			textBuffer[y][x] = msg[(x+y)%len(msg)]
		}
	}
}

func renderLoop() {
	for {
		buffer := scanvideo.BeginScanlineGeneration(true)
		if buffer == nil {
			continue
		}
		renderScanline(buffer)
		scanvideo.EndScanlineGeneration(buffer)
	}
}

// renderScanline renders a line of text as colored pixels
func renderScanline(buffer *scanvideo.ScanlineBuffer) {
	lineNum := scanvideo.ScanlineNumber(buffer.ScanlineID)

	// Which text row and font row?
	textRow := int(lineNum / fontHeight)
	fontRow := int(lineNum % fontHeight)

	if textRow >= numRows {
		// Blank line
		buffer.Data[0] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (0 << 16)
		buffer.Data[1] = uint32(vgaMode.Width-3) | (uint32(scanvideo.COMPOSABLE_RAW_1P) << 16)
		buffer.Data[2] = 0 | (uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16)
		buffer.DataUsed = 3
		buffer.Status = scanvideo.ScanlineOK
		return
	}

	// Build RAW_RUN for this line (simple approach)
	idx := 0

	// RAW_RUN header
	buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_RAW_RUN)
	idx++

	// First pixel + count
	var firstPixel uint16 = 0
	count := uint16(vgaMode.Width - 2)

	// Pack first pixel and count
	buffer.Data[idx] = uint32(firstPixel) | (uint32(count) << 16)
	idx++

	// Generate pixels for this scanline (simplified: just show gray bars for characters)
	pixelIdx := 2
	for col := 0; col < charsPerRow && pixelIdx < int(vgaMode.Width); col++ {
		ch := textBuffer[textRow][col]
		charIdx := int(ch) - 32
		if charIdx < 0 || charIdx >= 95 {
			charIdx = 0 // Space
		}

		// Get font data for this row of the character
		var fontByte uint8 = 0
		if charIdx < len(font8x8) {
			fontByte = font8x8[charIdx][fontRow]
		}

		// Generate 8 pixels for this character
		for bit := 7; bit >= 0 && pixelIdx < int(vgaMode.Width); bit-- {
			var pixel uint16
			if fontByte&(1<<bit) != 0 {
				// Foreground (white/green gradient based on row)
				pixel = scanvideo.RGB565_5bit(31, uint8(31-textRow/2), uint8(textRow/2))
			} else {
				// Background (dark blue)
				pixel = scanvideo.RGB565_5bit(0, 0, uint8(4+textRow/10))
			}

			// Pack two pixels per word
			if pixelIdx%2 == 0 {
				buffer.Data[idx] = uint32(pixel)
			} else {
				buffer.Data[idx] |= uint32(pixel) << 16
				idx++
			}
			pixelIdx++
		}
	}

	// Finish any remaining pixels
	if pixelIdx%2 == 1 {
		idx++
	}

	// End of line
	buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_RAW_1P) | (0 << 16)
	idx++
	buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_EOL_SKIP_ALIGN)
	idx++

	buffer.DataUsed = uint16(idx)
	buffer.Status = scanvideo.ScanlineOK
}
