// +build ignore

// Test if gvga.Init causes issues
package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/gvga"
)

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	println("about to call gvga.Init...")

	g := gvga.Init(640, 480, 1, true, false, nil)
	if g == nil {
		println("gvga.Init returned nil!")
	} else {
		println("gvga.Init succeeded! Width=", g.Width, "Height=", g.Height)
	}

	for i := 0; ; i++ {
		println("loop", i)
		led.Set(i%2 == 0)
		time.Sleep(500 * time.Millisecond)
	}
}
