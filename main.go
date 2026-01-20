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
		asm.Set(pio.SetDestPins, 0).Delay(29).Encode(),   // 5: set pins, 0 [29] (94 total so far)
		asm.IRQSet(false, 0).Encode(),                    // 6: irq 0 (for software)
		asm.IRQSet(false, 4).Encode(),                    // 7: irq 4 (for RGB SM)
		asm.Set(pio.SetDestPins, 1).Encode(),             // 8: set pins, 1
		asm.Jmp(pio.JmpAlways, 1).Encode(),               // 9: jmp 1 (wrap back)
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

	println("Starting HSYNC PIO with high count:", highCount)

	// ========================================
	// RGB State Machine (SM1) - PIO-based pixel output
	// ========================================
	rgbAsm := pio.AssemblerV0{}

	// RGB program: wait for IRQ 4, output 8 color bars
	// Total active: ~1180 cycles, each bar: ~147 cycles
	// Using pull for each bar color
	rgbProgram := []uint16{
		// Wait for IRQ 4 from HSYNC (start of back porch)
		rgbAsm.WaitIRQ(true, false, 4).Encode(),           // 0: wait 1 irq 4

		// Back porch - increased to ~104 cycles to shift bars right
		rgbAsm.Set(pio.SetDestX, 25).Encode(),             // 1: set x, 25
		rgbAsm.Mov(pio.MovDestPins, pio.MovSrcNull).Encode(), // 2: mov pins, null
		rgbAsm.Jmp(pio.JmpXNZeroDec, 2).Encode(),          // 3: jmp x-- 2
		rgbAsm.Set(pio.SetDestX, 25).Encode(),             // 4: another 50 cycles
		rgbAsm.Nop().Encode(),                             // 5: nop (loop body)
		rgbAsm.Jmp(pio.JmpXNZeroDec, 5).Encode(),          // 6: jmp x-- 5

		// Active video - 7 color bars + 1 black bar
		// Working config: x=6, nop[15]: 6*17=102 + 7 overhead = 109 cycles/bar
		// Note: Cannot exceed ~110 cycles without breaking sync
		rgbAsm.Set(pio.SetDestY, 7).Encode(),              // 7: set y, 7 (7 bars)

		rgbAsm.Pull(false, true).Encode(),                 // 8: pull block
		rgbAsm.Mov(pio.MovDestPins, pio.MovSrcOSR).Encode(), // 9: mov pins, osr
		rgbAsm.Set(pio.SetDestX, 6).Encode(),              // 10: set x, 6 (6 iterations)
		rgbAsm.Nop().Delay(15).Encode(),                   // 11: nop [15] (16 cycles)
		rgbAsm.Jmp(pio.JmpXNZeroDec, 11).Encode(),         // 12: jmp x-- 11 (6*17=102)
		rgbAsm.Nop().Delay(2).Encode(),                    // 13: nop [2] (3 cycles)
		rgbAsm.Jmp(pio.JmpYNZeroDec, 8).Encode(),          // 14: jmp y-- 8 (next bar)

		// 8th bar (black) - same timing as color bars
		rgbAsm.Mov(pio.MovDestPins, pio.MovSrcNull).Encode(), // 15: mov pins, null (BLACK)
		rgbAsm.Set(pio.SetDestX, 6).Encode(),              // 16: set x, 6
		rgbAsm.Nop().Delay(15).Encode(),                   // 17: nop [15]
		rgbAsm.Jmp(pio.JmpXNZeroDec, 17).Encode(),         // 18: jmp x-- 17 (102 cycles)
		rgbAsm.Nop().Delay(2).Encode(),                    // 19: nop [2] (3 cycles)

		// Back to wait for next IRQ
		rgbAsm.Jmp(pio.JmpAlways, 0).Encode(),             // 20: jmp 0
	}

	println("RGB program built, length:", len(rgbProgram))
	for i, instr := range rgbProgram {
		println("  RGB", i, ":", instr)
	}

	rgbOffset, err := Pio.AddProgram(rgbProgram, -1)
	if err != nil {
		println("Failed to load RGB program:", err.Error())
		blinkError(led)
		return
	}
	println("RGB program loaded at offset", rgbOffset)

	rgbSM := Pio.StateMachine(1)
	rgbSM.TryClaim()

	rgbCfg := pio.DefaultStateMachineConfig()
	rgbCfg.SetOutPins(pinRGBBase, 16)
	rgbCfg.SetClkDivIntFrac(5, 0) // 25 MHz (same as HSYNC)

	// No autopull - using explicit pull
	rgbCfg.SetOutShift(true, false, 32)

	// Configure FIFO join for more TX depth
	rgbCfg.SetFIFOJoin(pio.FifoJoinTx)

	// Configure RGB pins for PIO
	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: Pio.PinMode()})
	}
	rgbSM.SetPindirsConsecutive(pinRGBBase, 16, true)
	rgbSM.Init(rgbOffset, rgbCfg)

	// Start both state machines
	sm.SetEnabled(true)
	rgbSM.SetEnabled(true)
	println("Both PIO state machines started")

	// 8 standard color bars (RGB565 format for this board)
	// R=GPIO0-4, G=GPIO5-10, B=GPIO11-15
	colorBars := []uint32{
		0xFFFF, // White
		0x07FF, // Yellow (R+G)
		0xFFE0, // Cyan (G+B)
		0x07E0, // Green
		0xF81F, // Magenta (R+B)
		0x001F, // Red
		0xF800, // Blue
		0x0000, // Black
	}
	println("Starting with 8 color bars")

	// Pre-fill FIFO with first line's colors
	for _, c := range colorBars {
		rgbSM.TxPut(c)
	}

	// Main loop - push colors and handle VSYNC
	frameCount := 0
	lineCount := 0

	for {
		// Wait for PIO IRQ 0 (line complete)
		for Pio.GetIRQ()&1 == 0 {
			// Busy wait for IRQ
		}
		Pio.ClearIRQ(1) // Clear IRQ 0

		lineCount++

		// Push colors for next line (during blanking period)
		if lineCount < 480 {
			// Active video - push color bars
			for _, c := range colorBars {
				rgbSM.TxPut(c)
			}
		} else {
			// Vertical blanking - push black
			for i := 0; i < 8; i++ {
				rgbSM.TxPut(0)
			}
		}

		// Handle VSYNC
		if lineCount == vSyncStart {
			pinVSYNC.Low()
		} else if lineCount == vSyncEnd {
			pinVSYNC.High()
		} else if lineCount >= vTotal {
			lineCount = 0
			frameCount++
			// Print status every 60 frames (~1 second)
			if frameCount%60 == 0 {
				println("Frame:", frameCount)
			}
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
