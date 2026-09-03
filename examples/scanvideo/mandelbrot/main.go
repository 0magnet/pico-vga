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
	fracBits = 25
	maxIters = 127
)

// Fixed-point type
type fixed int32

func floatToFixed(x float32) fixed {
	return fixed(x * float32(1<<fracBits))
}

func doubleToFixed(x float64) fixed {
	return fixed(x * float64(1<<fracBits))
}

func fixedMult(a, b fixed) fixed {
	r := int64(a) * int64(b)
	return fixed(r >> fracBits)
}

var (
	// Mandelbrot params
	x0, y0    fixed
	dx0dx     fixed
	dy0dy     fixed
	maxMag    fixed
	frameNum  int
	y         uint
	paramsOK  bool

	// Framebuffer
	framebuffer [320 * 240]uint16

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
	time.Sleep(100 * time.Millisecond)
	println("mandelbrot starting...")

	if !scanvideo.Setup(vgaMode) {
		println("Failed to setup video!")
		for {
			time.Sleep(time.Second)
		}
	}

	// Initialize mandelbrot params
	frameUpdateLogic()

	scanvideo.TimingEnable(true)
	println("Video started - Mandelbrot zooming...")

	// Start render loop on core1
	go renderLoop()

	// Main loop - compute mandelbrot scanlines
	go computeLoop()

	// Input loop
	for {
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == 'r' {
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// computeLoop computes mandelbrot scanlines
func computeLoop() {
	for {
		// Check if we need new frame params
		if y >= uint(vgaMode.Height) {
			paramsOK = false
			frameUpdateLogic()
			y = 0
		}

		currentY := y
		y++

		// Compute one scanline
		computeScanline(currentY)
	}
}

func computeScanline(lineY uint) {
	lineBuffer := framebuffer[lineY*320:]
	mx := x0
	my := y0 + fixedMult(dy0dy, fixed(lineY)<<fracBits>>fracBits)

	for x := 0; x < 320; x++ {
		iters := computePixel(mx, my)
		if iters == maxIters+1 {
			lineBuffer[x] = 0
		} else if iters == maxIters {
			lineBuffer[x] = 0
		} else {
			lineBuffer[x] = colors[iters&15]
		}
		mx += dx0dx
	}
}

func computePixel(cr, ci fixed) int {
	zr := cr
	zi := ci
	var xold, yold fixed
	period := 0

	for iters := 0; iters < maxIters; iters++ {
		zr2 := fixedMult(zr, zr)
		zi2 := fixedMult(zi, zi)

		if zr2+zi2 > maxMag {
			return iters
		}

		zrtemp := zr2 - zi2 + cr
		zi = 2*fixedMult(zr, zi) + ci
		zr = zrtemp

		// Period checking for optimization
		if zr == xold && zi == yold {
			return maxIters + 1
		}

		period++
		if period > 20 {
			period = 0
			xold = zr
			yold = zi
		}
	}
	return maxIters
}

func frameUpdateLogic() {
	if !paramsOK {
		scale := float64(vgaMode.Height) / 2.0
		offx := float64(min(frameNum, 196)) / 500.0
		offy := -float64(min(frameNum, 229)) / 250.0
		scale *= (10000.0 + float64(frameNum)*float64(frameNum)) / 10000.0
		frameNum++

		x0 = doubleToFixed(offx + float64(-int(vgaMode.Width)/2)/scale - 0.5)
		y0 = doubleToFixed(offy + float64(-int(vgaMode.Height)/2)/scale)
		dx0dx = doubleToFixed(1.0 / scale)
		dy0dy = doubleToFixed(1.0 / scale)
		maxMag = doubleToFixed(4.0)
		paramsOK = true
	}
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

		fillScanlineBuffer(buffer)
		scanvideo.EndScanlineGeneration(buffer)
	}
}

func fillScanlineBuffer(buffer *scanvideo.ScanlineBuffer) {
	lineNum := scanvideo.ScanlineNumber(buffer.ScanlineID)
	pixels := framebuffer[uint32(lineNum)*320:]

	// Use RAW_RUN to output framebuffer pixels
	idx := 0

	// RAW_RUN: first pixel, count, then pixels
	buffer.Data[idx] = uint32(scanvideo.COMPOSABLE_RAW_RUN) | (uint32(pixels[0]) << 16)
	idx++
	buffer.Data[idx] = uint32(318-2) | (uint32(pixels[1]) << 16) // count = width - 2 - 1
	idx++

	// Pack remaining pixels 2 per word
	for i := 2; i < 320; i += 2 {
		buffer.Data[idx] = uint32(pixels[i]) | (uint32(pixels[i+1]) << 16)
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
