// VGA test - tuning timing to get monitor to accept
package main

import (
	"machine"
	"runtime/volatile"
	"time"
	"unsafe"
)

const (
	pinHSYNC   = machine.GPIO16
	pinVSYNC   = machine.GPIO17
	pinRGBBase = machine.GPIO0
)

const (
	SIO_BASE     = 0xd0000000
	GPIO_OUT_SET = SIO_BASE + 0x014
	GPIO_OUT_CLR = SIO_BASE + 0x018
)

var (
	gpioOutSet = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OUT_SET)))
	gpioOutClr = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OUT_CLR)))
)

var dummy volatile.Register32

// Tunable timing parameters
// VGA 640x480: ratio should be 704:96 (7.33:1)
var (
	highLoops = 620  // Loops during HSYNC HIGH
	lowLoops  = 85   // Loops during HSYNC LOW (sync pulse)
)

func main() {
	time.Sleep(3 * time.Second)

	println("=== VGA Timing Tuner v3 ===")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: machine.PinOutput})
		pin.Low()
	}

	pinHSYNC.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pinVSYNC.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pinHSYNC.High()
	pinVSYNC.High()

	println("Slow toggle test...")
	for i := 0; i < 4; i++ {
		pinHSYNC.Low()
		pinVSYNC.Low()
		led.Low()
		time.Sleep(200 * time.Millisecond)
		pinHSYNC.High()
		pinVSYNC.High()
		led.High()
		time.Sleep(200 * time.Millisecond)
	}

	hsyncMask := uint32(1 << 16)
	vsyncMask := uint32(1 << 17)

	println("HIGH loops:", highLoops, "LOW loops:", lowLoops)
	println("Starting VGA signal...")

	frameCount := uint32(0)

	for {
		// Generate one frame (525 lines)
		for line := 0; line < 525; line++ {
			// VSYNC: LOW during lines 490-491
			if line == 490 {
				gpioOutClr.Set(vsyncMask)
			} else if line == 492 {
				gpioOutSet.Set(vsyncMask)
			}

			// HSYNC HIGH period (active + front porch + back porch)
			for i := 0; i < highLoops; i++ {
				dummy.Set(uint32(i))
			}

			// HSYNC LOW (sync pulse)
			gpioOutClr.Set(hsyncMask)
			for i := 0; i < lowLoops; i++ {
				dummy.Set(uint32(i))
			}
			gpioOutSet.Set(hsyncMask)
		}

		frameCount++

		if frameCount%60 == 0 {
			led.Low()
		}
		if frameCount%120 == 0 {
			led.High()
			println("Frame:", frameCount, "H:", highLoops, "L:", lowLoops)
		}
	}
}
