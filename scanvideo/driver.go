package scanvideo

import (
	"device/rp"
	"machine"
	"runtime"
	"runtime/interrupt"
	"runtime/volatile"
	"unsafe"

	pio "github.com/tinygo-org/pio/rp2-pio"
)


// Configuration constants
const (
	ScanlineBufferCount    = 32  // Number of scanline buffers (increased for 640x480)
	MaxScanlineBufferWords = 400 // Max words per scanline buffer (increased for safety at 640x480)

	// State machine assignments
	ScanlineSM = 0 // SM0 for scanline output
	TimingSM   = 3 // SM3 for timing

	// DMA channels
	ScanlineDMAChannel = 0
)

// Driver state
var (
	videoPIO  *pio.PIO
	videoMode Mode
	timing    *Timing

	// Scanline buffers
	scanlineBuffers [ScanlineBufferCount]scanlineBufferInternal
	freeList        *scanlineBufferInternal
	generatedList   *scanlineBufferInternal
	generatedTail   *scanlineBufferInternal
	inUseList       *scanlineBufferInternal
	currentBuffer   *scanlineBufferInternal

	// Timing state
	timingState struct {
		vActive     int32
		vTotal      int32
		vPulseStart int32
		vPulseEnd   int32
		vsyncPulse  uint32
		vsyncNoPulse uint32
		vsyncBits   uint32

		// Timing DMA states (4 states per line)
		a, aVblank uint32
		b1, b2     uint32
		c, cVblank uint32

		stateIndex     uint16
		timingScanline int32
		inVblank       bool
	}
	dmaStates [4]uint32

	// Scanline tracking
	nextScanlineID uint32
	lastScanlineID uint32
	yRepeatIndex   uint16
	yRepeatTarget  uint16

	// DMA state tracking (matches C scanvideo.c shared_state.dma)
	dmaInProgress    uint32 // 1 if DMA transfer is in progress
	buffersToRelease uint32 // count of buffers pending release

	// Pending release buffer - released at START of next scanline after DMA completes
	pendingReleaseBuffer *scanlineBufferInternal

	// Missing scanline data (blue line shown when buffer not ready)
	missingData [4]uint32

	// Video enabled flags (use volatile for interrupt-safe access)
	timingEnabled  volatile.Register32
	displayEnabled volatile.Register32
)

// Interrupt handlers - initialized lazily in initInterrupts()
var (
	irqPIO0_0 interrupt.Interrupt
	irqPIO0_1 interrupt.Interrupt
	irqsInitialized bool
)

// Debug counters for diagnosis
var debugCounters struct {
	irq0Count       volatile.Register32 // IRQ0 (active scanline) fires
	irq1Count       volatile.Register32 // IRQ1 (vblank) fires
	timingFIFOFills volatile.Register32 // Timing FIFO refills
	dmaStarts       volatile.Register32 // DMA transfers started
	bufferHits      volatile.Register32 // Scanline buffers used
	bufferMisses    volatile.Register32 // Missing data used
	framesComplete  volatile.Register32 // Frames completed
	lastDataUsed    volatile.Register32 // DataUsed from last buffer
	lastScanlineNum volatile.Register32 // Last scanline number processed
	dmaAborts       volatile.Register32 // DMA aborts performed
}

// DMA buffer capture for verifying actual PIO output
var dmaCapture struct {
	enabled      bool
	captured     bool
	scanline     uint16
	dataUsed     uint16
	words        [20]uint32 // First 20 words sent to DMA/PIO
	targetLine   uint16     // Which scanline to capture (0 = any)
	multiCapture bool       // If true, capture multiple consecutive scanlines
	linesCaptured int       // Number of lines captured in multi mode
	multiData    [10][20]uint32 // Data for up to 10 scanlines
	multiLines   [10]uint16     // Scanline numbers
	multiUsed    [10]uint16     // DataUsed for each
	multiAddr    [10]uint32     // DMA read addresses for verification
}

// EnableDMACapture enables capture of next scanline 0 DMA buffer
func EnableDMACapture() {
	dmaCapture.enabled = true
	dmaCapture.captured = false
	dmaCapture.targetLine = 0
	dmaCapture.multiCapture = false
}

// EnableMultiDMACapture enables capture of first N scanlines starting from startLine
func EnableMultiDMACapture(startLine uint16) {
	dmaCapture.enabled = true
	dmaCapture.captured = false
	dmaCapture.targetLine = startLine
	dmaCapture.multiCapture = true
	dmaCapture.linesCaptured = 0
}

// GetDMACapture returns the captured DMA buffer data
func GetDMACapture() (captured bool, scanline, dataUsed uint16, words [20]uint32) {
	return dmaCapture.captured, dmaCapture.scanline, dmaCapture.dataUsed, dmaCapture.words
}

// GetMultiDMACapture returns captured data for multiple scanlines
func GetMultiDMACapture() (count int, lines [10]uint16, used [10]uint16, data [10][20]uint32, addrs [10]uint32) {
	return dmaCapture.linesCaptured, dmaCapture.multiLines, dmaCapture.multiUsed, dmaCapture.multiData, dmaCapture.multiAddr
}

// DebugStats returns current debug counters for diagnosis
type DebugStats struct {
	IRQ0Count       uint32
	IRQ1Count       uint32
	TimingFIFOFills uint32
	DMAStarts       uint32
	BufferHits      uint32
	BufferMisses    uint32
	FramesComplete  uint32
	LastDataUsed    uint32
	LastScanlineNum uint32
}

