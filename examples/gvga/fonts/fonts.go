// TinyGo port of GVga b0_fonts example
// Displays font character set in Commodore 64 style
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

	width := 320
	height := 240
	bits := 1
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

	// Draw the font demo
	y := 0

	// Title
	g.Text(32, y, "**** COMMODORE 64 BASIC V2 ****", 1)
	y += 16

	// Character table (16x16 grid)
	for row := 0; row < 16; row++ {
		for col := 0; col < 16; col++ {
			g.Char(col*16+32, y, uint8(row*16+col), 1)
		}
		y += 12
	}

	// ASCII character ranges
	g.Text(32, y, " !\"#$%&'()*+,-./0123456789:;<=>?", 1)
	y += 8
	g.Text(32, y, "@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_", 1)
	y += 8
	g.Text(32, y, "`abcdefghijklmnopqrstuvwxyz{|}~\x7f", 1)

	// Main loop - just blink LED and cycle palette color
	loopCount := 0
	for {
		loopCount++
		if loopCount%1000 == 0 {
			led.Set(!led.Get())
			// Cycle palette color 1 randomly
			palette[1] = gvga.RGB5(
				uint8(loopCount&0x1f),
				uint8((loopCount>>5)&0x1f),
				uint8((loopCount>>10)&0x1f),
			)
			g.SetPalette(palette, 0, len(palette))
		}
		time.Sleep(1 * time.Millisecond)
	}
}
