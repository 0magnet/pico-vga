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

		// new_state (offset 1) - wrap target:
		//   out exec, 16            ; Execute embedded instruction (sets IRQ)
		//   out x, 13               ; Get cycle count into X
		//   out pins, 3             ; Output sync pins (hsync, vsync, den)
		asm.Out(pio.OutDestExec, 16).Encode(), // 1: Execute instruction from OSR
		asm.Out(pio.OutDestX, 13).Encode(),    // 2: Load cycle count
		asm.Out(pio.OutDestPins, 3).Encode(),  // 3: Output hsync/vsync

		// loop (offset 4):
		//   nop
		//   jmp x-- loop
		asm.Nop().Encode(),                    // 4
		asm.Jmp(pio.JmpXNZeroDec, 4).Encode(), // 5: Delay loop - wrap back to offset 1
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
// This is an EXACT port of scanvideo.pio from pico-extras
//
// The program MUST be loaded at offset 0 because the data contains
// absolute addresses (jump targets) that are hardcoded
//
// For xscale=1 (default), this matches video_24mhz_composable_default exactly:
//   0x6060, 0x20c4, 0x60b0, 0x6010, 0x6030, 0x0045, 0x60b0,
//   0x6010, 0x6030, 0x6010, 0x0049, 0x6010, 0x60b0, 0x6010,
//   0x6000, 0x60b0
//
// For xscale>1, delays are added to stretch pixels horizontally
func BuildScanlineProgram(xscale uint8) []uint16 {
	asm := pio.AssemblerV0{}

	// Calculate delay based on xscale (matches C code EXACTLY)
	// C code: delay0 = 2 * xscale - 2; delay1 = delay0 + 1;
	// For xscale=1: delay0=0, delay1=1
	// For xscale=2: delay0=2, delay1=3
	var extra0, extra1 uint8
	extra0 = 2*xscale - 2
	extra1 = extra0 + 1

	program := []uint16{
		// 0: end_of_scanline_skip_ALIGN - discard remaining OSR
		asm.Out(pio.OutDestNull, 32).Encode(), // 0: 0x6060

		// 1: end_of_scanline_ALIGN / entry_point - wait for timing IRQ
		asm.WaitIRQ(true, false, 4).Encode(), // 1: 0x20c4

		// 2: nop_raw - main dispatch, jump based on data
		asm.Out(pio.OutDestPC, 16).Encode(), // 2: 0x60b0

		// 3-6: color_run - output single color for count pixels
		asm.Out(pio.OutDestPins, 16).Encode(),               // 3: 0x6010 - output color
		asm.Out(pio.OutDestX, 16).Encode(),                  // 4: 0x6030 - load count
		asm.Jmp(pio.JmpXNZeroDec, 5).Delay(extra1).Encode(), // 5: color_loop
		asm.Out(pio.OutDestPC, 16).Delay(extra1).Encode(),   // 6: next command

		// 7-10: raw_run - output multiple raw pixels
		asm.Out(pio.OutDestPins, 16).Delay(extra0).Encode(), // 7: raw_run - first pixel
		asm.Out(pio.OutDestX, 16).Encode(),                  // 8: 0x6030 - load count
		asm.Out(pio.OutDestPins, 16).Delay(extra0).Encode(), // 9: pixel_loop - output pixel
		asm.Jmp(pio.JmpXNZeroDec, 9).Encode(),               // 10: loop back to pixel_loop

		// 11-12: raw_1p - output single pixel (wrap target)
		asm.Out(pio.OutDestPins, 16).Delay(extra0).Encode(), // 11: raw_1p - output pixel
		asm.Out(pio.OutDestPC, 16).Encode(),                 // 12: 0x60b0 - next command

		// 13: raw_2p - output two pixels (wraps to raw_1p for second)
		asm.Out(pio.OutDestPins, 16).Delay(extra1).Encode(), // 13: raw_2p - first pixel (wraps)

		// 14-15: raw_1p_skip_ALIGN and nop_extra0
		asm.Out(pio.OutDestPins, 32).Encode(),               // 14: 0x6000 - raw_1p_skip_ALIGN (32-bit!)
		asm.Out(pio.OutDestPC, 16).Delay(extra0).Encode(),   // 15: nop_extra0
	}

	return program
}

