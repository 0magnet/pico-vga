// TinyGo port of pico-playground/scanvideo/mandelbrot
// Real-time Mandelbrot set fractal zoomer
// Uses fixed-point arithmetic for fast iteration

package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

var vgaMode = &scanvideo.Mode320x240_60

const (
	screenWidth  = 320
	screenHeight = 240
	fracBits     = 25
	maxIters     = 127
)

// Fixed-point type
type fixed int32

func doubleToFixed(x float64) fixed {
	return fixed(x * float64(1<<fracBits))
}

func fixedMult(a, b fixed) fixed {
	r := int64(a) * int64(b)
	return fixed(r >> fracBits)
}

var (
	// Mandelbrot params
	x0, y0   fixed
	dx0dx    fixed
	dy0dy    fixed
	maxMag   fixed
	frameNum int

	// Framebuffer - 16bpp color
	framebuffer [screenWidth * screenHeight]uint16

	// Color palette
	colors = [16]uint16{
		pxRGB8(66, 30, 15),
		pxRGB8(25, 7, 26),
		pxRGB8(9, 1, 47),
		pxRGB8(4, 4, 73),
		pxRGB8(0, 7, 100),
		pxRGB8(12, 44, 138),
		pxRGB8(24, 82, 177),
		pxRGB8(57, 125, 209),
		pxRGB8(134, 181, 229),
		pxRGB8(211, 236, 248),
		pxRGB8(241, 233, 191),
		pxRGB8(248, 201, 95),
		pxRGB8(255, 170, 0),
		pxRGB8(204, 128, 0),
		pxRGB8(153, 87, 0),
		pxRGB8(106, 52, 3),
	}
)

func pxRGB8(r, g, b uint8) uint16 {
	return scanvideo.RGB565(r, g, b)
}

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(500 * time.Millisecond)
	println("Mandelbrot starting...")

	// Compute first frame before starting video
	frameUpdateLogic()
	for y := 0; y < screenHeight; y++ {
		computeScanline(y)
	}
	println("First frame computed")

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

	// Main loop - compute frames with timing
	for {
		start := time.Now()

		frameUpdateLogic()
		for y := 0; y < screenHeight; y++ {
			computeScanline(y)
		}

		elapsed := time.Since(start)
		println("Frame", frameNum-1, ":", elapsed.Milliseconds(), "ms")

		led.Set(!led.Get())

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

// fastMult does 32-bit fixed-point multiply using 16-bit splits
// Avoids slow 64-bit operations on Cortex-M0
func fastMult(a, b int32) int32 {
	// Split into high and low 16-bit parts
	aH := a >> 16
	aL := a & 0xFFFF
	bH := b >> 16
	bL := b & 0xFFFF

	// Full product = aH*bH*2^32 + (aH*bL + aL*bH)*2^16 + aL*bL
	// We need >> 25, which is >> 32 then << 7 for high part
	high := aH * bH
	mid := aH*bL + aL*bH
	low := aL * bL

	// Combine: high<<7 + mid>>9 + low>>25
	return (high << 7) + (mid >> 9) + (low >> 25)
}

func computeScanline(lineY int) {
	lineBuffer := framebuffer[lineY*screenWidth:]
	mx := int32(x0)
	my := int32(y0) + int32(dy0dy)*int32(lineY)
	mag := int32(maxMag)
	dx := int32(dx0dx)

	for x := 0; x < screenWidth; x++ {
		cr, ci := mx, my
		zr, zi := cr, ci
		iters := 0

		for ; iters < maxIters; iters++ {
			zr2 := fastMult(zr, zr)
			zi2 := fastMult(zi, zi)

			if zr2+zi2 > mag {
				break
			}

			zrzi := fastMult(zr, zi)
			zr = zr2 - zi2 + cr
			zi = zrzi<<1 + ci
		}

		if iters >= maxIters {
			lineBuffer[x] = 0
		} else {
			lineBuffer[x] = colors[iters&15]
		}
		mx += dx
	}
}

func frameUpdateLogic() {
	scale := float64(screenHeight) / 2.0
	offx := float64(min(frameNum, 196)) / 500.0
	offy := -float64(min(frameNum, 229)) / 250.0
	scale *= (10000.0 + float64(frameNum)*float64(frameNum)) / 10000.0
	frameNum++

	x0 = doubleToFixed(offx + float64(-screenWidth/2)/scale - 0.5)
	y0 = doubleToFixed(offy + float64(-screenHeight/2)/scale)
	dx0dx = doubleToFixed(1.0 / scale)
	dy0dy = doubleToFixed(1.0 / scale)
	maxMag = doubleToFixed(4.0)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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

// renderScanline - display framebuffer using RAW_RUN
func renderScanline(buffer *scanvideo.ScanlineBuffer) {
	lineNum := scanvideo.ScanlineNumber(buffer.ScanlineID)
	width := int(vgaMode.Width)
	pixels := framebuffer[int(lineNum)*screenWidth:]

	ptr := 0

	// RAW_RUN header: command + first pixel
	buffer.Data[ptr] = uint32(scanvideo.COMPOSABLE_RAW_RUN) | (uint32(pixels[0]) << 16)
	ptr++

	// Word 1: count + second pixel
	buffer.Data[ptr] = uint32(width-2) | (uint32(pixels[1]) << 16)
	ptr++

	// Remaining pixels in pairs
	for i := 2; i < width; i += 2 {
		buffer.Data[ptr] = uint32(pixels[i]) | (uint32(pixels[i+1]) << 16)
		ptr++
	}

	// End of line
	buffer.Data[ptr] = uint32(scanvideo.COMPOSABLE_RAW_1P) | (0 << 16)
	ptr++
	buffer.Data[ptr] = uint32(scanvideo.COMPOSABLE_EOL_ALIGN) << 16

	buffer.DataUsed = uint16(ptr)
	buffer.Status = scanvideo.ScanlineOK
}