// GetDebugStats returns current debug statistics
func GetDebugStats() DebugStats {
	return DebugStats{
		IRQ0Count:       debugCounters.irq0Count.Get(),
		IRQ1Count:       debugCounters.irq1Count.Get(),
		TimingFIFOFills: debugCounters.timingFIFOFills.Get(),
		DMAStarts:       debugCounters.dmaStarts.Get(),
		BufferHits:      debugCounters.bufferHits.Get(),
		BufferMisses:    debugCounters.bufferMisses.Get(),
		FramesComplete:  debugCounters.framesComplete.Get(),
		LastDataUsed:    debugCounters.lastDataUsed.Get(),
		LastScanlineNum: debugCounters.lastScanlineNum.Get(),
	}
}

// ResetDebugStats clears all debug counters
func ResetDebugStats() {
	debugCounters.irq0Count.Set(0)
	debugCounters.irq1Count.Set(0)
	debugCounters.timingFIFOFills.Set(0)
	debugCounters.dmaStarts.Set(0)
	debugCounters.bufferHits.Set(0)
	debugCounters.bufferMisses.Set(0)
	debugCounters.framesComplete.Set(0)
}

// BufferCounts contains counts of buffers in each list for debugging
type BufferCounts struct {
	Free      int
	Generated int
	InUse     int
	Current   bool   // true if currentBuffer is set
	YRepeat   uint16 // current yRepeatIndex
	YTarget   uint16 // current yRepeatTarget
	DmaAborts uint32 // count of DMA aborts
}

// GetBufferCounts returns the number of buffers in each list
// This helps diagnose buffer leaks or deadlocks
func GetBufferCounts() BufferCounts {
	state := interrupt.Disable()
	counts := BufferCounts{}
	for p := freeList; p != nil; p = p.next {
		counts.Free++
	}
	for p := generatedList; p != nil; p = p.next {
		counts.Generated++
	}
	for p := inUseList; p != nil; p = p.next {
		counts.InUse++
	}
	counts.Current = currentBuffer != nil
	counts.YRepeat = yRepeatIndex
	counts.YTarget = yRepeatTarget
	counts.DmaAborts = debugCounters.dmaAborts.Get()
	interrupt.Restore(state)
	return counts
}

// PIOStatus contains debug info about PIO state
type PIOStatus struct {
	ScanlineSMEnabled bool
	TimingSMEnabled   bool
	ScanlineFSTAT     uint32
	TimingFSTAT       uint32
	ScanlineAddr      uint32  // Current instruction address
	TimingAddr        uint32
	IRQRaw            uint32
}

// GetPIOStatus returns current PIO state for debugging
func GetPIOStatus() PIOStatus {
	// CTRL register bits: SM_ENABLE is at bits 0-3
	ctrl := rp.PIO0.CTRL.Get()
	fstat := rp.PIO0.FSTAT.Get()

	// Get SM addresses from SMx_ADDR registers
	scanlineAddr := rp.PIO0.SM0_ADDR.Get()
	timingAddr := rp.PIO0.SM3_ADDR.Get()

	return PIOStatus{
		ScanlineSMEnabled: (ctrl & (1 << ScanlineSM)) != 0,
		TimingSMEnabled:   (ctrl & (1 << TimingSM)) != 0,
		ScanlineFSTAT:     fstat,
		TimingFSTAT:       fstat,
		ScanlineAddr:      scanlineAddr,
		TimingAddr:        timingAddr,
		IRQRaw:            rp.PIO0.IRQ.Get(),
	}
}

// Internal scanline buffer with linked list support
type scanlineBufferInternal struct {
	core ScanlineBuffer
	next *scanlineBufferInternal
	data [MaxScanlineBufferWords]uint32
}

// DMA channel hardware registers
type dmaChannelHW struct {
	ReadAddr   volatile.Register32
	WriteAddr  volatile.Register32
	TransCount volatile.Register32
	CtrlTrig   volatile.Register32
	pad        [12]volatile.Register32 // Aliases and padding
}

//go:nosplit
func getDMAChannel(ch int) *dmaChannelHW {
	base := uintptr(0x50000000) + uintptr(ch)*0x40
	return (*dmaChannelHW)(unsafe.Pointer(base))
}

// DMA CHAN_ABORT register - writing a bit mask aborts those channels
var dmaAbortReg = (*volatile.Register32)(unsafe.Pointer(uintptr(0x50000444)))

// abortDMAChannel aborts the DMA channel and waits for it to complete
// This matches C scanvideo.c abort_all_dma_channels_assuming_no_irq_preemption
//go:nosplit
func abortDMAChannel(ch int) {
	// Write channel bit to ABORT register
	dmaAbortReg.Set(1 << ch)

	// Wait for channel to no longer be busy
	// On RP2040, we check CTRL_TRIG.BUSY bit (bit 24)
	dma := getDMAChannel(ch)
	for dma.CtrlTrig.Get()&(1<<24) != 0 {
		// tight loop
	}
}

// updateDMATransferState checks if previous DMA completed and aborts if needed
// This matches C scanvideo.c update_dma_transfer_state_irqs_enabled (NO_DMA_TRACKING path)
// The NO_DMA_TRACKING path only aborts when buffers_to_release is set
//go:nosplit
func updateDMATransferState(cancelIfNotComplete bool) int {
	if dmaInProgress == 0 {
		// No transfer in progress
		return 0
	}

	// Match C scanvideo.c NO_DMA_TRACKING path (lines 652-661):
	// Only abort if there are buffers to release
	if buffersToRelease > 0 {
		toRelease := int(buffersToRelease)
		buffersToRelease = 0
		dmaInProgress = 0
		// Abort the DMA channel
		abortDMAChannel(ScanlineDMAChannel)
		debugCounters.dmaAborts.Set(debugCounters.dmaAborts.Get() + 1)
		return toRelease
	}

	return 0
}

