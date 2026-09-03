// Minimal test for TinyGo multicore support
// Build: tinygo build -target=pico -scheduler=cores -o multicore_test.uf2 .
package main

import (
	"machine"
	"runtime"
	"time"
)

var counter int

func main() {
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	println("Starting multicore test")
	println("NumCPU:", runtime.NumCPU())

	// Start a goroutine that should run on the other core
	go func() {
		println("Goroutine started on core", runtime.GOMAXPROCS(0))
		for {
			counter++
			time.Sleep(100 * time.Millisecond)
		}
	}()

	// Main loop on core0
	lastCount := 0
	for {
		if counter != lastCount {
			println("Counter:", counter)
			lastCount = counter
		}
		led.Set(!led.Get())
		time.Sleep(500 * time.Millisecond)
	}
}
