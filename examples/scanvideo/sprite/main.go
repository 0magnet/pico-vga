// TinyGo rough translation of pico-playground/scanvideo/sprite concepts
// NOTE: Full implementation requires porting the sprite rendering library.
// This is a simplified version demonstrating basic sprite blitting.
//
// The original sprite library provides:
// - 8bpp and 16bpp sprite rendering
// - Alpha blending
// - Affine transformations using RP2040 interpolators
// - Opacity metadata for optimized rendering

package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

var vgaMode = &scanvideo.Mode320x240_60

const (
	spriteSize   = 32
	numSprites   = 4
)

// Simple sprite structure
type sprite struct {
	x, y   int16
	vx, vy int16
	color  uint16
}

var sprites = [numSprites]sprite{
	{x: 50, y: 50, vx: 2, vy: 1, color: 0x7C00},    // Blue
	{x: 100, y: 100, vx: -1, vy: 2, color: 0x03E0}, // Green
	{x: 200, y: 50, vx: 1, vy: -1, color: 0x001F},  // Red
	{x: 150, y: 150, vx: -2, vy: -2, color: 0x7FFF}, // White
}

var bgColor = scanvideo.RGB565_5bit(2, 2, 8) // Dark blue

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(100 * time.Millisecond)
	println("sprite demo starting...")

	if !scanvideo.Setup(vgaMode) {
		println("Failed to setup video!")
		for {
			time.Sleep(time.Second)
		}
	}

	scanvideo.TimingEnable(true)
	println("Video started - sprites bouncing")

	go renderLoop()

	// Animation loop
	for {
		scanvideo.WaitForVblank()
		updateSprites()

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

func updateSprites() {
	for i := range sprites {
		s := &sprites[i]
		s.x += s.vx
		s.y += s.vy

		// Bounce off edges
		if s.x < 0 || s.x >= int16(vgaMode.Width)-spriteSize {
			s.vx = -s.vx
			s.x += s.vx
		}
		if s.y < 0 || s.y >= int16(vgaMode.Height)-spriteSize {
			s.vy = -s.vy
			s.y += s.vy
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

func renderScanline(buffer *scanvideo.ScanlineBuffer) {
	lineNum := int16(scanvideo.ScanlineNumber(buffer.ScanlineID))

	// Build a simple scanline buffer with sprites
	// Use RAW_RUN to output pixel-by-pixel

	idx := 0
	width := int(vgaMode.Width)

	// Use RAW_RUN for full scanline
	buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_RAW_RUN)
	idx++

	// First pixel and count
	firstPixel := getPixelColor(0, lineNum)
	count := uint16(width - 2)
	buffer.Data[idx] = uint32(firstPixel) | (uint32(count) << 16)
	idx++

	// Generate remaining pixels
	for x := 1; x < width; x += 2 {
		p0 := getPixelColor(int16(x), lineNum)
		p1 := uint16(0)
		if x+1 < width {
			p1 = getPixelColor(int16(x+1), lineNum)
		}
		buffer.Data[idx] = uint32(p0) | (uint32(p1) << 16)
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

func getPixelColor(x, y int16) uint16 {
	// Check each sprite (back to front)
	for i := numSprites - 1; i >= 0; i-- {
		s := &sprites[i]
		// Check if pixel is inside this sprite
		sx := x - s.x
		sy := y - s.y
		if sx >= 0 && sx < spriteSize && sy >= 0 && sy < spriteSize {
			// Simple circular sprite with gradient
			cx := sx - spriteSize/2
			cy := sy - spriteSize/2
			distSq := int(cx*cx + cy*cy)
			radiusSq := (spriteSize / 2) * (spriteSize / 2)

			if distSq < radiusSq {
				// Inside sprite - apply color with distance-based shading
				shade := 31 - (distSq * 20 / radiusSq)
				if shade < 8 {
					shade = 8
				}
				// Mix sprite color with shade
				r := (uint16(s.color&0x001F) * uint16(shade)) / 31
				g := (uint16((s.color>>5)&0x003F) * uint16(shade)) / 31
				b := (uint16((s.color>>11)&0x001F) * uint16(shade)) / 31
				return r | (g << 5) | (b << 11)
			}
		}
	}
	// Background
	return bgColor
}
