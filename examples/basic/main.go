// Basic test example - tests scanvideo Setup() and TimingEnable() step by step
// to isolate where USB serial breaks

package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

var led = machine.LED
var vgaMode = &scanvideo.Mode640x480_60

func main() {
	// Configure LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	// Configure serial
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})

	// Wait longer for USB to enumerate
	time.Sleep(2 * time.Second)

	println("=====================================")
	println("Scanvideo step-by-step test")
	println("=====================================")
	println("Step 1: Serial working")
	time.Sleep(100 * time.Millisecond)

	// Step 2: Call Setup
	println("Step 2: Calling scanvideo.Setup()...")
	time.Sleep(100 * time.Millisecond)

	if !scanvideo.Setup(vgaMode) {
		println("Setup FAILED!")
		for {
			led.High()
			time.Sleep(100 * time.Millisecond)
			led.Low()
			time.Sleep(100 * time.Millisecond)
		}
	}

	println("Step 3: Setup succeeded!")
	time.Sleep(100 * time.Millisecond)

	// Step 4: Enable timing (this enables IRQs)
	println("Step 4: Calling scanvideo.TimingEnable(true)...")
	time.Sleep(100 * time.Millisecond)

	scanvideo.TimingEnable(true)

	println("Step 5: TimingEnable succeeded!")
	println("If you see this, everything works!")
	println("Send 'r' to reboot to bootloader")

	counter := 0
	ledState := false

	for {
		// Check for serial input
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			println("Received:", string(b))
			if b == 'r' {
				println("Rebooting to bootloader...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}

		// Blink LED and print counter every second
		counter++
		if counter >= 10 {
			counter = 0
			ledState = !ledState
			led.Set(ledState)
			if ledState {
				println(">>> PASS: LED ON - video running!")
			} else {
				println(">>> PASS: LED OFF - video running!")
			}
		}

		time.Sleep(100 * time.Millisecond)
	}
}
