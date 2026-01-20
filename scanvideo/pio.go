package scanvideo

import (
	"machine"

	pio "github.com/tinygo-org/pio/rp2-pio"
)

// PIO program offsets (set after loading)
var (
	timingOffset   uint8
	scanlineOffset uint8
)

// BuildTimingProgram creates the horizontal timing PIO program
// This program generates HSYNC timing and signals IRQs for scanline synchronization
//
// The timing program:
// 1. Pulls timing data from FIFO (contains: instruction | cycle count | pin state)
// 2. Executes the instruction (usually sets an IRQ)
// 3. Loads the cycle count into X
// 4. Outputs pin state (hsync, vsync)
// 5. Delays for the cycle count
// 6. Loops back
//
// Each timing "state" is pushed as a 32-bit word:
// - Bits 0-15: Instruction to execute (irq set 0, irq set 1, irq set 4, irq clear 4)
// - Bits 16-28: Cycle count - 3
// - Bits 29-31: Pin state (hsync, vsync, den)
func BuildTimingProgram() []uint16 {
	asm := pio.AssemblerV0{}

	program := []uint16{
		// entry_point (offset 0):
		//   pull block              ; Get timing word from FIFO
		asm.Pull(false, true).Encode(), // 0

		// new_state (offset 1):
		//   out exec, 16            ; Execute embedded instruction (sets IRQ)
		//   out x, 13               ; Get cycle count into X
		//   out pins, 3             ; Output sync pins (hsync, vsync, den)
		asm.Out(pio.OutDestExec, 16).Encode(), // 1: Execute instruction from OSR
		asm.Out(pio.OutDestX, 13).Encode(),    // 2: Load cycle count
		asm.Out(pio.OutDestPins, 3).Encode(),  // 3: Output hsync/vsync

		// loop (offset 4):
		//   nop
		//   jmp x-- loop
		asm.Nop().Encode(),                         // 4
		asm.Jmp(pio.JmpXNZeroDec, 4).Encode(),      // 5: Delay loop

		//   jmp entry_point
		asm.Jmp(pio.JmpAlways, 0).Encode(), // 6: Get next timing word
	}

	return program
}

// TimingStateInstructions are the instructions that get executed via out exec
// These are loaded into a separate small program space
var TimingStateInstructions = []uint16{
	pio.AssemblerV0{}.IRQSet(false, 0).Encode(),   // SET_IRQ_0: Signal active scanline
	pio.AssemblerV0{}.IRQSet(false, 1).Encode(),   // SET_IRQ_1: Signal vblank
	pio.AssemblerV0{}.IRQSet(false, 4).Encode(),   // SET_IRQ_SCANLINE: Signal scanline SM to start
	pio.AssemblerV0{}.IRQClear(false, 4).Encode(), // CLEAR_IRQ_SCANLINE: Clear scanline IRQ
}

// Timing state indices
const (
	SetIRQ0          = 0
	SetIRQ1          = 1
	SetIRQScanline   = 2
	ClearIRQScanline = 3
)

