// TinyGo rough translation of pico-playground/scanvideo/demo1
// NOTE: Full implementation requires porting the span rendering system
// and the pi400 image data. This is a simplified version showing the structure.
//
// The original demo1 shows a bouncing Raspberry Pi 400 image with:
// - Sprite rendering using span-based drawing
// - 4-bit palette indexed images
// - Animation with bouncing logic
// - Optional performance counter display

package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

var vgaMode = &scanvideo.Mode640x480_60

var (
	left   int16 = 200
	top    int16 = 100
	hspeed int16 = -1
	vspeed int16 = 1

	// Placeholder image dimensions (pi400 is about 299x159)
	imgWidth  int16 = 100
	imgHeight int16 = 80
)

// Background color (orange-ish)
var bgColor = scanvideo.RGB565_5bit(0x1f, 0x10, 0x00) // ~orange

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(100 * time.Millisecond)
	println("demo1 starting (simplified version)...")

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

		// Update animation (bouncing box)
		left += hspeed
		if left < -imgWidth/2 {
			left = -imgWidth / 2
			hspeed = -hspeed
		} else if left >= int16(vgaMode.Width)-imgWidth/2 {
			left = int16(vgaMode.Width) - imgWidth/2
			hspeed = -hspeed
		}

		top += vspeed
		if top < -imgHeight/2 {
			top = -imgHeight / 2
			vspeed = -vspeed
		} else if top >= int16(vgaMode.Height)-imgHeight/2 {
			top = int16(vgaMode.Height) - imgHeight/2
			vspeed = -vspeed
		}

		// Check for reboot
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

// renderScanline draws a simplified version: solid background with a colored box
func renderScanline(buffer *scanvideo.ScanlineBuffer) {
	lineNum := int16(scanvideo.ScanlineNumber(buffer.ScanlineID))

	// Check if this line is within the sprite bounds
	spriteY := lineNum - top
	inSprite := spriteY >= 0 && spriteY < imgHeight

	idx := 0
	if !inSprite || left >= int16(vgaMode.Width) || left+imgWidth <= 0 {
		// Solid background line
		buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(bgColor) << 16)
		idx++
		buffer.Data[idx] = uint32(vgaMode.Width-3) | (uint32(scanvideo.COMPOSABLE_RAW_1P) << 16)
		idx++
		buffer.Data[idx] = 0 | (uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16)
		idx++
	} else {
		// Draw: [before] [sprite] [after]
		beforeWidth := left
		if beforeWidth < 0 {
			beforeWidth = 0
		}
		afterStart := left + imgWidth
		if afterStart > int16(vgaMode.Width) {
			afterStart = int16(vgaMode.Width)
		}
		spriteWidth := afterStart - beforeWidth
		if left < 0 {
			spriteWidth += left
		}
		afterWidth := int16(vgaMode.Width) - afterStart

		// Create a gradient color for the sprite based on position
		spriteColor := scanvideo.RGB565_5bit(
			uint8(spriteY*31/imgHeight),
			uint8(31-(spriteY*31/imgHeight)),
			uint8(15),
		)

		// Before (background)
		if beforeWidth >= 3 {
			buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(bgColor) << 16)
			idx++
			buffer.Data[idx] = uint32(beforeWidth-3) | (uint32(scanvideo.COMPOSABLE_COLOR_RUN) << 16)
			idx++
		} else if beforeWidth == 2 {
			buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_RAW_2P) | (uint32(bgColor) << 16)
			idx++
			buffer.Data[idx] = uint32(bgColor) | (uint32(scanvideo.COMPOSABLE_COLOR_RUN) << 16)
			idx++
		} else if beforeWidth == 1 {
			buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_RAW_1P) | (uint32(bgColor) << 16)
			idx++
			buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_COLOR_RUN)
			idx++
		}

		// Sprite (colored box)
		if spriteWidth >= 3 {
			buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(spriteColor) << 16)
			idx++
			buffer.Data[idx] = uint32(spriteWidth - 3)
			if afterWidth > 0 {
				buffer.Data[idx] |= uint32(scanvideo.COMPOSABLE_COLOR_RUN) << 16
			} else {
				buffer.Data[idx] |= uint32(scanvideo.COMPOSABLE_RAW_1P) << 16
			}
			idx++
		}

		// After (background)
		if afterWidth >= 3 {
			buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (uint32(bgColor) << 16)
			idx++
			buffer.Data[idx] = uint32(afterWidth-3) | (uint32(scanvideo.COMPOSABLE_RAW_1P) << 16)
			idx++
		} else if afterWidth == 2 {
			buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_RAW_2P) | (uint32(bgColor) << 16)
			idx++
			buffer.Data[idx] = uint32(bgColor) | (uint32(scanvideo.COMPOSABLE_RAW_1P) << 16)
			idx++
		} else if afterWidth == 1 {
			buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_RAW_1P) | (uint32(bgColor) << 16)
			idx++
		}

		// End of line
		buffer.Data[idx] = 0 | (uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16)
		idx++
	}

	buffer.DataUsed = uint16(idx)
	buffer.Status = scanvideo.ScanlineOK
}
