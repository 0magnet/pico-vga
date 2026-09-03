// TinyGo port of GVga a1_screensaver example
// Classic line screensaver with trailing lines
package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/gvga"
)

const (
	screenWidth  = 320
	screenHeight = 480
	screenBits   = 2
	screenColors = 1 << screenBits
	lineCount    = 256
)

var palette = []gvga.Color{
	gvga.White,
	gvga.Red,
	gvga.Green,
	gvga.Blue,
	gvga.Yellow,
	gvga.Cyan,
	gvga.Magenta,
	gvga.Black,
}

type Line struct {
	x1, y1, x2, y2    int
	dx1, dy1, dx2, dy2 int
	color             uint16
}

var lines [lineCount]Line

// Simple pseudo-random number generator
var rngState uint32 = 12345

func rnd() uint32 {
	rngState = rngState*1103515245 + 12345
	return rngState
}

func rndRange(min, max int) int {
	if max <= min {
		return min
	}
	return min + int(rnd()%uint32(max-min+1))
}

func rndNonZero(min, max int) int {
	for {
		result := rndRange(min, max)
		if result != 0 {
			return result
		}
	}
}

func initLine(line *Line) {
	line.x1 = rndNonZero(0, screenWidth-1)
	line.y1 = rndNonZero(0, screenHeight-1)
	line.x2 = rndNonZero(0, screenWidth-1)
	line.y2 = rndNonZero(0, screenHeight-1)
	line.dx1 = rndNonZero(-10, 10)
	line.dy1 = rndNonZero(-10, 10)
	line.dx2 = rndNonZero(-10, 10)
	line.dy2 = rndNonZero(-10, 10)
	line.color = uint16(rndNonZero(1, screenColors-1))
}

func moveLine(line *Line) {
	line.x1 += line.dx1
	line.y1 += line.dy1
	line.x2 += line.dx2
	line.y2 += line.dy2

	if line.x1 < 0 || line.x1 >= screenWidth {
		if line.x1 < 0 {
			line.x1 = 0
		} else {
			line.x1 = screenWidth - 1
		}
		line.dx1 = -line.dx1
	}
	if line.y1 < 0 || line.y1 >= screenHeight {
		if line.y1 < 0 {
			line.y1 = 0
		} else {
			line.y1 = screenHeight - 1
		}
		line.dy1 = -line.dy1
	}
	if line.x2 < 0 || line.x2 >= screenWidth {
		if line.x2 < 0 {
			line.x2 = 0
		} else {
			line.x2 = screenWidth - 1
		}
		line.dx2 = -line.dx2
	}
	if line.y2 < 0 || line.y2 >= screenHeight {
		if line.y2 < 0 {
			line.y2 = 0
		} else {
			line.y2 = screenHeight - 1
		}
		line.dy2 = -line.dy2
	}
}

func drawLines(g *gvga.GVga) {
	for i := 0; i < lineCount; i++ {
		g.Line(lines[i].x1, lines[i].y1, lines[i].x2, lines[i].y2, lines[i].color)
	}
}

func moveLines() {
	// Shift all lines down (creates trail effect)
	for i := lineCount - 1; i > 0; i-- {
		lines[i] = lines[i-1]
		lines[i].color = (lines[i].color + 1) % screenColors
		if lines[i].color == 0 {
			lines[i].color = 1
		}
	}
	moveLine(&lines[0])
}

// Screensaver state
var (
	dx uint = 0
	dy uint = 1
	x0 uint = 0
	y0 uint = 0
	x1 uint = screenWidth - 1
	y1 uint = screenHeight - 1
)

func screensaver(g *gvga.GVga) {
	g.Clear(0)

	// Draw border rectangles
	for i := 0; i < 10; i += 2 {
		g.Box(i, i, screenWidth-i-1, screenHeight-i-1, 1)
	}

	// Draw rotating line
	g.Line(int(x0), int(y0), int(x1), int(y1), 1)

	x0 += dx
	y0 += dy
	x1 -= dx
	y1 -= dy

	if y0 == screenHeight {
		dx = 1
		dy = 0
		y0 = screenHeight - 1
		y1 = 0
	}
	if x0 == screenWidth {
		dx = 0
		dy = ^uint(0) // -1 in unsigned
		x0 = screenWidth - 1
		x1 = 0
	}
	if y1 == screenHeight {
		dx = ^uint(0) // -1
		dy = 0
		y0 = 0
		y1 = screenHeight - 1
	}
	if x1 == screenWidth {
		dx = 0
		dy = 1
		x0 = 0
		x1 = screenWidth - 1
	}

	moveLines()
	drawLines(g)
}

func main() {
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Startup blink
	for i := 0; i < 3; i++ {
		led.High()
		time.Sleep(100 * time.Millisecond)
		led.Low()
		time.Sleep(100 * time.Millisecond)
	}

	g := gvga.Init(screenWidth, screenHeight, screenBits, true, false, nil)
	if g == nil {
		for {
			led.Set(!led.Get())
			time.Sleep(100 * time.Millisecond)
		}
	}

	g.SetPalette(palette, 0, len(palette))
	g.Start()

	// Initialize first line
	initLine(&lines[0])
	moveLine(&lines[0])

	// Main loop
	loopCount := 0
	for {
		loopCount++
		if loopCount%10 == 0 {
			led.Set(!led.Get())
		}

		screensaver(g)
		g.Swap(false)
	}
}