// BuildScanlineProgram creates the composable scanline PIO program
// This program outputs pixels using a "composable" format where the data stream
// contains jump offsets that direct the PIO to different code paths
//
// The composable format allows:
// - COLOR_RUN: Output a single color for N pixels (efficient for solid areas)
// - RAW_RUN: Output N arbitrary pixels
// - RAW_1P, RAW_2P: Output 1 or 2 pixels
// - EOL_ALIGN: End of line marker
//
// Format: Data contains PIO addresses that cause jumps to specific handlers
func BuildScanlineProgram(xscale uint8) []uint16 {
	asm := pio.AssemblerV0{}

	// Calculate delay based on xscale
	// For xscale=1: no extra delay (extra0=0, extra1=1)
	// For xscale=2: 1 extra cycle (extra0=1, extra1=2)
	extra0 := xscale - 1
	if xscale < 1 {
		extra0 = 0
	}
	extra1 := extra0 + 1

	// The program MUST be loaded at offset 0 because the data contains
	// absolute addresses (jump targets)
	program := []uint16{
		// 0: end_of_scanline_skip_ALIGN - discard remaining OSR
		asm.Out(pio.OutDestNull, 32).Encode(),

		// 1: nop (padding to align entry_point)
		asm.Nop().Encode(),

		// 2: nop (padding to align entry_point)
		asm.Nop().Encode(),

		// entry_point (offset 3): Wait for timing IRQ, then start processing
		asm.WaitIRQ(true, false, 4).Encode(), // 3: Wait for IRQ 4 from timing SM

		// nop_raw (offset 4): Main dispatch - jump based on data
		asm.Out(pio.OutDestPC, 16).Encode(), // 4: Jump to address from data

		// color_run (offset 5): Output single color for count pixels
		// Format: | jmp color_run | color | count-3 |
		asm.Out(pio.OutDestPins, 16).Encode(),             // 5: Output color
		asm.Out(pio.OutDestX, 16).Encode(),                // 6: Load count
		// color_loop (offset 7):
		asm.Jmp(pio.JmpXNZeroDec, 7).Delay(extra1).Encode(), // 7: Loop with delay
		asm.Out(pio.OutDestPC, 16).Delay(extra1).Encode(),   // 8: Next command

		// raw_run (offset 9): Output multiple pixels
		// Format: | jmp raw_run | color | n | <n+2 colors> |
		asm.Out(pio.OutDestPins, 16).Delay(extra0).Encode(), // 9: First pixel
		asm.Out(pio.OutDestX, 16).Encode(),                  // 10: Load count
		// pixel_loop (offset 11):
		asm.Out(pio.OutDestPins, 16).Delay(extra0).Encode(), // 11: Output pixel
		asm.Jmp(pio.JmpXNZeroDec, 11).Encode(),              // 12: Loop

		// raw_1p (offset 13): Output single pixel
		// Format: | jmp raw_1p | color |
		asm.Out(pio.OutDestPins, 16).Delay(extra0).Encode(), // 13: Output pixel
		asm.Out(pio.OutDestPC, 16).Encode(),                 // 14: Next command

		// raw_2p (offset 15): Output two pixels
		// Format: | jmp raw_2p | color | color |
		asm.Out(pio.OutDestPins, 16).Delay(extra1).Encode(), // 15: First pixel (wraps)
	}

	// Pad to ensure offsets are correct
	for len(program) < 16 {
		program = append(program, asm.Nop().Encode())
	}

	return program
}

// Composable command offsets (must match BuildScanlineProgram)
const (
	OffsetEOLSkipAlign = 0
	OffsetEOLAlign     = 3
	OffsetEntryPoint   = 3
	OffsetColorRun     = 5
	OffsetRawRun       = 9
	OffsetRaw1P        = 13
	OffsetRaw2P        = 15
)

// ConfigureTimingSM sets up the timing state machine
func ConfigureTimingSM(p *pio.PIO, sm pio.StateMachine, offset uint8) {
	cfg := pio.DefaultStateMachineConfig()

	// Configure OUT pins for hsync, vsync (and optionally den)
	cfg.SetOutPins(SyncPinBase, 2) // HSYNC, VSYNC

	// Auto-pull enabled, shift right
	cfg.SetOutShift(true, true, 32)

	// Set clock divider for pixel clock
	// 125MHz / 5 = 25MHz (for 640x480@60Hz which needs 25.175MHz)
	cfg.SetClkDivIntFrac(5, 0)

	// Configure pins
	for i := uint8(0); i < 2; i++ {
		pin := machine.Pin(uint8(SyncPinBase) + i)
		pin.Configure(machine.PinConfig{Mode: p.PinMode()})
	}
	sm.SetPindirsConsecutive(SyncPinBase, 2, true)

	sm.Init(offset, cfg)
}

// ConfigureScanlineSM sets up the scanline state machine
func ConfigureScanlineSM(p *pio.PIO, sm pio.StateMachine, offset uint8) {
	cfg := pio.DefaultStateMachineConfig()

	// Configure OUT pins for RGB
	cfg.SetOutPins(ColorPinBase, ColorPinCount)

	// Shift right, no auto-pull (we control pulling)
	cfg.SetOutShift(true, false, 32)

	// Join FIFOs for 8-deep TX
	cfg.SetFIFOJoin(pio.FifoJoinTx)

	// Same clock as timing
	cfg.SetClkDivIntFrac(5, 0)

	// Configure RGB pins
	for i := uint8(0); i < ColorPinCount; i++ {
		pin := machine.Pin(uint8(ColorPinBase) + i)
		pin.Configure(machine.PinConfig{Mode: p.PinMode()})
	}
	sm.SetPindirsConsecutive(ColorPinBase, ColorPinCount, true)

	sm.Init(offset, cfg)
}
