// Minimal Solid Color Test - VGA 640x480@60Hz
// Stripped down to absolute minimum for debugging
// Build: tinygo build -target=pico -o test_pattern.uf2 .
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

const (
	hActive     = 640
	hFrontPorch = 16
	hPulse      = 96
	hTotal      = 800

	vActive     = 480
	vFrontPorch = 10
	vPulse      = 2
	vTotal      = 525

	hSyncPolarity = 0
	vSyncPolarity = 0
)

const (
	COMPOSABLE_EOL_ALIGN = 1
	COMPOSABLE_COLOR_RUN = 3
	COMPOSABLE_RAW_1P    = 11
)

const (
	SET_IRQ_0          = 0
	SET_IRQ_1          = 1
	SET_IRQ_SCANLINE   = 2
	CLEAR_IRQ_SCANLINE = 3
)

var timingInstructions [4]uint16

var timingState struct {
	vActive, vTotal, vPulseStart, vPulseEnd int32
	vsyncBitsPulse, vsyncBitsNoPulse        uint32
	a, aVblank, b1, b2, c, cVblank          uint32
	vsyncBits                               uint32
	dmaStateIndex                           uint16
	timingScanline                          int32
}

var dmaStates [4]uint32

// SINGLE scanline buffer - pre-built once, never changed
var scanline [3]uint32

var (
	videoPIO       *pio.PIO
	timingSM       pio.StateMachine
	scanlineSM     pio.StateMachine
	timingOffset   uint8
	scanlineOffset uint8
)

type dmaChannel struct {
	ReadAddr   volatile.Register32
	WriteAddr  volatile.Register32
	TransCount volatile.Register32
	CtrlTrig   volatile.Register32
}

func getDMAChannel(ch int) *dmaChannel {
	base := uintptr(0x50000000) + uintptr(ch)*0x40
	return (*dmaChannel)(unsafe.Pointer(base))
}

// WHITE in RGB555 format (Pimoroni: R[4:0] G[10:6] B[15:11])
const WHITE = 0xFFDF // R=31, G=31, B=31

