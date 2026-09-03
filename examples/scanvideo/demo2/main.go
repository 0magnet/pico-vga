// TinyGo rough translation of pico-playground/scanvideo/demo2
// NOTE: Full implementation requires porting the span rendering system
// and multiple image sprites. This is a simplified version showing the structure.
//
// The original demo2 shows two bouncing sprites (pi400 and riscv logos) with:
// - Multiple sprite rendering with overlap blending
// - Dual-core rendering support
// - Animation with independent speeds per sprite

package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

var vgaMode = &scanvideo.Mode640x480_60

// Two sprites
var sprites = [2]struct {
	left, top       int16
	hspeed, vspeed  int16
	width, height   int16
	color           uint16
}{
	{left: 100, top: 50, hspeed: -4, vspeed: 1, width: 80, height: 60, color: 0x7C00}, // Blue sprite
	{left: 400, top: 200, hspeed: 1, vspeed: 2, width: 100, height: 80, color: 0x001F}, // Red sprite
}

var bgColor = scanvideo.RGB565_5bit(0x1f, 0x10, 0x00) // Orange background

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(100 * time.Millisecond)
	println("demo2 starting (simplified version)...")

	if !scanvideo.Setup(vgaMode) {
		println("Failed to setup video!")
		for {
			time.Sleep(time.Second)
		}
	}

	scanvideo.TimingEnable(true)
	println("Video started")

	go renderLoop()

	// Animation and input loop
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
		s.left += s.hspeed
		if s.left < -s.width/2 || s.left >= int16(vgaMode.Width)-s.width/2 {
			s.hspeed = -s.hspeed
			s.left += s.hspeed
		}

		s.top += s.vspeed
		if s.top < -s.height/2 || s.top >= int16(vgaMode.Height)-s.height/2 {
			s.vspeed = -s.vspeed
			s.top += s.vspeed
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

// renderScanline draws background with two colored boxes representing sprites
func renderScanline(buffer *scanvideo.ScanlineBuffer) {
	lineNum := int16(scanvideo.ScanlineNumber(buffer.ScanlineID))

	// Find which sprites are visible on this line
	var visibleSprites []int
	for i, s := range sprites {
		spriteY := lineNum - s.top
		if spriteY >= 0 && spriteY < s.height && s.left < int16(vgaMode.Width) && s.left+s.width > 0 {
			visibleSprites = append(visibleSprites, i)
		}
	}

	if len(visibleSprites) == 0 {
		// Solid background
		buffer.Data[0] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(bgColor) << 16)
		buffer.Data[1] = uint32(vgaMode.Width-3) | (uint32(scanvideo.COMPOSABLE_RAW_1P) << 16)
		buffer.Data[2] = 0 | (uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16)
		buffer.DataUsed = 3
	} else {
		// Simple rendering: just use single COLOR_RUN spans
		// A full implementation would handle overlapping sprites with blending
		idx := 0
		x := int16(0)

		for x < int16(vgaMode.Width) {
			// Find if we're in any sprite at position x
			var inSprite int = -1
			for _, si := range visibleSprites {
				s := &sprites[si]
				if x >= s.left && x < s.left+s.width {
					inSprite = si
					break
				}
			}

			if inSprite >= 0 {
				// Sprite segment
				s := &sprites[inSprite]
				segEnd := s.left + s.width
				if segEnd > int16(vgaMode.Width) {
					segEnd = int16(vgaMode.Width)
				}
				segLen := segEnd - x

				if segLen >= 3 {
					buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(s.color) << 16)
					idx++
					buffer.Data[idx] = uint32(segLen - 3)
					x = segEnd
				} else {
					x++
					continue
				}
			} else {
				// Background segment until next sprite or end
				segEnd := int16(vgaMode.Width)
				for _, si := range visibleSprites {
					s := &sprites[si]
					if s.left > x && s.left < segEnd {
						segEnd = s.left
					}
				}
				segLen := segEnd - x

				if segLen >= 3 {
					buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(bgColor) << 16)
					idx++
					buffer.Data[idx] = uint32(segLen - 3)
					x = segEnd
				} else {
					x++
					continue
				}
			}

			// Determine next command
			if x >= int16(vgaMode.Width) {
				buffer.Data[idx] |= uint32(scanvideo.COMPOSABLE_RAW_1P) << 16
			} else {
				buffer.Data[idx] |= uint32(scanvideo.COMPOSABLE_COLOR_RUN) << 16
			}
			idx++
		}

		// End of line
		buffer.Data[idx] = 0 | (uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16)
		idx++
		buffer.DataUsed = uint16(idx)
	}

	buffer.Status = scanvideo.ScanlineOK
}
