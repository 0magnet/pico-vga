// TinyGo port of GVga b1_text_mode example
// Text mode display with C64 theme
package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/gvga"
)

// C64-style palette
var palette = []gvga.Color{
	gvga.Blue,
	gvga.RGB5(16, 28, 28), // C64 cyan
	gvga.Green,
	gvga.Blue,
	gvga.Yellow,
	gvga.Cyan,
	gvga.Magenta,
	gvga.Black,
}

func center(g *gvga.GVga, row int, text string, color uint16) {
	col := (int(g.Cols) - len(text)) / 2
	g.TextAt(row, col, text, color)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func itoa2(n int) string {
	s := itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
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

	width := 640
	height := 240
	bits := -1 // Negative = text mode
	doubleBuffer := false
	interlaced := false

	g := gvga.Init(uint16(width), uint16(height), bits, doubleBuffer, interlaced, nil)
	if g == nil {
		for {
			led.Set(!led.Get())
			time.Sleep(100 * time.Millisecond)
		}
	}

	g.SetPalette(palette, 0, len(palette))
	g.Start()

	// Title
	center(g, 1, "**** COMMODORE 64 BASIC V2 ****", 1)

	// Row numbers
	for row := 0; row < int(g.Rows); row++ {
		g.TextAt(row, 0, itoa2(row), 1)
	}

	// Column numbers
	for col := 0; col < int(g.Cols); col += 10 {
		g.TextAt(0, col, itoa2(col), 1)
	}
	g.TextAt(0, int(g.Cols)-2, itoa2(int(g.Cols)), 1)

	// Character table (16x16 grid)
	for row := 0; row < 16; row++ {
		for col := 0; col < 16; col++ {
			g.CharAt(row+2, col+3, uint8(row*16+col), 1)
			// Also show on right side
			g.CharAt(row+2, int(g.Cols)-18+col, uint8(row*16+col), 1)
		}
	}

	// ASCII character ranges
	row := 19
	col := (int(g.Cols) - 16) / 2
	g.TextAt(row, col, " !\"#$%&'()*+,-./", 1)
	row++
	g.TextAt(row, col, "0123456789:;<=>?", 1)
	row++
	g.TextAt(row, col, "@ABCDEFGHIJKLMNO", 1)
	row++
	g.TextAt(row, col, "PQRSTUVWXYZ[\\]^_", 1)
	row++
	g.TextAt(row, col, "`abcdefghijklmno", 1)
	row++
	g.TextAt(row, col, "pqrstuvwxyz{|}~\x7f", 1)

	// Main loop
	for {
		led.Set(!led.Get())
		time.Sleep(100 * time.Millisecond)
	}
}
