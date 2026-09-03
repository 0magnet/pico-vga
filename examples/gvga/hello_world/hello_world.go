// TinyGo port of GVga hello_world example
// Matches C version: apps/a0_hello_world/src/main.c
// Build with: tinygo build -target=pico -scheduler=tasks -o hello_world.uf2 .
package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/gvga"
)

// Palette matching C version
var palette = []gvga.Color{
	gvga.White, gvga.Red, gvga.Green, gvga.Blue,
	gvga.Yellow, gvga.Cyan, gvga.Magenta, gvga.Black,
	gvga.RGB5(15, 15, 15), gvga.RGB5(15, 0, 0), gvga.RGB5(0, 15, 0), gvga.RGB5(0, 0, 15),
	gvga.RGB5(15, 15, 0), gvga.RGB5(0, 15, 15), gvga.RGB5(15, 15, 0), gvga.RGB5(7, 7, 7),
}

type helloWorldState struct {
	width, height  int
	x, y           int
	dx, dy         int
	color1, color2 uint16
}

var state helloWorldState

func main() {
	// Initialize LED
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Startup blink pattern: 3 fast blinks to show program started
	for i := 0; i < 3; i++ {
		led.High()
		time.Sleep(100 * time.Millisecond)
		led.Low()
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	// Use 640x480 mode (full resolution with multicore)
	width := 640
	height := 480
	bits := 1
	doubleBuffer := true
	interlaced := false

	g := gvga.Init(uint16(width), uint16(height), bits, doubleBuffer, interlaced, nil)
	if g == nil {
		// Error: rapid blink pattern
		for {
			led.Low()
			time.Sleep(100 * time.Millisecond)
			led.High()
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Init succeeded: 2 slow blinks
	for i := 0; i < 2; i++ {
		led.High()
		time.Sleep(300 * time.Millisecond)
		led.Low()
		time.Sleep(300 * time.Millisecond)
	}

	g.SetPalette(palette, 0, len(palette))
	initHelloWorld(&state, width, height)

	// Pre-fill frame buffers
	g.Clear(0)
	drawHelloWorld(g, &state)
	copy(g.ShowFrame, g.DrawFrame)

	// Start video
	g.Start()

	// Start succeeded: 1 long blink
	led.High()
	time.Sleep(1000 * time.Millisecond)
	led.Low()
	time.Sleep(500 * time.Millisecond)

	// Main loop (matches C version structure)
	loopCount := 0
	for {
		loopCount++
		if loopCount%100 == 0 {
			led.Set(!led.Get())
		}

		g.Clear(0)
		moveHelloWorld(&state)
		drawHelloWorld(g, &state)
		g.Swap(false)
	}
}

func initHelloWorld(s *helloWorldState, width, height int) {
	s.width = width
	s.height = height
	s.x = 20
	s.y = 20
	s.dx = 5
	s.dy = 5
	s.color1 = 1 // RED
	s.color2 = 2 // GREEN
}

func moveHelloWorld(s *helloWorldState) {
	s.x += s.dx
	s.y += s.dy
	if s.x > s.width-120 || s.x < 20 {
		s.dx = -s.dx
	}
	if s.y > s.height-70 || s.y < 20 {
		s.dy = -s.dy
	}
}

func drawHelloWorld(g *gvga.GVga, s *helloWorldState) {
	width := s.width
	height := s.height
	X := s.x
	Y := s.y
	color1 := s.color1
	color2 := s.color2

	// Draw border boxes (16 boxes like C version)
	for i := 1; i < 16; i++ {
		g.Box(i-1, i-1, width-i, height-i, uint16(i)%g.Colors)
	}

	x := X
	y := Y
	h := 20
	w := 10
	H := h / 2
	W := w / 2
	dx := w + 10

	// H
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+w, y+0, x+w, y+h, color1)
	g.Line(x+0, y+H, x+w, y+H, color1)
	x += dx
	// E
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+0, y+0, x+w, y+0, color1)
	g.Line(x+0, y+H, x+w, y+H, color1)
	g.Line(x+0, y+h, x+w, y+h, color1)
	x += dx
	// L
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+0, y+h, x+w, y+h, color1)
	x += dx
	// L
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+0, y+h, x+w, y+h, color1)
	x += dx
	// O
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+w, y+0, x+w, y+h, color1)
	g.Line(x+0, y+0, x+w, y+0, color1)
	g.Line(x+0, y+h, x+w, y+h, color1)

	x = X
	y += h + 10
	// W
	g.Line(x+0, y+0, x+0, y+h, color2)
	g.Line(x+w, y+0, x+w, y+h, color2)
	g.Line(x+0, y+h, x+W, y+H, color2)
	g.Line(x+w, y+h, x+W, y+H, color2)
	x += dx
	// O
	g.Line(x+0, y+0, x+0, y+h, color2)
	g.Line(x+w, y+0, x+w, y+h, color2)
	g.Line(x+0, y+0, x+w, y+0, color2)
	g.Line(x+0, y+h, x+w, y+h, color2)
	x += dx
	// R
	g.Line(x+0, y+0, x+0, y+h, color2)
	g.Line(x+w, y+0, x+w, y+H, color2)
	g.Line(x+0, y+0, x+w, y+0, color2)
	g.Line(x+0, y+H, x+w, y+H, color2)
	g.Line(x+W, y+H, x+w, y+h, color2)
	x += dx
	// L
	g.Line(x+0, y+0, x+0, y+h, color2)
	g.Line(x+0, y+h, x+w, y+h, color2)
	x += dx
	// D
	g.Line(x+W/2, y+0, x+W/2, y+h, color2)
	g.Line(x+0, y+0, x+w, y+0, color2)
	g.Line(x+w, y+0, x+w, y+h, color2)
	g.Line(x+0, y+h, x+w, y+h, color2)
	x += dx
	// !
	g.Line(x+0, y+0, x+0, y+H+H/2, color2)
	g.Line(x+0, y+h-2, x+0, y+h, color2)
}
