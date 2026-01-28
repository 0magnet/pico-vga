// +build ignore

// Minimal test to verify device works
package main

import (
	"machine"
	"time"
)

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	for i := 0; ; i++ {
		println("hello", i)
		led.Set(i%2 == 0)
		time.Sleep(500 * time.Millisecond)
	}
}