// Setup initializes the video system with the given mode
func Setup(mode *Mode) bool {
	return SetupWithTiming(mode, mode.DefaultTiming)
}

// SetupWithTiming initializes the video system with custom timing
func SetupWithTiming(mode *Mode, t *Timing) bool {
	println("Setup: starting...")
	videoMode = *mode
	timing = t

	if videoMode.YScaleDenom == 0 {
		videoMode.YScaleDenom = 1
	}

	println("Setup: init buffers...")
	// Initialize scanline buffers
	for i := 0; i < ScanlineBufferCount; i++ {
		scanlineBuffers[i].core.Data = scanlineBuffers[i].data[:]
		scanlineBuffers[i].core.DataMax = MaxScanlineBufferWords
		if i < ScanlineBufferCount-1 {
			scanlineBuffers[i].next = &scanlineBuffers[i+1]
		}
	}
	freeList = &scanlineBuffers[0]
	lastScanlineID = 0xFFFFFFFF

	// Setup missing scanline data (shows blue when buffer not ready)
	missingData[0] = uint32(OffsetColorRun) | (uint32(RGB565(0, 0, 255)) << 16)
	missingData[1] = uint32(mode.Width-3) | (uint32(OffsetRaw1P) << 16)
	missingData[2] = 0 | (uint32(OffsetEOLAlign) << 16)

	println("Setup: get PIO...")
	// Get PIO instance
	videoPIO = pio.PIO0

	println("Setup: enable DMA...")
	// Enable DMA
	rp.RESETS.RESET.ClearBits(rp.RESETS_RESET_DMA)
	for !rp.RESETS.RESET_DONE.HasBits(rp.RESETS_RESET_DONE_DMA) {
	}

	println("Setup: configure GPIO pins...")
	// Configure GPIO pins
	for i := uint8(0); i < ColorPinCount; i++ {
		pin := machine.Pin(uint8(ColorPinBase) + i)
		pin.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	}
	for i := uint8(0); i < 2; i++ {
		pin := machine.Pin(uint8(SyncPinBase) + i)
		pin.Configure(machine.PinConfig{Mode: videoPIO.PinMode()})
	}

	println("Setup: load scanline PIO program...")
	// Load and configure scanline PIO program
	scanlineProgram := BuildScanlineProgram(mode.XScale)
	var err error
	offset, err := videoPIO.AddProgram(scanlineProgram, 0) // Must be at offset 0
	if err != nil {
		println("Failed to load scanline program:", err.Error())
		return false
	}
	scanlineOffset = offset

	println("Setup: configure scanline SM...")
	scanlineSM := videoPIO.StateMachine(ScanlineSM)
	scanlineSM.TryClaim()
	ConfigureScanlineSM(videoPIO, scanlineSM, scanlineOffset)

	println("Setup: load timing PIO program...")
	// Load and configure timing PIO program
	timingProgram := BuildTimingProgram()
	offset, err = videoPIO.AddProgram(timingProgram, -1)
	if err != nil {
		println("Failed to load timing program:", err.Error())
		return false
	}
	timingOffset = offset

	println("Setup: configure timing SM...")
	timingSM := videoPIO.StateMachine(TimingSM)
	timingSM.TryClaim()
	ConfigureTimingSM(videoPIO, timingSM, timingOffset)

	println("Setup: configure DMA...")
	// Configure DMA for scanline data transfer
	setupDMA()

	println("Setup: init timing state...")
	// Initialize timing state
	initTimingState()

	displayEnabled.Set(1)

	// Initialize scanline tracking
	yRepeatTarget = uint16(videoMode.YScale)
	yRepeatIndex = 0
	nextScanlineID = 0
	lastScanlineID = 0xFFFFFFFF

	println("Setup: complete!")
	return true
}

// setupDMA configures DMA for scanline transfer
func setupDMA() {
	dma := getDMAChannel(ScanlineDMAChannel)

	// Get PIO TX FIFO address
	scanlineSM := videoPIO.StateMachine(ScanlineSM)
	txFifoAddr := uint32(uintptr(unsafe.Pointer(&scanlineSM.TxReg().Reg)))

	// Configure DMA control
	ctrl := uint32(0)
	ctrl |= 1 << 0   // Enable
	ctrl |= 1 << 1   // High priority
	ctrl |= 2 << 2   // 32-bit transfers
	ctrl |= 1 << 4   // Increment read address
	ctrl |= 0 << 15  // DREQ = PIO0_TX0 (for SM0)
	ctrl |= 1 << 21  // IRQ quiet (we'll trigger completion manually)

	dma.WriteAddr.Set(txFifoAddr)
	dma.CtrlTrig.Set(ctrl & ^uint32(1)) // Configure but don't enable yet

	// Enable DMA IRQ
	rp.DMA.INTE0.SetBits(1 << ScanlineDMAChannel)
}

