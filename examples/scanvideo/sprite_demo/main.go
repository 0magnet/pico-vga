// TinyGo port of pico-playground/scanvideo/sprite_demo
// Bouncing sprites with 16bpp rendering and alpha blending
//
// Original uses assembly sprite routines and RP2040 interpolators.
// This Go version uses pure Go sprite rendering.

package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

var vgaMode = &scanvideo.Mode320x240_60

const (
	spriteLogSize = 5              // 32x32 sprites
	spriteSize    = 1 << spriteLogSize
	numSprites    = 20
	alphaMask     = 0x8000         // Alpha bit in BGAR5515 format
)

// Sprite structure matching C version
type sprite struct {
	x, y int16
	vx, vy int16
	img  []uint16  // sprite pixel data
}

var sprites [numSprites]sprite

// Pre-generated raspberry-like sprite (32x32)
// Uses BGAR5515 format: bit 15 = alpha, bits 14-10 = blue, 9-5 = green, 4-0 = red
var raspberrySprite [spriteSize * spriteSize]uint16

// Background color - light blue sky
var bgColor = scanvideo.RGB565(0x40, 0xc0, 0xff)

// Scanline buffer for rendering
var scanlineBuf [320]uint16

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(100 * time.Millisecond)
	println("sprite_demo starting...")

	// Generate the raspberry sprite
	generateRaspberrySprite()

	// Initialize sprites with random positions and velocities
	initSprites()

	if !scanvideo.Setup(vgaMode) {
		println("Failed to setup video!")
		for {
			time.Sleep(time.Second)
		}
	}

	go renderLoop()
	time.Sleep(50 * time.Millisecond)

	scanvideo.TimingEnable(true)
	println("Video started")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Animation loop
	frameCount := 0
	for {
		scanvideo.WaitForVblank()
		updateSprites()

		frameCount++
		if frameCount%60 == 0 {
			led.Set(!led.Get())
		}

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

// generateRaspberrySprite creates a simple raspberry-like sprite
func generateRaspberrySprite() {
	cx, cy := spriteSize/2, spriteSize/2
	radius := spriteSize/2 - 2

	for y := 0; y < spriteSize; y++ {
		for x := 0; x < spriteSize; x++ {
			dx := x - cx
			dy := y - cy
			distSq := dx*dx + dy*dy
			radiusSq := radius * radius

			idx := y*spriteSize + x

			if distSq > radiusSq {
				// Outside - transparent (alpha = 0)
				raspberrySprite[idx] = 0
			} else {
				// Inside - raspberry red with shading
				// Create a bumpy raspberry texture
				dist := isqrt(distSq)
				shade := 31 - (dist * 16 / radius)
				if shade < 8 {
					shade = 8
				}

				// Add some "berry bumps"
				bumpX := (x * 5) % 7
				bumpY := (y * 5) % 7
				if bumpX < 2 || bumpY < 2 {
					shade = shade * 3 / 4 // darker in gaps
				}

				// Raspberry color: red with some blue tint
				r := uint16(shade)
				g := uint16(shade / 4)
				b := uint16(shade / 3)

				// BGAR5515: alpha(15) | blue(14-10) | green(9-5) | red(4-0)
				raspberrySprite[idx] = alphaMask | (b << 10) | (g << 5) | r
			}
		}
	}
}

// Simple integer square root
func isqrt(n int) int {
	if n <= 0 {
		return 0
	}
	x := n
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + n/x) / 2
	}
	return x
}

// Simple pseudo-random number generator
var randState uint32 = 12345

func randInt() int {
	randState = randState*1103515245 + 12345
	return int(randState >> 16)
}

func initSprites() {
	xmin := -spriteSize / 2
	xmax := int(vgaMode.Width) - spriteSize/2
	ymin := -spriteSize / 2
	ymax := int(vgaMode.Height) - spriteSize/2

	for i := 0; i < numSprites; i++ {
		sprites[i].x = int16(randInt()%(xmax-xmin) + xmin)
		sprites[i].y = int16(randInt()%(ymax-ymin) + ymin)
		sprites[i].vx = randomVelocity()
		sprites[i].vy = randomVelocity()
		sprites[i].img = raspberrySprite[:]
	}
}