// Composable command offsets - MUST match scanvideo.pio exactly
const (
	OffsetEOLSkipAlign   = 0
	OffsetEOLAlign       = 1
	OffsetEntryPoint     = 1
	OffsetColorRun       = 3
	OffsetRawRun         = 7
	OffsetRaw1P          = 11
	OffsetRaw2P          = 13
	OffsetRaw1PSkipAlign = 14
)

// COMPOSABLE_* constants for building scanline buffers
// These are the PIO instruction offsets used in composable scanline data
const (
	COMPOSABLE_COLOR_RUN       = OffsetColorRun
	COMPOSABLE_EOL_ALIGN       = OffsetEOLAlign
	COMPOSABLE_EOL_SKIP_ALIGN  = OffsetEOLSkipAlign
	COMPOSABLE_RAW_RUN         = OffsetRawRun
	COMPOSABLE_RAW_1P          = OffsetRaw1P
	COMPOSABLE_RAW_2P          = OffsetRaw2P
	COMPOSABLE_RAW_1P_SKIP_ALIGN = OffsetRaw1PSkipAlign
)

// Timing program wrap points (relative to program start)
const (
	TimingWrapTarget = 1 // new_state (out exec) - where wrap goes TO
	TimingWrapEnd    = 5 // jmp x-- loop - where wrap happens FROM
)

// Scanline program wrap points (relative to program start)
// Matches scanvideo.pio: .wrap_target before raw_1p, .wrap after raw_2p
const (
	ScanlineWrapTarget = 11 // raw_1p - where wrap goes TO
	ScanlineWrapEnd    = 13 // raw_2p - where wrap happens FROM
)

// ConfigureTimingSM sets up the timing state machine
func ConfigureTimingSM(p *pio.PIO, sm pio.StateMachine, offset uint8) {
	cfg := pio.DefaultStateMachineConfig()

	// Configure OUT pins for hsync, vsync (and optionally den)
	cfg.SetOutPins(SyncPinBase, 2) // HSYNC, VSYNC

	// Auto-pull enabled, shift right
	cfg.SetOutShift(true, true, 32)

	// Set clock divider for pixel clock
	// 125MHz / 4 = 31.25MHz (matches working example timing)
	cfg.SetClkDivIntFrac(4, 0)

	// Set wrap points: wrap from jmp x-- back to new_state (skip initial pull)
	wrapBottom := offset + TimingWrapTarget
	wrapTop := offset + TimingWrapEnd
	cfg.SetWrap(wrapBottom, wrapTop)

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

	// Shift right, autopull at 32 bits
	cfg.SetOutShift(true, true, 32)

	// Join FIFOs for 8-deep TX
	cfg.SetFIFOJoin(pio.FifoJoinTx)

	// Same clock as timing (125MHz / 4 = 31.25MHz)
	cfg.SetClkDivIntFrac(4, 0)

	// Enable sticky output - continuously assert OUT pins (matches C scanvideo)
	cfg.SetOutSpecial(true, false, 0)

	// Set wrap points: wrap from raw_2p back to raw_1p
	// Matches scanvideo.pio: .wrap_target at raw_1p (11), .wrap at raw_2p (13)
	wrapBottom := offset + ScanlineWrapTarget
	wrapTop := offset + ScanlineWrapEnd
	cfg.SetWrap(wrapBottom, wrapTop)

	// Configure RGB pins
	for i := uint8(0); i < ColorPinCount; i++ {
		pin := machine.Pin(uint8(ColorPinBase) + i)
		pin.Configure(machine.PinConfig{Mode: p.PinMode()})
	}
	sm.SetPindirsConsecutive(ColorPinBase, ColorPinCount, true)

	// Start at entry point (offset + 1), which is the wait IRQ instruction
	// This is where the SM waits between scanlines
	sm.Init(offset+OffsetEntryPoint, cfg)
}
