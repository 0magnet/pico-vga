// Serial test with scanvideo Setup() AND TimingEnable()
package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

func main() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})

	// Wait for USB enumeration
	time.Sleep(2 * time.Second)

	println("About to call scanvideo.Setup...")

	if !scanvideo.Setup(&scanvideo.Mode320x240_60) {
		println("Setup failed!")
	} else {
		println("Setup succeeded!")
	}

	println("About to call TimingEnable...")
	time.Sleep(100 * time.Millisecond)

	scanvideo.TimingEnable(true)

	println("TimingEnable done!")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	counter := 0
	for {
		counter++
		println("After TimingEnable, Count:", counter)
		led.Set(!led.Get())

		// Check for reboot command
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == 'r' {
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}

		time.Sleep(1 * time.Second)
	}
}
