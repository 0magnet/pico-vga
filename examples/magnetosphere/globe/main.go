// Magnetosphere Globe - Rotating wireframe globe animation
// Uses 16bpp framebuffer like working mandelbrot example

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
	centerX      = screenWidth / 2
	centerY      = screenHeight / 2

	globeStacks = 20
	globeSlices = 30
	globeRadius = 90

	fpBits = 8
	fpOne  = 1 << fpBits
)

// 16bpp framebuffer like mandelbrot
var framebuffer [screenWidth * screenHeight]uint16

// Colors
const (
	colorBlack = 0x0000
	colorWhite = 0xFFFF
)

var sinTable [256]int16
var cosTable [256]int16

type vertex struct {
	x, y, z int16
}

var globeVerts [globeStacks + 1][globeSlices + 1]vertex

var rotX, rotY, rotZ int32
var speedX, speedY, speedZ int16
var initRotX, initRotY, initRotZ uint8

type projected struct {
	x, y int16
}

var proj [globeStacks + 1][globeSlices + 1]projected

var randState uint32

func randInit() {
	randState = uint32(time.Now().UnixNano())
	if randState == 0 {
		randState = 12345
	}
}

func randNext() uint32 {
	randState = randState*1664525 + 1013904223
	return randState
}

func randByte() uint8 {
	return uint8(randNext() >> 24)
}

func randSpeed() int16 {
	v := int16(randNext()%51) - 25
	if v == 0 {
		v = 1
	}
	return v
}

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(100 * time.Millisecond)
	println("Globe starting...")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	for i := 0; i < 3; i++ {
		led.High()
		time.Sleep(100 * time.Millisecond)
		led.Low()
		time.Sleep(100 * time.Millisecond)
	}

	randInit()
	initRotX = randByte()
	initRotY = randByte()
	initRotZ = randByte()
	speedX = randSpeed()
	speedY = randSpeed()
	speedZ = randSpeed()

	initTrigTables()
	generateSphereVertices()

	// Draw first frame before starting video
	clearFramebuffer()
	drawGlobe()
	println("First frame ready")

	if !scanvideo.Setup(vgaMode) {
		println("Video setup failed!")
		for {
			led.High()
			time.Sleep(50 * time.Millisecond)
			led.Low()
			time.Sleep(50 * time.Millisecond)
		}
	}

	go renderLoop()
	time.Sleep(50 * time.Millisecond)

	scanvideo.TimingEnable(true)
	println("Video started")

	frameCount := 0
	for {
		scanvideo.WaitForVblank()

		rotX += int32(speedX)
		rotY += int32(speedY)
		rotZ += int32(speedZ)

		clearFramebuffer()
		drawGlobe()

		frameCount++
		if frameCount%60 == 0 {
			led.Set(!led.Get())
		}

		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == 'r' {
				machine.EnterBootloader()
			}
		}
	}
}

func clearFramebuffer() {
	for i := range framebuffer {
		framebuffer[i] = colorBlack
	}
}

func setPixel(x, y int) {
	if x < 0 || x >= screenWidth || y < 0 || y >= screenHeight {
		return
	}
	framebuffer[y*screenWidth+x] = colorWhite
}

