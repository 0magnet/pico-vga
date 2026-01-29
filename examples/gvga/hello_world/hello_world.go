// TinyGo port of GVga hello_world example
// Matches C version: apps/a0_hello_world/src/main.c
// Build with: tinygo build -target=pico -scheduler=cores -o hello_world.uf2 .
// Build with debug: tinygo build -target=pico -scheduler=cores -tags=debug -o hello_world.uf2 .
package main

import (
	"machine"
	"runtime"
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
	width, height int
	x, y          int
	dx, dy        int
	color1, color2 uint16
}

var state helloWorldState
var freezeAnimation = false

func main() {
	// Initialize serial (only if debug build)
	initSerial()
	debugPrint("GVga hello_world starting...")

	// Initialize LED
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Use 640x480 mode
	width := 640
	height := 480
	bits := 1
	doubleBuffer := true
	interlaced := false

	g := gvga.Init(uint16(width), uint16(height), bits, doubleBuffer, interlaced, nil)
	if g == nil {
		for {
			led.Low()
			time.Sleep(100 * time.Millisecond)
			led.High()
			time.Sleep(100 * time.Millisecond)
		}
	}

	g.SetPalette(palette, 0, len(palette))
	initHelloWorld(&state, width, height)

	// Pre-fill frame buffers
	g.Clear(0)
	drawHelloWorld(g, &state)
	copy(g.ShowFrame, g.DrawFrame)

	// Start video
	g.Start()
	g.Sync()
	g.Sync()

	// Main loop
	loopCount := 0
	for {
		loopCount++
		if loopCount%100 == 0 {
			led.Set(!led.Get())
		}

		// Draw content with yields
		runtime.Gosched()
		g.Clear(0)
		runtime.Gosched()
		drawHelloWorld(g, &state)
		runtime.Gosched()
		if !freezeAnimation {
			moveHelloWorld(&state)
		}
		g.Swap(false)

		// Check for serial commands (only if debug build)
		checkSerialCommands(g, led)
		printLoopStats(loopCount, g)

		time.Sleep(10 * time.Millisecond)
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

	// Draw border boxes
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
