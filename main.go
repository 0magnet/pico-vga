// VGA 640x480@60Hz - exact timing from pico-extras vga_modes.c
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

// VGA 640x480@60Hz timing from pico-extras:
// Pixel clock: 25MHz, System clock: 125MHz (5x ratio)
// H: active=640, front_porch=16, sync=96, back_porch=48, total=800
// V: active=480, front_porch=10, sync=2, back_porch=33, total=525
// Polarity: negative (sync pulse is LOW)
//
// At 125MHz:
// H_total = 800 * 5 = 4000 cycles
// H_HIGH = 704 * 5 = 3520 cycles (active + front + back porch)
// H_LOW = 96 * 5 = 480 cycles (sync pulse)
//
// Loop calibration: each iteration with volatile write ≈ 5-6 cycles
// HIGH loops: 3520 / 5 = 704
// LOW loops: 480 / 5 = 96

const (
	hHighLoops = 670  // Was 704, trying ~5% faster
	hLowLoops  = 91   // Was 96, keeping ratio
	vTotal     = 525
	vSyncStart = 490 // 480 active + 10 front porch
	vSyncEnd   = 492 // vSyncStart + 2 sync lines
)

func main() {
	time.Sleep(3 * time.Second)

	println("=== VGA 640x480@60Hz - Exact pico-extras timing ===")
	println("H: 670 HIGH loops, 91 LOW loops")
	println("V: 525 total, sync at lines 490-491")

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

	println("Starting VGA signal generation...")

	frameCount := uint32(0)

	for {
		// Generate one frame (525 lines)
		for line := 0; line < vTotal; line++ {
			// VSYNC control: LOW during sync lines
			if line == vSyncStart {
				gpioOutClr.Set(vsyncMask)
			} else if line == vSyncEnd {
				gpioOutSet.Set(vsyncMask)
			}

			// HSYNC HIGH period (active + front porch + back porch)
			for i := 0; i < hHighLoops; i++ {
				dummy.Set(uint32(i))
			}

			// HSYNC LOW (sync pulse)
			gpioOutClr.Set(hsyncMask)
			for i := 0; i < hLowLoops; i++ {
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
			println("Frame:", frameCount)
		}
	}
}