func main() {
	time.Sleep(3 * time.Second)
	println("=== Minimal Solid Color Test ===")
	println("VGA 640x480@60Hz - WHITE screen")
	println("Press 'r' to reboot")

	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.High()

	if !initVideo() {
		println("Video init failed!")
		for {
			led.Low()
			time.Sleep(100 * time.Millisecond)
			led.High()
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Force GPIO 0-15 to PIO0 (FUNCSEL=6)
	for i := 0; i < 16; i++ {
		ctrlAddr := (*volatile.Register32)(unsafe.Pointer(uintptr(0x40014000 + 8*i + 4)))
		ctrlAddr.Set(6)
	}

	// Build ONE scanline, used for every line
	// COLOR_RUN format: 3 words total
	const MIN_COLOR_RUN = 3
	scanline[0] = uint32(COMPOSABLE_COLOR_RUN) | (uint32(WHITE) << 16)
	scanline[1] = uint32(hActive-MIN_COLOR_RUN) | (uint32(COMPOSABLE_RAW_1P) << 16)
	scanline[2] = 0 | (uint32(COMPOSABLE_EOL_ALIGN) << 16)
	println("Scanline: [0]=", hex(scanline[0]), "[1]=", hex(scanline[1]), "[2]=", hex(scanline[2]))

	// Prime scanline FIFO
	scanlineSM.TxPut(scanline[0])
	scanlineSM.TxPut(scanline[1])
	scanlineSM.TxPut(scanline[2])

	enableVideo(true)
	println("Video enabled")

	// Counters
	var hsyncCount, frameCount, dmaCount volatile.Register32
	line := 0

	// Main render loop - NO GOROUTINE, direct polling
	lastHsync := uint32(0)
	lastFrame := uint32(0)
	lastTime := time.Now()

	for {
		// Check serial
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == 'r' || b == 'R' {
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			}
		}

		// Feed timing FIFO
		topUpTimingFIFO()

		// Check IRQs
		irqs := videoPIO.GetIRQ()

		if irqs&1 != 0 {
			// Active scanline
			videoPIO.ClearIRQ(1)
			hsyncCount.Set(hsyncCount.Get() + 1)

			if line < vActive {
				// Start DMA transfer
				dma := getDMAChannel(0)
				dma.ReadAddr.Set(uint32(uintptr(unsafe.Pointer(&scanline[0]))))
				dma.TransCount.Set(3)
				dma.CtrlTrig.SetBits(1)
				dmaCount.Set(dmaCount.Get() + 1)
			}
			line++
		}

		if irqs&2 != 0 {
			// Vblank scanline
			videoPIO.ClearIRQ(2)
			hsyncCount.Set(hsyncCount.Get() + 1)
			line++
		}

		// Frame boundary
		if line >= vTotal {
			line = 0
			frameCount.Set(frameCount.Get() + 1)

			// Status every 2 seconds
			f := frameCount.Get()
			if f > 0 && f%120 == 0 && f != lastFrame {
				now := time.Now()
				elapsedMs := now.Sub(lastTime).Milliseconds()
				if elapsedMs == 0 {
					elapsedMs = 1
				}

				h := hsyncCount.Get()
				hsyncHz := ((h - lastHsync) * 1000) / uint32(elapsedMs)
				vsyncHz := ((f - lastFrame) * 1000) / uint32(elapsedMs)

				println("HSYNC:", hsyncHz, "Hz, VSYNC:", vsyncHz, "Hz")
				println("DMA count:", dmaCount.Get())

				lastHsync = h
				lastFrame = f
				lastTime = now
			}
		}
	}
}

func initVideo() bool {
	videoPIO = pio.PIO0

	asm := pio.AssemblerV0{}
	timingInstructions[SET_IRQ_0] = asm.IRQSet(false, 0).Encode()
	timingInstructions[SET_IRQ_1] = asm.IRQSet(false, 1).Encode()
	timingInstructions[SET_IRQ_SCANLINE] = asm.IRQSet(false, 4).Encode()
	timingInstructions[CLEAR_IRQ_SCANLINE] = asm.IRQClear(false, 4).Encode()

	initTimingState()

	// Load scanline program at offset 0
	scanlineProgram := buildScanlineProgram()
	offset, err := videoPIO.AddProgram(scanlineProgram, 0)
	if err != nil {
		println("Failed to load scanline program:", err.Error())
		return false
	}
	scanlineOffset = offset

	// Load timing program at offset 16
	timingProgram := buildTimingProgram()
	offset, err = videoPIO.AddProgram(timingProgram, 16)
	if err != nil {
		println("Failed to load timing program:", err.Error())
		return false
	}
	timingOffset = offset

	// Configure scanline SM (SM0)
	scanlineSM = videoPIO.StateMachine(0)
	scanlineSM.TryClaim()

	scanlineCfg := pio.DefaultStateMachineConfig()
	scanlineCfg.SetOutPins(pinRGBBase, 16)
	scanlineCfg.SetOutShift(true, true, 32)
	scanlineCfg.SetFIFOJoin(pio.FifoJoinTx)
	scanlineCfg.SetClkDivIntFrac(4, 0)
	scanlineCfg.SetOutSpecial(true, false, 0) // Sticky output

	for pin := pinRGBBase; pin < pinRGBBase+16; pin++ {
		pin.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	}
	scanlineSM.SetPindirsConsecutive(pinRGBBase, 16, true)
	scanlineSM.Init(scanlineOffset+1, scanlineCfg)

	// Configure timing SM (SM3)
	timingSM = videoPIO.StateMachine(3)
	timingSM.TryClaim()

	timingCfg := pio.DefaultStateMachineConfig()
	timingCfg.SetOutPins(pinHSYNC, 2)
	timingCfg.SetOutShift(true, true, 32)
	timingCfg.SetClkDivIntFrac(4, 0)
	timingCfg.SetWrap(timingOffset+1, timingOffset+5)

	pinHSYNC.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	pinVSYNC.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	timingSM.SetPindirsConsecutive(pinHSYNC, 2, true)
	timingSM.Init(timingOffset, timingCfg)

	setupDMA()
	return true
}

func initTimingState() {
	timingState.vTotal = vTotal
	timingState.vActive = vActive
	timingState.vPulseStart = int32(vActive + vFrontPorch)
	timingState.vPulseEnd = timingState.vPulseStart + vPulse

	const vsyncBit = 0x40000000
	if vSyncPolarity != 0 {
		timingState.vsyncBitsPulse = 0
		timingState.vsyncBitsNoPulse = vsyncBit
	} else {
		timingState.vsyncBitsPulse = vsyncBit
		timingState.vsyncBitsNoPulse = 0
	}

	var hSyncBit uint32
	if hSyncPolarity == 0 {
		hSyncBit = 1
	}
	hSyncNoBit := 1 - hSyncBit
	hBackPorch := hTotal - hActive - hFrontPorch - hPulse
	hActiveAndFront := hActive + hFrontPorch

	timingState.a = timingEncode(SET_IRQ_0, 4, hSyncBit)
	timingState.aVblank = timingEncode(SET_IRQ_1, 4, hSyncBit)
	timingState.b1 = timingEncode(CLEAR_IRQ_SCANLINE, hPulse-4, hSyncBit)
	timingState.b2 = timingEncode(CLEAR_IRQ_SCANLINE, hBackPorch, hSyncNoBit)
	timingState.c = timingEncode(SET_IRQ_SCANLINE, hActiveAndFront, 4|hSyncNoBit)
	timingState.cVblank = timingEncode(CLEAR_IRQ_SCANLINE, hActiveAndFront, hSyncNoBit)

	setupDmaStatesVblank()
	timingState.vsyncBits = timingState.vsyncBitsNoPulse
}

func timingEncode(cmd int, cycles int, pins uint32) uint32 {
	return uint32(timingInstructions[cmd]) | (uint32(cycles-3) << 16) | (pins << 29)
}

func setupDmaStatesVblank() {
	dmaStates[0] = timingState.aVblank
	dmaStates[1] = timingState.b1
	dmaStates[2] = timingState.b2
	dmaStates[3] = timingState.cVblank
}

func setupDmaStatesActive() {
	dmaStates[0] = timingState.a
	dmaStates[1] = timingState.b1
	dmaStates[2] = timingState.b2
	dmaStates[3] = timingState.c
}

func topUpTimingFIFO() {
	for !timingSM.IsTxFIFOFull() {
		timingSM.TxPut(dmaStates[timingState.dmaStateIndex] | timingState.vsyncBits)
		timingState.dmaStateIndex++
		if timingState.dmaStateIndex >= 4 {
			timingState.dmaStateIndex = 0
			timingState.timingScanline++
			if timingState.timingScanline >= timingState.vActive {
				if timingState.timingScanline >= timingState.vTotal {
					timingState.timingScanline = 0
					setupDmaStatesActive()
				} else if timingState.timingScanline == timingState.vActive {
					setupDmaStatesVblank()
				} else if timingState.timingScanline == timingState.vPulseStart {
					timingState.vsyncBits = timingState.vsyncBitsPulse
				} else if timingState.timingScanline == timingState.vPulseEnd {
					timingState.vsyncBits = timingState.vsyncBitsNoPulse
				}
			}
		}
	}
}

func buildTimingProgram() []uint16 {
	asm := pio.AssemblerV0{}
	return []uint16{
		asm.Pull(false, true).Encode(),
		asm.Out(pio.OutDestExec, 16).Encode(),
		asm.Out(pio.OutDestX, 13).Encode(),
		asm.Out(pio.OutDestPins, 3).Encode(),
		asm.Nop().Encode(),
		asm.Jmp(pio.JmpXNZeroDec, 4).Encode(),
	}
}

func buildScanlineProgram() []uint16 {
	asm := pio.AssemblerV0{}
	const delay1 = 1 << 8

	return []uint16{
		asm.Out(pio.OutDestNull, 32).Encode(),          // 0: EOL_SKIP_ALIGN
		asm.WaitIRQ(true, false, 4).Encode(),           // 1: entry / EOL_ALIGN
		asm.Out(pio.OutDestPC, 16).Encode(),            // 2: dispatch

		// color_run (3-6)
		asm.Out(pio.OutDestPins, 16).Encode(),          // 3
		asm.Out(pio.OutDestX, 16).Encode(),             // 4
		asm.Jmp(pio.JmpXNZeroDec, 5).Encode() | delay1, // 5
		asm.Out(pio.OutDestPC, 16).Encode() | delay1,   // 6

		// raw_run (7-10)
		asm.Out(pio.OutDestPins, 16).Encode(),          // 7
		asm.Out(pio.OutDestX, 16).Encode(),             // 8
		asm.Out(pio.OutDestPins, 16).Encode(),          // 9
		asm.Jmp(pio.JmpXNZeroDec, 9).Encode(),          // 10

		// raw_1p (11-12)
		asm.Out(pio.OutDestPins, 16).Encode(),          // 11
		asm.Out(pio.OutDestPC, 16).Encode(),            // 12

		// raw_2p (13)
		asm.Out(pio.OutDestPins, 16).Encode() | delay1, // 13

		// raw_1p_skip_ALIGN (14-15)
		asm.Out(pio.OutDestPins, 32).Encode(),          // 14
		asm.Out(pio.OutDestPC, 16).Encode(),            // 15
	}
}

func enableVideo(enable bool) {
	timingSM.SetEnabled(false)
	scanlineSM.SetEnabled(false)

	if enable {
		for i := 0; i < 8; i++ {
			topUpTimingFIFO()
		}

		rp.PIO0.IRQ0_INTE.SetBits(0x03)
		rp.PIO0.IRQ1_INTE.SetBits(1 << (8 + 3))
		videoPIO.ClearIRQ(0xFF)

		scanlineSM.Exec(uint16(scanlineOffset + 1))
		timingSM.Exec(uint16(timingOffset))

		timingSM.SetEnabled(true)
		scanlineSM.SetEnabled(true)
	}
}

func setupDMA() {
	rp.RESETS.RESET.ClearBits(rp.RESETS_RESET_DMA)
	for !rp.RESETS.RESET_DONE.HasBits(rp.RESETS_RESET_DONE_DMA) {
	}

	dma := getDMAChannel(0)
	txFifo := uint32(0x50200010) // PIO0 TXF0

	ctrl := uint32(0)
	ctrl |= 1 << 0  // Enable
	ctrl |= 1 << 1  // High priority
	ctrl |= 2 << 2  // 32-bit
	ctrl |= 1 << 4  // Increment read
	// DREQ = PIO0_TX0 (bits 20:15 = 0)

	dma.WriteAddr.Set(txFifo)
	dma.CtrlTrig.Set(ctrl & ^uint32(1))
}

func hex(v uint32) string {
	digits := "0123456789ABCDEF"
	result := make([]byte, 10)
	result[0] = '0'
	result[1] = 'x'
	for i := 0; i < 8; i++ {
		result[9-i] = digits[v&0xF]
		v >>= 4
	}
	return string(result)
}
