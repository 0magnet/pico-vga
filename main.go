// VGA 640x480@60Hz using PIO for precise timing
package main

import (
	"machine"
	"runtime/volatile"
	"time"
	"unsafe"

	pio "github.com/tinygo-org/pio/rp2-pio"
)

const (
	pinHSYNC   = machine.GPIO16
	pinVSYNC   = machine.GPIO17
	pinRGBBase = machine.GPIO0
)

// GPIO registers for fast RGB output
const (
	SIO_BASE     = 0xd0000000
	GPIO_OUT     = SIO_BASE + 0x010
	GPIO_OUT_SET = SIO_BASE + 0x014
	GPIO_OUT_CLR = SIO_BASE + 0x018
	GPIO_OE      = SIO_BASE + 0x020 // Output enable

	// IO Bank0 for function select
	IO_BANK0_BASE = 0x40014000
)

var (
	gpioOut    = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OUT)))
	gpioOutSet = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OUT_SET)))
	gpioOutClr = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OUT_CLR)))
	gpioOE     = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OE)))
)

// Get GPIO CTRL register (function select) - each GPIO has 8-byte spacing
func gpioCtrl(pin int) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Pointer(uintptr(IO_BANK0_BASE + 4 + pin*8)))
}

const rgbMask = uint32(0xFFFF) // GPIO0-15

// VGA timing for 640x480@60Hz at 25.175 MHz pixel clock
const (
	vTotal     = 525
	vSyncStart = 490 // 480 + 10 front porch
	vSyncEnd   = 492 // 490 + 2 sync pulse
)

