// VGA 640x480@60Hz with timing measurement
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

// Tunable parameters
// Target: HSYNC = 31.468 kHz, VSYNC = 59.94 Hz
// At 31.468 kHz, one line = 31.778 us
// At 59.94 Hz, one frame = 16.683 ms
const (
	hHighLoops = 704  // Keep 704 stable
	hLowLoops  = 93   // Reduce LOW to speed up slightly (target 31468 Hz)
	vTotal     = 525
	vSyncStart = 490
	vSyncEnd   = 492
)

func main() {
	time.Sleep(3 * time.Second)

	println("=== VGA with Timing Measurement ===")
	println("H loops:", hHighLoops, "/", hLowLoops)
	println("Target: 31.468 kHz HSYNC, 59.94 Hz VSYNC")
	println("Target frame time: 16683 us")
	println("")

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

	println("Starting - will measure timing...")

	frameCount := uint32(0)
	startTime := time.Now()

	for {
		// Generate one frame (525 lines)
		for line := 0; line < vTotal; line++ {
			if line == vSyncStart {
				gpioOutClr.Set(vsyncMask)
			} else if line == vSyncEnd {
				gpioOutSet.Set(vsyncMask)
			}

			// HSYNC HIGH
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

		// Every 60 frames, measure and report timing
		if frameCount%60 == 0 {
			elapsed := time.Since(startTime)
			elapsedUs := elapsed.Microseconds()
			frameTimeUs := elapsedUs / int64(frameCount)
			hsyncHz := int64(1000000) * int64(frameCount) * int64(vTotal) / elapsedUs
			vsyncHz := int64(1000000) * int64(frameCount) * 100 / elapsedUs // x100 for 2 decimal places

			println("Frames:", frameCount,
				"FrameTime:", frameTimeUs, "us",
				"HSYNC:", hsyncHz, "Hz",
				"VSYNC:", vsyncHz/100, ".", vsyncHz%100, "Hz")

			if frameCount%120 == 0 {
				led.High()
			} else {
				led.Low()
			}
		}
	}
}