func randomVelocity() int16 {
	// Never return 0
	v := int16(randInt()%5 + 1)
	if randInt()&1 == 0 {
		v = -v
	}
	return v
}

func updateSprites() {
	xmin := int16(-spriteSize / 2)
	xmax := int16(vgaMode.Width) - spriteSize/2
	ymin := int16(-spriteSize / 2)
	ymax := int16(vgaMode.Height) - spriteSize/2

	for i := 0; i < numSprites; i++ {
		s := &sprites[i]
		s.x += s.vx
		s.y += s.vy

		// Bounce off edges
		if s.x < xmin || s.x > xmax {
			s.vx = randomVelocity()
			if s.x < xmin {
				s.x = xmin
			} else {
				s.x = xmax
			}
		}
		if s.y < ymin || s.y > ymax {
			s.vy = randomVelocity()
			if s.y < ymin {
				s.y = ymin
			} else {
				s.y = ymax
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

func renderScanline(buffer *scanvideo.ScanlineBuffer) {
	lineNum := int(scanvideo.ScanlineNumber(buffer.ScanlineID))
	width := int(vgaMode.Width)

	// Fill background
	for i := 0; i < width; i++ {
		scanlineBuf[i] = bgColor
	}

	// Render each sprite
	for i := 0; i < numSprites; i++ {
		spriteRender16(&sprites[i], lineNum, width)
	}

	// Output using RAW_RUN
	ptr := 0

	// RAW_RUN header: command + first pixel
	buffer.Data[ptr] = uint32(scanvideo.COMPOSABLE_RAW_RUN) | (uint32(scanlineBuf[0]) << 16)
	ptr++

	// Word 1: count + second pixel
	buffer.Data[ptr] = uint32(width-2) | (uint32(scanlineBuf[1]) << 16)
	ptr++

	// Remaining pixels in pairs
	for i := 2; i < width; i += 2 {
		buffer.Data[ptr] = uint32(scanlineBuf[i]) | (uint32(scanlineBuf[i+1]) << 16)
		ptr++
	}

	// End of line
	buffer.Data[ptr] = uint32(scanvideo.COMPOSABLE_RAW_1P) | (0 << 16)
	ptr++
	buffer.Data[ptr] = uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16

	buffer.DataUsed = uint16(ptr)
	buffer.Status = scanvideo.ScanlineOK
}

// spriteRender16 renders a sprite onto the scanline buffer with alpha blending
func spriteRender16(sp *sprite, rasterY, rasterW int) {
	// Calculate intersection with scanline
	texOffsY := rasterY - int(sp.y)
	if texOffsY < 0 || texOffsY >= spriteSize {
		return // Sprite doesn't intersect this scanline
	}

	xStart := int(sp.x)
	xEnd := xStart + spriteSize

	// Clip to screen
	texOffsX := 0
	if xStart < 0 {
		texOffsX = -xStart
		xStart = 0
	}
	if xEnd > rasterW {
		xEnd = rasterW
	}

	if xStart >= xEnd {
		return
	}

	// Get sprite row
	srcRow := sp.img[texOffsY*spriteSize:]

	// Blit with alpha
	for x := xStart; x < xEnd; x++ {
		srcPx := srcRow[texOffsX]
		texOffsX++

		if srcPx&alphaMask != 0 {
			// Convert BGAR5515 to RGB565 for display
			// BGAR5515: alpha(15) | blue(14-10) | green(9-5) | red(4-0)
			// RGB565:   red(15-11) | green(10-5) | blue(4-0)
			r := (srcPx & 0x1F) << 11
			g := (srcPx & 0x3E0)      // green stays in middle
			b := (srcPx >> 10) & 0x1F
			scanlineBuf[x] = r | g | b
		}
	}
}
