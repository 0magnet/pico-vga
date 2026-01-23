package scanvideo

import (
	"device/rp"
	"machine"
	"runtime"
	"runtime/volatile"
	"sync"
	"unsafe"

	pio "github.com/tinygo-org/pio/rp2-pio"
)

// Configuration constants
const (
	ScanlineBufferCount    = 8   // Number of scanline buffers
	MaxScanlineBufferWords = 320 // Max words per scanline buffer (enough for 640 pixels)

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

	// Missing scanline data (blue line shown when buffer not ready)
	missingData [4]uint32

	// Synchronization (safe in goroutines, was problematic in interrupt handlers)
	stateLock sync.Mutex

	// Video enabled flags (use volatile for goroutine-safe access)
	timingEnabled  volatile.Register32
	displayEnabled volatile.Register32

	// Video loop control
	videoLoopRunning volatile.Register32
)

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

func getDMAChannel(ch int) *dmaChannelHW {
	base := uintptr(0x50000000) + uintptr(ch)*0x40
	return (*dmaChannelHW)(unsafe.Pointer(base))
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

// TimingEnable starts or stops video timing
// This follows the same sequence as the C pico-extras implementation
// Uses polling in a goroutine (like working color_run example) instead of
// hardware interrupts to avoid mutex issues in interrupt context
func TimingEnable(enable bool) {
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

	if enable {
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
		// This ensures it starts waiting for the timing IRQ
		scanlineSM.Exec(encodeJmp(scanlineOffset + OffsetEntryPoint))

		// Force timing SM to jump to entry point
		timingSM.Exec(encodeJmp(timingOffset))

		// Enable both state machines
		timingSM.SetEnabled(true)
		scanlineSM.SetEnabled(true)

		// Start the video loop goroutine
		timingEnabled.Set(1)
		videoLoopRunning.Set(1)
		go videoLoop()
	} else {
		// Stop the video loop
		timingEnabled.Set(0)
		// Wait for video loop to stop
		for videoLoopRunning.Get() != 0 {
			// busy wait
		}
	}
}

// encodeJmp creates a PIO JMP instruction to the given address
func encodeJmp(addr uint8) uint16 {
	// PIO JMP instruction: 000 00 000 AAAAA (where AAAAA is the address)
	return uint16(addr) & 0x1F
}

// videoLoop is the main video processing loop running in a goroutine
// This uses polling instead of hardware interrupts (same pattern as working color_run example)
func videoLoop() {
	// Give render goroutine time to pre-generate some scanlines
	for i := 0; i < 100; i++ {
		runtime.Gosched()
	}

	for timingEnabled.Get() != 0 {
		// Keep timing FIFO fed
		topUpTimingFIFO()

		// Poll for PIO IRQ flags
		irqFlags := videoPIO.GetIRQ()

		// IRQ 0: Active scanline start
		if irqFlags&1 != 0 {
			videoPIO.ClearIRQ(1)
			if displayEnabled.Get() != 0 {
				prepareForActiveScanline()
			}
			// Yield after each scanline to let render goroutine run
			runtime.Gosched()
		}

		// IRQ 1: Vblank scanline
		if irqFlags&2 != 0 {
			videoPIO.ClearIRQ(2)
			prepareForVblankScanline()
			// Yield during vblank to let render goroutine generate scanlines
			runtime.Gosched()
		}
	}
	videoLoopRunning.Set(0)
}

// topUpTimingFIFO keeps the timing SM FIFO fed
//go:nosplit
func topUpTimingFIFO() {
	timingSM := videoPIO.StateMachine(TimingSM)

	for !timingSM.IsTxFIFOFull() {
		// Push next timing state
		timingSM.TxPut(dmaStates[timingState.stateIndex] | timingState.vsyncBits)

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

// prepareForActiveScanline handles the start of an active display line
//go:nosplit
func prepareForActiveScanline() {
	// Try to latch a buffer for this scanline
	var buf *scanlineBufferInternal

	stateLock.Lock()
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
		}
	} else {
		buf = currentBuffer
	}
	stateLock.Unlock()

	// Start DMA transfer
	var data *uint32
	var count uint16

	if buf != nil && buf.core.ScanlineID == nextScanlineID {
		data = &buf.core.Data[0]
		count = buf.core.DataUsed
	} else {
		// Use missing scanline data
		data = &missingData[0]
		count = 3
	}

	// Configure and start DMA
	dma := getDMAChannel(ScanlineDMAChannel)
	dma.ReadAddr.Set(uint32(uintptr(unsafe.Pointer(data))))
	dma.TransCount.Set(uint32(count))
	dma.CtrlTrig.SetBits(1) // Enable

	// Update scanline tracking
	stateLock.Lock()
	timingState.inVblank = false

	yRepeatIndex += videoMode.YScaleDenom
	if yRepeatIndex >= yRepeatTarget {
		// Move to next scanline
		if buf != nil && buf.core.ScanlineID == nextScanlineID {
			// Release the buffer
			releaseBuffer(buf)
		}
		yRepeatIndex -= yRepeatTarget
		nextScanlineID = scanlineIDAfter(nextScanlineID)
		currentBuffer = nil
	}
	stateLock.Unlock()
}

// prepareForVblankScanline handles vblank scanlines
//go:nosplit
func prepareForVblankScanline() {
	stateLock.Lock()
	if !timingState.inVblank {
		timingState.inVblank = true
		yRepeatIndex = 0

		// Wrap to next frame if needed
		if ScanlineNumber(nextScanlineID) != 0 {
			nextScanlineID = (uint32(FrameNumber(nextScanlineID)+1) << 16)
			yRepeatTarget = uint16(videoMode.YScale)
		}
	}
	stateLock.Unlock()
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
		stateLock.Lock()
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
		stateLock.Unlock()

		if buf != nil {
			return &buf.core
		}

		if !block {
			return nil
		}

		// Wait for a buffer to become available
		// In a real implementation, we'd use WFE here
	}
}

// EndScanlineGeneration releases a filled scanline buffer
func EndScanlineGeneration(buf *ScanlineBuffer) {
	internal := (*scanlineBufferInternal)(unsafe.Pointer(uintptr(unsafe.Pointer(buf)) - unsafe.Offsetof(scanlineBufferInternal{}.core)))

	stateLock.Lock()
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
	stateLock.Unlock()
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
		// Busy wait - in a real implementation we'd use WFE
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