func drawLine(x0, y0, x1, y1 int) {
	dx := x1 - x0
	if dx < 0 {
		dx = -dx
	}
	dy := y1 - y0
	if dy < 0 {
		dy = -dy
	}
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
	if y0 >= y1 {
		sy = -1
	}
	err := dx - dy

	for {
		setPixel(x0, y0)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

func initTrigTables() {
	for i := 0; i < 256; i++ {
		angle := float32(i) * 6.283185 / 256.0
		sinTable[i] = int16(sin32(angle) * float32(fpOne))
		cosTable[i] = int16(cos32(angle) * float32(fpOne))
	}
}

func sin32(x float32) float32 {
	for x < 0 {
		x += 6.283185
	}
	for x >= 6.283185 {
		x -= 6.283185
	}
	if x > 3.141592 {
		x -= 6.283185
	}
	x2 := x * x
	x3 := x2 * x
	x5 := x3 * x2
	x7 := x5 * x2
	return x - x3/6.0 + x5/120.0 - x7/5040.0
}

func cos32(x float32) float32 {
	return sin32(x + 1.5707963)
}

func generateSphereVertices() {
	sinIX := sinTable[initRotX]
	cosIX := cosTable[initRotX]
	sinIY := sinTable[initRotY]
	cosIY := cosTable[initRotY]
	sinIZ := sinTable[initRotZ]
	cosIZ := cosTable[initRotZ]

	for i := 0; i <= globeStacks; i++ {
		phi := uint8(i * 128 / globeStacks)
		sinPhi := sinTable[phi]
		cosPhi := cosTable[phi]

		for j := 0; j <= globeSlices; j++ {
			theta := uint8(j * 256 / globeSlices)
			sinTheta := sinTable[theta]
			cosTheta := cosTable[theta]

			x0 := int32((int32(globeRadius) * int32(sinPhi) * int32(cosTheta)) >> (fpBits * 2))
			y0 := int32((int32(globeRadius) * int32(sinPhi) * int32(sinTheta)) >> (fpBits * 2))
			z0 := int32((int32(globeRadius) * int32(cosPhi)) >> fpBits)

			y1 := (y0*int32(cosIX) - z0*int32(sinIX)) >> fpBits
			z1 := (y0*int32(sinIX) + z0*int32(cosIX)) >> fpBits
			x1 := x0

			x2 := (x1*int32(cosIY) + z1*int32(sinIY)) >> fpBits
			z2 := (-x1*int32(sinIY) + z1*int32(cosIY)) >> fpBits
			y2 := y1

			x3 := (x2*int32(cosIZ) - y2*int32(sinIZ)) >> fpBits
			y3 := (x2*int32(sinIZ) + y2*int32(cosIZ)) >> fpBits
			z3 := z2

			globeVerts[i][j] = vertex{int16(x3), int16(y3), int16(z3)}
		}
	}
}

func drawGlobe() {
	rx := uint8(rotX >> 4)
	ry := uint8(rotY >> 4)
	rz := uint8(rotZ >> 4)

	sinRX := sinTable[rx]
	cosRX := cosTable[rx]
	sinRY := sinTable[ry]
	cosRY := cosTable[ry]
	sinRZ := sinTable[rz]
	cosRZ := cosTable[rz]

	for i := 0; i <= globeStacks; i++ {
		for j := 0; j <= globeSlices; j++ {
			v := globeVerts[i][j]

			y1 := (int32(v.y)*int32(cosRX) - int32(v.z)*int32(sinRX)) >> fpBits
			z1 := (int32(v.y)*int32(sinRX) + int32(v.z)*int32(cosRX)) >> fpBits
			x1 := int32(v.x)

			x2 := (x1*int32(cosRY) + z1*int32(sinRY)) >> fpBits
			y2 := y1

			x3 := (x2*int32(cosRZ) - y2*int32(sinRZ)) >> fpBits
			y3 := (x2*int32(sinRZ) + y2*int32(cosRZ)) >> fpBits

			proj[i][j].x = int16(centerX + int(x3))
			proj[i][j].y = int16(centerY - int(y3))
		}
	}

	for i := 0; i <= globeStacks; i++ {
		for j := 0; j < globeSlices; j++ {
			p1 := proj[i][j]
			p2 := proj[i][j+1]
			drawLine(int(p1.x), int(p1.y), int(p2.x), int(p2.y))
		}
	}

	for j := 0; j <= globeSlices; j++ {
		for i := 0; i < globeStacks; i++ {
			p1 := proj[i][j]
			p2 := proj[i+1][j]
			drawLine(int(p1.x), int(p1.y), int(p2.x), int(p2.y))
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

// Render exactly like mandelbrot example
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
