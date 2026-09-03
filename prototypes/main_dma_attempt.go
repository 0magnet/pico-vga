// VGA 640x480@60Hz using PIO with DMA chaining for continuous pixel streaming
package main

import (
	"device/rp"
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

// VGA timing for 640x480@60Hz
const (
	vTotal     = 525
	vSyncStart = 490
	vSyncEnd   = 492
)

// DMA channel hardware registers
type dmaChannelHW struct {
	ReadAddr   volatile.Register32
	WriteAddr  volatile.Register32
	TransCount volatile.Register32
	CtrlTrig   volatile.Register32
	// Aliases
	Al1Ctrl       volatile.Register32
	Al1ReadAddr   volatile.Register32
	Al1WriteAddr  volatile.Register32
	Al1TransCount volatile.Register32
	Al2Ctrl       volatile.Register32
	Al2TransCount volatile.Register32
	Al2ReadAddr   volatile.Register32
	Al2WriteAddr  volatile.Register32
	Al3Ctrl       volatile.Register32
	Al3WriteAddr  volatile.Register32
	Al3TransCount volatile.Register32
	Al3ReadAddr   volatile.Register32 // Writing here triggers transfer
}

func getDMAChannel(ch int) *dmaChannelHW {
	base := uintptr(0x50000000) + uintptr(ch)*0x40
	return (*dmaChannelHW)(unsafe.Pointer(base))
}

// DMA control bits
const (
	DMA_CTRL_EN        = 1 << 0
	DMA_CTRL_HIGH_PRIO = 1 << 1
	DMA_CTRL_SIZE_8    = 0 << 2
	DMA_CTRL_SIZE_16   = 1 << 2
	DMA_CTRL_SIZE_32   = 2 << 2
	DMA_CTRL_INCR_READ = 1 << 4
	DMA_CTRL_INCR_WRITE = 1 << 5
	DMA_CTRL_RING_SIZE_POS = 6
	DMA_CTRL_RING_SEL  = 1 << 10
	DMA_CTRL_CHAIN_POS = 11
	DMA_CTRL_TREQ_POS  = 15
	DMA_CTRL_IRQ_QUIET = 1 << 21
)

// DREQ values
const (
	DREQ_PIO0_TX0 = 0
	DREQ_PIO0_TX1 = 1
)

// Color buffer - 8 colors aligned to 32 bytes for DMA read ring
// Must be power of 2 size (8 * 4 = 32 bytes) and 32-byte aligned
//
//go:align 32
var colorBuffer = [8]uint32{
	0xFFFF, // White
	0xFFE0, // Yellow
	0x07FF, // Cyan
	0x07E0, // Green
	0xF81F, // Magenta
	0xF800, // Red
	0x001F, // Blue
	0x0000, // Black
}

func main() {
	time.Sleep(2 * time.Second)
	println("=== VGA DMA Chaining Test ===")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	// Configure sync pins
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

	// Build HSYNC PIO program
	asm := pio.AssemblerV0{}
	hsyncProgram := []uint16{
		asm.Pull(false, true).Encode(),
		asm.Mov(pio.MovDestX, pio.MovSrcOSR).Encode(),
		asm.Jmp(pio.JmpXNZeroDec, 2).Encode(),
		asm.Set(pio.SetDestPins, 0).Delay(31).Encode(),
		asm.Set(pio.SetDestPins, 0).Delay(31).Encode(),
		asm.Set(pio.SetDestPins, 0).Delay(29).Encode(),
		asm.IRQSet(false, 0).Encode(), // IRQ 0 for software
		asm.IRQSet(false, 4).Encode(), // IRQ 4 for RGB SM
		asm.Set(pio.SetDestPins, 1).Encode(),
		asm.Jmp(pio.JmpAlways, 1).Encode(),
	}

	Pio := pio.PIO0
	hsyncOffset, err := Pio.AddProgram(hsyncProgram, -1)
	if err != nil {
		println("Failed to load HSYNC:", err.Error())
		blinkError(led)
		return
	}

	hsyncSM := Pio.StateMachine(0)
	hsyncSM.TryClaim()
	hsyncCfg := pio.DefaultStateMachineConfig()
	hsyncCfg.SetSetPins(pinHSYNC, 1)
	hsyncCfg.SetClkDivIntFrac(5, 0)
	pinHSYNC.Configure(machine.PinConfig{Mode: Pio.PinMode()})
	hsyncSM.SetPindirsConsecutive(pinHSYNC, 1, true)
	hsyncSM.Init(hsyncOffset, hsyncCfg)
	hsyncSM.TxPut(1172) // Calibrated high count

	// Build RGB PIO program - wait for IRQ, output 8 colors from FIFO
	rgbAsm := pio.AssemblerV0{}
	rgbProgram := []uint16{
		// Wait for IRQ 4 from HSYNC
		rgbAsm.WaitIRQ(true, false, 4).Encode(), // 0

		// Back porch delay ~76 cycles (keep RGB black during back porch)
		rgbAsm.Set(pio.SetDestX, 18).Encode(),                // 1
		rgbAsm.Mov(pio.MovDestPins, pio.MovSrcNull).Encode(), // 2
		rgbAsm.Jmp(pio.JmpXNZeroDec, 2).Encode(),             // 3 (19*2=38)
		rgbAsm.Set(pio.SetDestX, 18).Encode(),                // 4
		rgbAsm.Nop().Encode(),                                // 5
		rgbAsm.Jmp(pio.JmpXNZeroDec, 5).Encode(),             // 6 (19*2=38, total 76)

		// 8 color bars from FIFO (y=8 gives 8 iterations: decrement then check, stops when result=0)
		rgbAsm.Set(pio.SetDestY, 8).Encode(), // 7: y=8 for 8 iterations

		// Bar loop - each bar exactly 80 cycles (640/8=80 pixels)
		rgbAsm.Pull(false, true).Encode(),                   // 8: 1 cycle
		rgbAsm.Mov(pio.MovDestPins, pio.MovSrcOSR).Encode(), // 9: 1 cycle
		rgbAsm.Set(pio.SetDestX, 24).Encode(),               // 10: 1 cycle
		rgbAsm.Nop().Delay(1).Encode(),                      // 11: 2 cycles [1]
		rgbAsm.Jmp(pio.JmpXNZeroDec, 11).Encode(),           // 12: 25*3=75 cycles
		rgbAsm.Nop().Encode(),                               // 13: 1 cycle (padding to reach 80)
		rgbAsm.Jmp(pio.JmpYNZeroDec, 8).Encode(),            // 14: total = 1+1+1+75+1 = 79 + 1 jmp = 80

		// After all bars, set black and loop back
		rgbAsm.Mov(pio.MovDestPins, pio.MovSrcNull).Encode(), // 15
		rgbAsm.Jmp(pio.JmpAlways, 0).Encode(),                // 16
	}

	rgbOffset, err := Pio.AddProgram(rgbProgram, -1)
	if err != nil {
		println("Failed to load RGB:", err.Error())
		blinkError(led)
		return
	}

	rgbSM := Pio.StateMachine(1)
	rgbSM.TryClaim()
	rgbCfg := pio.DefaultStateMachineConfig()
	rgbCfg.SetOutPins(pinRGBBase, 16)
	rgbCfg.SetClkDivIntFrac(5, 0)
	rgbCfg.SetOutShift(true, false, 32)
	rgbCfg.SetFIFOJoin(pio.FifoJoinTx)
	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: Pio.PinMode()})
	}
	rgbSM.SetPindirsConsecutive(pinRGBBase, 16, true)
	rgbSM.Init(rgbOffset, rgbCfg)

	// ========================================
	// DMA Setup - Simple continuous with read ring
	// ========================================
	println("Setting up DMA with read ring...")

	// Enable DMA
	rp.RESETS.RESET.ClearBits(rp.RESETS_RESET_DMA)
	for !rp.RESETS.RESET_DONE.HasBits(rp.RESETS_RESET_DONE_DMA) {
	}

	dma0 := getDMAChannel(0)
	txFifoAddr := uint32(uintptr(unsafe.Pointer(&rgbSM.TxReg().Reg)))

	// Channel 0: Transfer color buffer to PIO TX FIFO continuously
	// Uses read ring to wrap around the 32-byte (8-word) color buffer
	ctrl0 := uint32(0)
	ctrl0 |= DMA_CTRL_EN
	ctrl0 |= DMA_CTRL_HIGH_PRIO
	ctrl0 |= DMA_CTRL_SIZE_32
	ctrl0 |= DMA_CTRL_INCR_READ                // Increment read (will wrap via ring)
	ctrl0 |= DREQ_PIO0_TX1 << DMA_CTRL_TREQ_POS // Paced by PIO TX FIFO
	ctrl0 |= 0 << DMA_CTRL_CHAIN_POS            // Chain to self (keeps running)
	ctrl0 |= 5 << DMA_CTRL_RING_SIZE_POS        // Ring size 2^5 = 32 bytes (8 words)
	// RING_SEL=0 means ring on read side (default)
	ctrl0 |= DMA_CTRL_IRQ_QUIET

	dma0.ReadAddr.Set(uint32(uintptr(unsafe.Pointer(&colorBuffer[0]))))
	dma0.WriteAddr.Set(txFifoAddr)
	dma0.TransCount.Set(0xFFFFFFFF) // Run essentially forever

	colorBufferAddr := uint32(uintptr(unsafe.Pointer(&colorBuffer[0])))
	println("DMA configured")
	println("Color buffer addr:", colorBufferAddr)
	println("Color buffer aligned to 32?", colorBufferAddr&0x1F == 0)
	println("TX FIFO addr:", txFifoAddr)

	// Pre-fill the FIFO before starting
	for i := 0; i < 8; i++ {
		rgbSM.TxPut(colorBuffer[i])
	}

	// Start both state machines
	hsyncSM.SetEnabled(true)
	rgbSM.SetEnabled(true)
	println("State machines started")

	// Start DMA channel 0
	dma0.CtrlTrig.Set(ctrl0)

	println("DMA started with read ring")

	// Main loop - handle VSYNC and push colors via software (DMA disabled for testing)
	lineCount := 0
	frameCount := 0

	// Disable DMA for this test - use software push
	dma0.CtrlTrig.Set(0) // Disable DMA

	for {
		// Wait for line IRQ
		for Pio.GetIRQ()&1 == 0 {
		}
		Pio.ClearIRQ(1)

		lineCount++

		// Push 8 colors for visible lines
		if lineCount <= 480 {
			for i := 0; i < 8; i++ {
				rgbSM.TxPut(colorBuffer[i])
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
