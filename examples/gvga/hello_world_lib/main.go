// Hello World demo using the gvga library
// This is the TinyGo port of the C GVga hello_world example
// Uses the gvga library which in turn uses the scanvideo library
//
// Build with: tinygo build -target=pico -o hello.uf2 .
package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/gvga"
)

// Animation state (matches C example struct)
type animation struct {
	x, y   int
	dx, dy int
}

// Animation matches C example: start at (20,20), move by (5,5) per frame
var anim = animation{x: 20, y: 20, dx: 5, dy: 5}

func main() {
	// Initialize serial for debug output
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(100 * time.Millisecond)
	println("GVga Hello World starting...")

	// Initialize LED
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	// Initialize gvga: 640x480, 1bpp, double buffered, not interlaced
	g := gvga.Init(640, 480, 1, true, false, nil)
	if g == nil {
		println("gvga init failed!")
		blinkError(led)
		return
	}
	println("gvga initialized")

	// Set palette: WHITE background (pen 0), RED foreground (pen 1)
	// Matches C example colors
	g.SetPalette([]gvga.Color{gvga.White, gvga.Red}, 0, 2)

	// Clear screen with background color
	g.Clear(0)
	println("Screen cleared")

	// Start video output
	g.Start()

	// Allow video goroutine to initialize
	// time.Sleep is a blocking call that properly triggers TinyGo's scheduler
	time.Sleep(500 * time.Millisecond)
	println("Video started")

	// Run serial handling in a separate goroutine
	go func() {
		println("Serial goroutine started")
		for {
			if machine.Serial.Buffered() > 0 {
				b, _ := machine.Serial.ReadByte()
				if b == 'r' || b == 'R' {
					println("Rebooting...")
					time.Sleep(100 * time.Millisecond)
					machine.EnterBootloader()
				}
			}
			time.Sleep(10 * time.Millisecond) // Don't spin too fast
		}
	}()

	// Main animation loop
	for {

		// Clear and draw on the back buffer
		g.Clear(0)

		// Update animation position (matches C example EXACTLY)
		anim.x += anim.dx
		anim.y += anim.dy

		// Bounds check for 640x480
		if anim.x > 640-120 || anim.x < 20 {
			anim.dx = -anim.dx
		}
		if anim.y > 480-70 || anim.y < 20 {
			anim.dy = -anim.dy
		}

		// Draw the pattern
		drawPattern(g)

		// Swap buffers (matches C: sync=false, just swap without waiting)
		g.Swap(false)
	}
}

func drawPattern(g *gvga.GVga) {
	// Border for 640x480
	for i := 1; i < 16; i++ {
		color := uint16(i % 2) // Alternates 1, 0, 1, 0, ...
		g.Box(i-1, i-1, 640-i, 480-i, color)
	}

	// Draw "HELLO" text (matches C example)
	drawText(g, anim.x, anim.y, "HELLO", 1)
	drawText(g, anim.x, anim.y+30, "WORLD!", 1)
}

func drawText(g *gvga.GVga, x, y int, text string, pen uint16) {
	// C example uses: w=10, dx = w + 10 = 20
	for _, c := range text {
		drawChar(g, x, y, byte(c), pen)
		x += 20
	}
}

func drawChar(g *gvga.GVga, x, y int, c byte, pen uint16) {
	// Match C example EXACTLY: w=10, h=20, H=h/2=10, W=w/2=5
	const w = 10
	const h = 20
	const H = h / 2 // 10
	const W = w / 2 // 5

	switch c {
	case 'H':
		g.Line(x, y, x, y+h, pen)
		g.Line(x+w, y, x+w, y+h, pen)
		g.Line(x, y+H, x+w, y+H, pen)
	case 'E':
		g.Line(x, y, x, y+h, pen)
		g.Line(x, y, x+w, y, pen)
		g.Line(x, y+H, x+w, y+H, pen)
		g.Line(x, y+h, x+w, y+h, pen)
	case 'L':
		g.Line(x, y, x, y+h, pen)
		g.Line(x, y+h, x+w, y+h, pen)
	case 'O':
		g.Line(x, y, x, y+h, pen)
		g.Line(x+w, y, x+w, y+h, pen)
		g.Line(x, y, x+w, y, pen)
		g.Line(x, y+h, x+w, y+h, pen)
	case 'W':
		g.Line(x, y, x, y+h, pen)
		g.Line(x+w, y, x+w, y+h, pen)
		g.Line(x, y+h, x+W, y+H, pen)
		g.Line(x+w, y+h, x+W, y+H, pen)
	case 'R':
		g.Line(x, y, x, y+h, pen)
		g.Line(x+w, y, x+w, y+H, pen)
		g.Line(x, y, x+w, y, pen)
		g.Line(x, y+H, x+w, y+H, pen)
		g.Line(x+W, y+H, x+w, y+h, pen)
	case 'D':
		g.Line(x+W/2, y, x+W/2, y+h, pen)
		g.Line(x, y, x+w, y, pen)
		g.Line(x+w, y, x+w, y+h, pen)
		g.Line(x, y+h, x+w, y+h, pen)
	case '!':
		g.Line(x, y, x, y+H+H/2, pen)
		g.Line(x, y+h-2, x, y+h, pen)
	}
}

func blinkError(led machine.Pin) {
	for {
		led.Low()
		time.Sleep(100 * time.Millisecond)
		led.High()
		time.Sleep(100 * time.Millisecond)
	}
}