// initTimingState sets up the timing state machine parameters
// This matches the C pico-extras scanvideo implementation exactly
func initTimingState() {
	timingState.vTotal = int32(timing.VTotal)
	timingState.vActive = int32(timing.VActive)
	timingState.vPulseStart = int32(timing.VActive + timing.VFrontPorch)
	timingState.vPulseEnd = timingState.vPulseStart + int32(timing.VPulse)

	// VSYNC polarity - bit 30 controls vsync pin (bit 1 of 3-bit pin output)
	// The vsync bit is bit 30 in the 32-bit word (shifted to position 1 of pins)
	// Matches C code EXACTLY:
	//   vsync_bits_pulse = timing->v_sync_polarity ? 0 : vsync_bit;
	//   vsync_bits_no_pulse = timing->v_sync_polarity ? vsync_bit : 0;
	vsyncBit := uint32(0x40000000) // Bit 30
	if timing.VSyncPolarity != 0 {
		// Active high: during pulse output 0, outside pulse output vsyncBit
		timingState.vsyncPulse = 0
		timingState.vsyncNoPulse = vsyncBit
	} else {
		// Active low: during pulse output vsyncBit, outside pulse output 0
		timingState.vsyncPulse = vsyncBit
		timingState.vsyncNoPulse = 0
	}

	// Calculate timing state values
	// Each timing word contains: instruction(16) | (cycles-3)(13) | pins(3)
	// Pin bits: bit 29 = hsync, bit 30 = vsync, bit 31 = DEN (display enable)

	// HSYNC polarity: 0 = active low, 1 = active high
	// During HSYNC pulse, we want the active state
	var hSyncPulse uint32    // Pin state during HSYNC pulse
	var hSyncNoPulse uint32  // Pin state outside HSYNC pulse
	if timing.HSyncPolarity == 0 {
		// Active low: pulse = 0, no pulse = 1
		hSyncPulse = 0
		hSyncNoPulse = 1
	} else {
		// Active high: pulse = 1, no pulse = 0
		hSyncPulse = 1
		hSyncNoPulse = 0
	}

	// DEN (Display Enable) bit - bit 31 (pin position 2)
	denBit := uint32(4) // 0b100 - bit 2 of the 3-bit pin output

	// Calculate horizontal timing components
	hBackPorch := int(timing.HTotal - timing.HActive - timing.HFrontPorch - timing.HPulse)
	hActiveAndFront := int(timing.HActive + timing.HFrontPorch)

	// State A: Start of line during HSYNC pulse (4 cycles)
	// Sets IRQ 0 (active scanline) or IRQ 1 (vblank)
	timingState.a = encodeTimingState(SetIRQ0, 4, hSyncPulse)
	timingState.aVblank = encodeTimingState(SetIRQ1, 4, hSyncPulse)

	// State B1: Rest of HSYNC pulse (h_pulse - 4 cycles)
	// Clears scanline IRQ (IRQ 4) - prepares for next scanline
	timingState.b1 = encodeTimingState(ClearIRQScanline, uint16(timing.HPulse-4), hSyncPulse)

	// State B2: Back porch (after HSYNC pulse)
	// Continue clearing scanline IRQ, HSYNC now inactive
	timingState.b2 = encodeTimingState(ClearIRQScanline, uint16(hBackPorch), hSyncNoPulse)

	// State C: Active display + front porch
	// Sets scanline IRQ (IRQ 4) to trigger pixel output
	// For active lines: enable DEN bit; for vblank: no DEN
	timingState.c = encodeTimingState(SetIRQScanline, uint16(hActiveAndFront), denBit|hSyncNoPulse)
	timingState.cVblank = encodeTimingState(ClearIRQScanline, uint16(hActiveAndFront), hSyncNoPulse)

	// Start with vblank states (frame starts in vblank)
	setupDMAStatesVblank()
	timingState.vsyncBits = timingState.vsyncNoPulse
}

func encodeTimingState(state int, cycles uint16, pins uint32) uint32 {
	// Format: instruction(16) | (cycles-3)(13) | pins(3)
	instr := TimingStateInstructions[state]
	return uint32(instr) | (uint32(cycles-3) << 16) | (pins << 29)
}

func setupDMAStatesVblank() {
	dmaStates[0] = timingState.aVblank
	dmaStates[1] = timingState.b1
	dmaStates[2] = timingState.b2
	dmaStates[3] = timingState.cVblank
}

func setupDMAStatesActive() {
	dmaStates[0] = timingState.a
	dmaStates[1] = timingState.b1
	dmaStates[2] = timingState.b2
	dmaStates[3] = timingState.c
}

// PIO interrupt source bits (matches C pis_* enum and RP2040 datasheet)
// These are bit positions in the IRQx_INTE register
const (
	pisInterrupt0       = 8  // PIO IRQ flag 0 (PIO_INTR_SM0_LSB)
	pisInterrupt1       = 9  // PIO IRQ flag 1 (PIO_INTR_SM1_LSB)
	pisSM0TxFIFONotFull = 4  // SM0 TX FIFO not full (PIO_INTR_SM0_TXNFULL_LSB)
	pisSM3TxFIFONotFull = 7  // SM3 TX FIFO not full (PIO_INTR_SM3_TXNFULL_LSB)
)

