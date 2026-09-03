// +build ignore

// Test if importing gvga causes issues
package main

import (
	"machine"
	"time"

	_ "github.com/0magnet/pico-vga/gvga" // Just import, don't use
)

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	println("gvga imported successfully")

	for i := 0; ; i++ {
		println("loop", i)
		led.Set(i%2 == 0)
		time.Sleep(500 * time.Millisecond)
	}
}