func main() {
	time.Sleep(2 * time.Second)
	println("=== VGA PIO Test v3 ===")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	// Configure sync pins initially
	pinHSYNC.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pinVSYNC.Configure(machine.PinConfig{Mode: machine.PinOutput})
	pinHSYNC.High()
	pinVSYNC.High()

	// Configure RGB pins
	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: machine.PinOutput})
		pin.Low()
	}

	println("Pins configured")

	// Test basic toggle - including RGB
	println("Testing basic toggle with RGB...")
	for i := 0; i < 4; i++ {
		pinHSYNC.Low()
		pinVSYNC.Low()
		led.Low()
		// Set all RGB low
		for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
			pin.Low()
		}
		time.Sleep(200 * time.Millisecond)
		pinHSYNC.High()
		pinVSYNC.High()
		led.High()
		// Set all RGB high
		for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
			pin.High()
		}
		time.Sleep(200 * time.Millisecond)
	}
	println("Toggle test complete, RGB should have flashed")

	// Debug: show GPIO state
	println("GPIO_OUT:", gpioOut.Get())
	println("GPIO_OE:", gpioOE.Get())
	// Check function select for first few pins (should be 5 for SIO)
	println("GPIO0 CTRL:", gpioCtrl(0).Get()&0x1F)
	println("GPIO1 CTRL:", gpioCtrl(1).Get()&0x1F)
	println("GPIO2 CTRL:", gpioCtrl(2).Get()&0x1F)
	println("GPIO15 CTRL:", gpioCtrl(15).Get()&0x1F)

	// Build PIO program using AssemblerV0
	println("Building PIO program...")
	asm := pio.AssemblerV0{}

	// HSYNC program: 800 cycles per line at 25 MHz = 32 µs
	// High for 704 cycles (active 640 + front 16 + back 48)
	// Low for 96 cycles (sync pulse)
	// Sets IRQ 0 at end of each line for synchronization
	//
	// Program structure:
	// 0: pull block         - get high period count (one time, before wrap)
	// 1: mov x, osr         - copy count to x (wrap target)
	// 2: jmp x-- 2          - loop for high period
	// 3: set pins, 0 [31]   - low for 32 cycles
	// 4: set pins, 0 [31]   - low for 64 cycles
	// 5: set pins, 0 [30]   - low for 31 cycles (95 total)
	// 6: irq 0              - signal line complete
	// 7: set pins, 1        - back high, wrap to 1

	hsyncProgram := []uint16{
		asm.Pull(false, true).Encode(),                    // 0: pull block
		asm.Mov(pio.MovDestX, pio.MovSrcOSR).Encode(),    // 1: mov x, osr
		asm.Jmp(pio.JmpXNZeroDec, 2).Encode(),            // 2: jmp x-- 2
		asm.Set(pio.SetDestPins, 0).Delay(31).Encode(),   // 3: set pins, 0 [31]
		asm.Set(pio.SetDestPins, 0).Delay(31).Encode(),   // 4: set pins, 0 [31]
		asm.Set(pio.SetDestPins, 0).Delay(30).Encode(),   // 5: set pins, 0 [30]
		asm.IRQSet(false, 0).Encode(),                    // 6: irq 0
		asm.Set(pio.SetDestPins, 1).Encode(),             // 7: set pins, 1
		asm.Jmp(pio.JmpAlways, 1).Encode(),               // 8: jmp 1 (wrap back)
	}

	println("Program built, length:", len(hsyncProgram))
	for i, instr := range hsyncProgram {
		println("  ", i, ":", instr)
	}

	// Initialize PIO
	Pio := pio.PIO0
	offset, err := Pio.AddProgram(hsyncProgram, -1)
	if err != nil {
		println("Failed to load program:", err.Error())
		blinkError(led)
		return
	}
	println("Program loaded at offset", offset)

	// Get state machine
	sm := Pio.StateMachine(0)
	sm.TryClaim()

	// Configure state machine
	cfg := pio.DefaultStateMachineConfig()
	cfg.SetSetPins(pinHSYNC, 1)

	// Clock divider: 125 MHz / 25 MHz = 5
	cfg.SetClkDivIntFrac(5, 0)

	// Configure pin for PIO control
	pinHSYNC.Configure(machine.PinConfig{Mode: Pio.PinMode()})
	sm.SetPindirsConsecutive(pinHSYNC, 1, true)

	sm.Init(offset, cfg)

	// Push the high period count
	// Target: 31.468 kHz HSYNC
	// Calibration: 1190→31003 Hz, need 31468 Hz
	// Adjusted: 1190 * (31003/31468) ≈ 1172
	highCount := uint32(1172)
	sm.TxPut(highCount)

	println("Starting PIO with high count:", highCount)
	sm.SetEnabled(true)

	// Set all RGB pins HIGH using machine.Pin
	println("Setting all RGB pins HIGH...")
	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.High()
	}
	println("All RGB pins set HIGH")

	// Main loop - handle VSYNC and RGB output
	frameCount := 0
	lineCount := 0

	for {
		// Wait for PIO IRQ 0 (end of sync pulse)
		for Pio.GetIRQ()&1 == 0 {
			// Busy wait for IRQ
		}
		Pio.ClearIRQ(1) // Clear IRQ 0

		// Back porch delay (~48 pixels at 25MHz = ~1.9µs)
		// At 125MHz CPU, need ~240 cycles. Loop overhead helps.
		for i := 0; i < 60; i++ {
			_ = gpioOut.Get() // volatile read as delay
		}

		// Active video - output color bars (only during visible lines 0-479)
		if lineCount < 480 {
			// 8 color bars, each 80 pixels wide (640/8 = 80)
			// At 125MHz CPU, 80 pixels at 25MHz = 400 CPU cycles per bar
			// Colors: White, Yellow, Cyan, Green, Magenta, Red, Blue, Black
			// RGB565: R=GPIO0-4, G=GPIO5-10, B=GPIO11-15

			// Bar 1: White (all on)
			gpioOutSet.Set(0xFFFF)
			for i := 0; i < 100; i++ {
				_ = gpioOut.Get()
			}

			// Bar 2: Yellow (R+G)
			gpioOutClr.Set(0xF800) // Clear blue
			gpioOutSet.Set(0x07FF) // Set red+green
			for i := 0; i < 100; i++ {
				_ = gpioOut.Get()
			}

			// Bar 3: Cyan (G+B)
			gpioOutClr.Set(0x001F) // Clear red
			gpioOutSet.Set(0xFFE0) // Set green+blue
			for i := 0; i < 100; i++ {
				_ = gpioOut.Get()
			}

			// Bar 4: Green
			gpioOutClr.Set(0xF81F) // Clear red+blue
			gpioOutSet.Set(0x07E0) // Set green
			for i := 0; i < 100; i++ {
				_ = gpioOut.Get()
			}

			// Bar 5: Magenta (R+B)
			gpioOutClr.Set(0x07E0) // Clear green
			gpioOutSet.Set(0xF81F) // Set red+blue
			for i := 0; i < 100; i++ {
				_ = gpioOut.Get()
			}

			// Bar 6: Red
			gpioOutClr.Set(0xFFE0) // Clear green+blue
			gpioOutSet.Set(0x001F) // Set red
			for i := 0; i < 100; i++ {
				_ = gpioOut.Get()
			}

			// Bar 7: Blue
			gpioOutClr.Set(0x07FF) // Clear red+green
			gpioOutSet.Set(0xF800) // Set blue
			for i := 0; i < 100; i++ {
				_ = gpioOut.Get()
			}

			// Bar 8: Black
			gpioOutClr.Set(0xFFFF) // Clear all
			for i := 0; i < 100; i++ {
				_ = gpioOut.Get()
			}
		}

		// Front porch - clear RGB
		gpioOutClr.Set(rgbMask)

		lineCount++

		// Handle VSYNC
		if lineCount == vSyncStart {
			pinVSYNC.Low()
		} else if lineCount == vSyncEnd {
			pinVSYNC.High()
		} else if lineCount >= vTotal {
			lineCount = 0
			frameCount++

			// No LED toggle - it disrupts timing
		}
	}
}

func blinkError(led machine.Pin) {
	for {
		led.Low()
		time.Sleep(100 * time.Millisecond)
		led.High()
		time.Sleep(100 * time.Millisecond)
	}
}