// TimingEnable starts or stops video timing
// This follows the C pico-extras implementation using hardware interrupts
func TimingEnable(enable bool) {
	println("TimingEnable:", enable)
	if enable && timingEnabled.Get() != 0 {
		return
	}
	if !enable && timingEnabled.Get() == 0 {
		return
	}

	// Get state machine references
	timingSM := videoPIO.StateMachine(TimingSM)
	scanlineSM := videoPIO.StateMachine(ScanlineSM)

	// Disable both SMs first
	timingSM.SetEnabled(false)
	scanlineSM.SetEnabled(false)

	// Restart clock dividers for both SMs to synchronize them
	// CLKDIV_RESTART bits are at [11:8] of CTRL register
	// SM mask: (1 << ScanlineSM) | (1 << TimingSM) = (1 << 0) | (1 << 3) = 0x09
	const clkdivRestartLSB = 8
	smMask := uint32((1 << ScanlineSM) | (1 << TimingSM))
	rp.PIO0.CTRL.SetBits(smMask << clkdivRestartLSB)

	if enable {
		// Initialize interrupts on first enable
		initInterrupts()

		println("TimingEnable: resetting state")
		// Reset timing state
		timingState.timingScanline = 0
		timingState.stateIndex = 0
		timingState.inVblank = true
		setupDMAStatesVblank()
		timingState.vsyncBits = timingState.vsyncNoPulse

		// Clear any pending IRQ flags
		videoPIO.ClearIRQ(0x0F)

		// Prime the timing FIFO with initial data
		topUpTimingFIFO()

		// Pre-fill scanline FIFO with blank data (matches working example)
		// This ensures the first scanline has data ready
		scanlineSM.TxPut(uint32(OffsetColorRun) | (0 << 16))                      // COLOR_RUN | BLACK
		scanlineSM.TxPut(uint32(videoMode.Width-3) | (uint32(OffsetRaw1P) << 16)) // count | RAW_1P
		scanlineSM.TxPut(0 | (uint32(OffsetEOLAlign) << 16))                      // BLACK | EOL_ALIGN

		// Force scanline SM to jump to entry point (wait IRQ instruction)
		scanlineSM.Exec(encodeJmp(scanlineOffset + OffsetEntryPoint))

		// Force timing SM to jump to entry point
		timingSM.Exec(encodeJmp(timingOffset))

		println("TimingEnable: setting up interrupts")
		// Setup hardware interrupts
		// Use lower priority than USB (which is at 0x00) so serial still works for debugging
		irqPIO0_0.SetPriority(0x80) // Lower than USB
		irqPIO0_1.SetPriority(0xC0) // Lower priority

		// Configure PIO interrupt routing
		// PIO0_IRQ_0: triggered by software IRQ flags 0 and 1
		// PIO0_IRQ_1: triggered by SM3 TX FIFO not full
		pioSetIRQ0SourceMask((1<<pisInterrupt0)|(1<<pisInterrupt1), true)
		pioSetIRQ1SourceEnabled(pisSM3TxFIFONotFull, true)

		println("TimingEnable: enabling SMs")
		// Enable both state machines
		timingSM.SetEnabled(true)
		scanlineSM.SetEnabled(true)

		println("TimingEnable: enabling IRQs")
		// Enable interrupts
		timingEnabled.Set(1)
		irqPIO0_0.Enable()
		irqPIO0_1.Enable()
		println("TimingEnable: done")
	} else {
		// Disable interrupts first
		irqPIO0_0.Disable()
		irqPIO0_1.Disable()

		// Disable PIO interrupt sources
		pioSetIRQ0SourceMask((1<<pisInterrupt0)|(1<<pisInterrupt1), false)
		pioSetIRQ1SourceEnabled(pisSM3TxFIFONotFull, false)

		timingEnabled.Set(0)
	}
}

// initInterrupts sets up the interrupt handlers (called once during first TimingEnable)
func initInterrupts() {
	if irqsInitialized {
		return
	}
	irqPIO0_0 = interrupt.New(rp.IRQ_PIO0_IRQ_0, isrPIO0_0)
	irqPIO0_1 = interrupt.New(rp.IRQ_PIO0_IRQ_1, isrPIO0_1)
	irqsInitialized = true
}

// pioSetIRQ0SourceMask enables/disables PIO interrupt sources for IRQ0
// Uses rp.PIO0.IRQ0_INTE register directly (offset 0x12C)
func pioSetIRQ0SourceMask(mask uint32, enable bool) {
	if enable {
		rp.PIO0.IRQ0_INTE.SetBits(mask)
	} else {
		rp.PIO0.IRQ0_INTE.ClearBits(mask)
	}
}

// pioSetIRQ1SourceEnabled enables/disables a specific PIO interrupt source for IRQ1
// Uses rp.PIO0.IRQ1_INTE register directly (offset 0x138)
func pioSetIRQ1SourceEnabled(source uint32, enable bool) {
	if enable {
		rp.PIO0.IRQ1_INTE.SetBits(1 << source)
	} else {
		rp.PIO0.IRQ1_INTE.ClearBits(1 << source)
	}
}

// encodeJmp creates a PIO JMP instruction to the given address
func encodeJmp(addr uint8) uint16 {
	// PIO JMP instruction: 000 00 000 AAAAA (where AAAAA is the address)
	return uint16(addr) & 0x1F
}

// isrPIO0_0 handles scanline IRQs (matches C isr_pio0_0)
// Called when PIO raises IRQ flag 0 (active scanline) or 1 (vblank)
// Uses direct register access to avoid calling non-nosplit methods
//
//go:nosplit
func isrPIO0_0(irq interrupt.Interrupt) {
	// Direct register access: PIO0 IRQ register at offset 0x030
	irqFlags := rp.PIO0.IRQ.Get()

	// Check for IRQ 0: active scanline start
	if irqFlags&1 != 0 {
		rp.PIO0.IRQ.Set(1) // Clear IRQ 0
		debugCounters.irq0Count.Set(debugCounters.irq0Count.Get() + 1)
		if displayEnabled.Get() != 0 {
			prepareForActiveScanline()
		}
	}

	// Check for IRQ 1: vblank scanline
	// C code clears both irq0 and irq1 for good measure: video_pio->irq = 3
	if irqFlags&2 != 0 {
		rp.PIO0.IRQ.Set(3) // Clear both for good measure like C code
		debugCounters.irq1Count.Set(debugCounters.irq1Count.Get() + 1)
		prepareForVblankScanline()
	}
}

// isrPIO0_1 handles timing FIFO (matches C isr_pio0_1)
// Called when timing SM's TX FIFO is not full
//
//go:nosplit
func isrPIO0_1(irq interrupt.Interrupt) {
	topUpTimingFIFO()
}

// topUpTimingFIFO keeps the timing SM FIFO fed
// Uses direct register access to avoid calling non-nosplit methods
//go:nosplit
func topUpTimingFIFO() {
	// PIO0 FSTAT register: bit (16 + SM) is TXFULL for each SM
	// TimingSM = 3, so bit 19 is TXFULL for timing SM
	const txFullBit = 1 << (16 + TimingSM)

	debugCounters.timingFIFOFills.Set(debugCounters.timingFIFOFills.Get() + 1)

	for rp.PIO0.FSTAT.Get()&txFullBit == 0 {
		// Write directly to TX FIFO
		// TXF3 is the TX FIFO for SM3 (TimingSM)
		rp.PIO0.TXF3.Set(dmaStates[timingState.stateIndex] | timingState.vsyncBits)

		timingState.stateIndex++
		if timingState.stateIndex >= 4 {
			timingState.stateIndex = 0
			timingState.timingScanline++

			// Handle vertical timing transitions
			if timingState.timingScanline >= timingState.vActive {
				if timingState.timingScanline >= timingState.vTotal {
					// Start of new frame
					timingState.timingScanline = 0
					setupDMAStatesActive()
					debugCounters.framesComplete.Set(debugCounters.framesComplete.Get() + 1)
				} else if timingState.timingScanline == timingState.vActive {
					// Start of vblank
					setupDMAStatesVblank()
				} else if timingState.timingScanline == timingState.vPulseStart {
					// VSYNC pulse start
					timingState.vsyncBits = timingState.vsyncPulse
				} else if timingState.timingScanline == timingState.vPulseEnd {
					// VSYNC pulse end
					timingState.vsyncBits = timingState.vsyncNoPulse
				}
			}
		}
	}
}

// PIO_WAIT_IRQ4 is the encoded instruction at offset 1 (wait irq 4)
// This is what the scanline SM should be executing when idle
const PIO_WAIT_IRQ4 = 0x20c4

// prepareForActiveScanline handles the start of an active display line
// Note: C code comment says this must complete DMA start within ~4.5µs
// Uses interrupt.Disable/Restore like C's spin_lock_blocking
//go:nosplit
func prepareForActiveScanline() {
	// Release any buffer that was pending from the previous scanline
	// This ensures DMA has finished reading from it before we release
	// (matches C scanvideo.c deferred release pattern)
	if pendingReleaseBuffer != nil {
		state := interrupt.Disable()
		releaseBuffer(pendingReleaseBuffer)
		pendingReleaseBuffer = nil
		interrupt.Restore(state)
	}

	// Try to latch a buffer for this scanline
	var buf *scanlineBufferInternal

	state := interrupt.Disable()
	// First, discard any old scanlines that are behind what we need
	// This prevents buffer starvation when render loop falls behind
	// Note: We check if nextScanlineID is AFTER the buffer's ID, meaning the buffer is old.
	// We do NOT use "nextScanlineID-1" because that causes unsigned wraparound when
	// nextScanlineID=0 at startup, incorrectly discarding ALL buffers including scanline 0.
	for generatedList != nil && isScanlineAfter(nextScanlineID, generatedList.core.ScanlineID) {
		// This scanline is old (strictly before what we need), discard it
		old := generatedList
		generatedList = generatedList.next
		if generatedList == nil {
			generatedTail = nil
		}
		// Return to free list
		old.next = freeList
		freeList = old
	}

	if currentBuffer == nil && generatedList != nil {
		// Check if the generated buffer matches what we need
		if generatedList.core.ScanlineID == nextScanlineID {
			buf = generatedList
			generatedList = generatedList.next
			if generatedList == nil {
				generatedTail = nil
			}
			buf.next = inUseList
			inUseList = buf
			currentBuffer = buf
			// NOTE: Removed yRepeatIndex reset - it was causing double-image
			// artifacts by making some scanlines display extra times.
		}
	} else {
		buf = currentBuffer
	}
	interrupt.Restore(state)

	// Start DMA transfer
	var data *uint32
	var count uint16

	wasCorrectScanline := buf != nil && buf.core.ScanlineID == nextScanlineID
	if wasCorrectScanline {
		data = &buf.core.Data[0]
		count = buf.core.DataUsed
		debugCounters.bufferHits.Set(debugCounters.bufferHits.Get() + 1)
		debugCounters.lastDataUsed.Set(uint32(count))
	} else {
		// Use missing scanline data
		data = &missingData[0]
		count = 3
		debugCounters.bufferMisses.Set(debugCounters.bufferMisses.Get() + 1)
	}

	scanlineNum := ScanlineNumber(nextScanlineID)
	debugCounters.lastScanlineNum.Set(uint32(scanlineNum))
	debugCounters.dmaStarts.Set(debugCounters.dmaStarts.Get() + 1)

	// Capture DMA buffer for debugging
	if dmaCapture.enabled && buf != nil {
		if dmaCapture.multiCapture {
			// Multi-line capture mode: capture consecutive scanlines starting from targetLine
			if scanlineNum >= dmaCapture.targetLine && dmaCapture.linesCaptured < 10 {
				idx := dmaCapture.linesCaptured
				dmaCapture.multiLines[idx] = scanlineNum
				dmaCapture.multiUsed[idx] = count
				dmaCapture.multiAddr[idx] = uint32(uintptr(unsafe.Pointer(data))) // Capture DMA address
				for i := 0; i < 20 && i < int(count); i++ {
					dmaCapture.multiData[idx][i] = buf.core.Data[i]
				}
				dmaCapture.linesCaptured++
				if dmaCapture.linesCaptured >= 10 {
					dmaCapture.captured = true
					dmaCapture.enabled = false
				}
			}
		} else if !dmaCapture.captured && scanlineNum == dmaCapture.targetLine {
			// Single line capture mode
			dmaCapture.scanline = scanlineNum
			dmaCapture.dataUsed = count
			for i := 0; i < 20 && i < int(count); i++ {
				dmaCapture.words[i] = buf.core.Data[i]
			}
			dmaCapture.captured = true
			dmaCapture.enabled = false
		}
	}

	// VIDEO RECOVERY: Clear FIFO and reset SM if needed (matches C scanvideo.c lines 740-765)
	// This is critical for handling cases where the SM got stuck or has stale data
	recoverScanlineSM()

	// Configure and start DMA
	dma := getDMAChannel(ScanlineDMAChannel)
	dma.ReadAddr.Set(uint32(uintptr(unsafe.Pointer(data))))
	dma.TransCount.Set(uint32(count))
	dma.CtrlTrig.SetBits(1) // Enable

	// Update scanline tracking (matches C scanvideo.c lines 811-859)
	state = interrupt.Disable()
	timingState.inVblank = false

	yRepeatIndex += videoMode.YScaleDenom
	if yRepeatIndex >= yRepeatTarget {
		// Move to next scanline
		if wasCorrectScanline && buf != nil {
			// Don't release immediately - DMA is still reading from this buffer!
			// Mark for release at the START of the next scanline
			pendingReleaseBuffer = buf
		}
		yRepeatIndex -= yRepeatTarget
		nextScanlineID = scanlineIDAfter(nextScanlineID)
		currentBuffer = nil
	} else if !wasCorrectScanline {
		// Not at end of yscale, but wrong/missing scanline - clear currentBuffer
		// This matches C scanvideo.c lines 826-828: if we had a miss mid-yscale,
		// we should try to latch a new buffer on the next repeat instead of
		// reusing the stale one
		currentBuffer = nil
	}
	interrupt.Restore(state)
}

// recoverScanlineSM checks and recovers the scanline SM if it's in a bad state
// This matches the C scanvideo.c PICO_SCANVIDEO_ENABLE_VIDEO_RECOVERY code (lines 740-765)
//go:nosplit
func recoverScanlineSM() {
	// FSTAT bit 0 is TXEMPTY for SM0 (ScanlineSM)
	const txEmptyBit = 1 << ScanlineSM

	// Check if TX FIFO is not empty - indicates stale data from previous scanline
	if rp.PIO0.FSTAT.Get()&txEmptyBit == 0 {
		// FIFO not empty - clear it by toggling FJOIN bits in SHIFTCTRL
		// This is what pio_sm_clear_fifos does in the C SDK
		shiftctrl := rp.PIO0.SM0_SHIFTCTRL.Get()
		rp.PIO0.SM0_SHIFTCTRL.Set(shiftctrl ^ (0x3 << 30)) // Toggle FJOIN_TX and FJOIN_RX bits
		rp.PIO0.SM0_SHIFTCTRL.Set(shiftctrl)              // Restore original

		// Also clear the OSR in case there was partial data
		// Execute: out null, 32 (opcode 0x6060)
		rp.PIO0.SM0_INSTR.Set(0x6060)
	}

	// Check if SM is at the expected wait instruction (offset 1 = wait irq 4)
	// SM0_INSTR holds the currently executing instruction
	// SM0_ADDR holds the current program counter
	currentAddr := rp.PIO0.SM0_ADDR.Get()
	expectedWaitAddr := uint32(scanlineOffset + OffsetEntryPoint) // offset + 1

	if currentAddr != expectedWaitAddr {
		// SM is not at the wait instruction - force it back
		// First try executing a wait irq 4 instruction
		rp.PIO0.SM0_INSTR.Set(PIO_WAIT_IRQ4)

		// Check if SM is now stalled (waiting for IRQ)
		// EXECCTRL bit 31 is EXEC_STALLED
		if rp.PIO0.SM0_EXECCTRL.Get()&(1<<31) != 0 {
			// SM is stalled on our injected wait - check if it's at the right place
			// If not at wait+1 (the out pc instruction), jump to wait
			if rp.PIO0.SM0_ADDR.Get() != expectedWaitAddr+1 {
				// Jump to wait instruction
				rp.PIO0.SM0_INSTR.Set(uint32(scanlineOffset + OffsetEntryPoint))
			}
		} else {
			// SM is not stalled, so IRQ must have already fired
			// Jump to the instruction after wait (out pc, 16)
			rp.PIO0.SM0_INSTR.Set(uint32(scanlineOffset + OffsetEntryPoint + 1))
		}
	}
}

// prepareForVblankScanline handles vblank scanlines
//go:nosplit
func prepareForVblankScanline() {
	state := interrupt.Disable()
	if !timingState.inVblank {
		timingState.inVblank = true
		yRepeatIndex = 0

		// Wrap to next frame if needed
		if ScanlineNumber(nextScanlineID) != 0 {
			nextScanlineID = (uint32(FrameNumber(nextScanlineID)+1) << 16)
			yRepeatTarget = uint16(videoMode.YScale)
		}
	}
	interrupt.Restore(state)
}

// releaseBuffer returns a buffer to the free list
//go:nosplit
func releaseBuffer(buf *scanlineBufferInternal) {
	// Remove from in-use list
	if inUseList == buf {
		inUseList = buf.next
	} else {
		for p := inUseList; p != nil; p = p.next {
			if p.next == buf {
				p.next = buf.next
				break
			}
		}
	}

	// Add to free list
	buf.next = freeList
	freeList = buf
}

// FlushGeneratedBuffers moves all pre-generated scanline buffers back to the free list.
// This should be called during buffer swaps to ensure fresh content is rendered.
// Without this, pre-generated buffers may contain stale framebuffer content from
// before the swap, causing visual artifacts like "vibrating" text.
func FlushGeneratedBuffers() {
	state := interrupt.Disable()
	// Move all generated buffers back to free list
	for generatedList != nil {
		buf := generatedList
		generatedList = buf.next
		buf.next = freeList
		freeList = buf
	}
	generatedTail = nil
	// Set lastScanlineID to just before nextScanlineID so render loop
	// generates the correct next scanline.
	// For scanlineID format: high 16 bits = frame, low 16 bits = scanline
	scanline := nextScanlineID & 0xFFFF
	frame := nextScanlineID >> 16
	if scanline > 0 {
		// Same frame, previous scanline
		lastScanlineID = (frame << 16) | (scanline - 1)
	} else {
		// Scanline 0 - previous is last scanline of previous frame
		if frame > 0 {
			lastScanlineID = ((frame - 1) << 16) | uint32(videoMode.Height-1)
		} else {
			// Frame 0, scanline 0 - use a very old ID
			lastScanlineID = 0xFFFF0000 | uint32(videoMode.Height-1)
		}
	}
	interrupt.Restore(state)
}

// scanlineIDAfter returns the next scanline ID
func scanlineIDAfter(id uint32) uint32 {
	line := id & 0xFFFF
	if line < uint32(videoMode.Height-1) {
		return id + 1
	}
	return (id & 0xFFFF0000) + 0x10000 // Next frame, scanline 0
}

// BeginScanlineGeneration acquires a buffer to fill with scanline data
func BeginScanlineGeneration(block bool) *ScanlineBuffer {
	for {
		state := interrupt.Disable()
		buf := freeList
		if buf != nil {
			freeList = buf.next
			buf.next = nil

			// Assign scanline ID
			scanlineID := nextScanlineID
			if !isScanlineAfter(scanlineID, lastScanlineID) {
				scanlineID = scanlineIDAfter(lastScanlineID)
			}
			buf.core.ScanlineID = scanlineID
			lastScanlineID = scanlineID
		}
		interrupt.Restore(state)

		if buf != nil {
			return &buf.core
		}

		if !block {
			return nil
		}

		// Yield while waiting for a buffer to become available
		runtime.Gosched()
	}
}

// EndScanlineGeneration releases a filled scanline buffer
func EndScanlineGeneration(buf *ScanlineBuffer) {
	internal := (*scanlineBufferInternal)(unsafe.Pointer(uintptr(unsafe.Pointer(buf)) - unsafe.Offsetof(scanlineBufferInternal{}.core)))

	state := interrupt.Disable()
	// Add to generated list (sorted by scanline ID)
	if generatedList == nil || !isScanlineAfter(buf.ScanlineID, generatedTail.core.ScanlineID) {
		// Add at end (most common case)
		if generatedTail != nil {
			generatedTail.next = internal
		} else {
			generatedList = internal
		}
		generatedTail = internal
	} else {
		// Insert in sorted order
		var prev *scanlineBufferInternal
		for p := generatedList; p != nil; p = p.next {
			if !isScanlineAfter(buf.ScanlineID, p.core.ScanlineID) {
				break
			}
			prev = p
		}
		if prev == nil {
			internal.next = generatedList
			generatedList = internal
		} else {
			internal.next = prev.next
			prev.next = internal
		}
	}
	interrupt.Restore(state)
}

func isScanlineAfter(id1, id2 uint32) bool {
	return int32(id1-id2) > 0
}

// InVblank returns true if currently in vertical blanking interval
func InVblank() bool {
	return timingState.inVblank
}

// WaitForVblank blocks until vblank begins
func WaitForVblank() {
	for !InVblank() {
		// Yield to other goroutines while waiting
		runtime.Gosched()
	}
}

// WaitForFrame waits for a complete frame to be displayed
// This is similar to the C gvga_sync() function - it ensures that
// all content scanlines for the current frame have been rendered
// before returning. This is safer than WaitForVblank alone because
// it guarantees the render loop has finished reading ShowFrame.
func WaitForFrame() {
	startFrame := debugCounters.framesComplete.Get()
	for debugCounters.framesComplete.Get() == startFrame {
		runtime.Gosched()
	}
}

// GetMode returns the current video mode
func GetMode() Mode {
	return videoMode
}

// GetNextScanlineID returns the next scanline ID to be displayed
func GetNextScanlineID() uint32 {
	return nextScanlineID
}

// GetProgramOffsets returns the loaded PIO program offsets for debugging
func GetProgramOffsets() (scanline, timing uint8) {
	return scanlineOffset, timingOffset
}
