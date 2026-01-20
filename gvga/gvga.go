package gvga

import (
	"machine"
	"runtime/volatile"
	"unsafe"

	pio "github.com/tinygo-org/pio/rp2-pio"
)

// GPIO registers for fast RGB output
const (
	SIO_BASE     = 0xd0000000
	GPIO_OUT     = SIO_BASE + 0x010
	GPIO_OUT_SET = SIO_BASE + 0x014
	GPIO_OUT_CLR = SIO_BASE + 0x018
	GPIO_OE      = SIO_BASE + 0x020
)

var (
	gpioOut    = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OUT)))
	gpioOutSet = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OUT_SET)))
	gpioOutClr = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OUT_CLR)))
	gpioOE     = (*volatile.Register32)(unsafe.Pointer(uintptr(GPIO_OE)))
)

const rgbMask = uint32(0xFFFF) // GPIO0-15

// PIO state machine for HSYNC
var (
	Pio         *pio.PIO
	hsyncSM     pio.StateMachine
	hsyncOffset uint8
)

// Init creates and initializes a GVga context
func Init(width, height uint16, bits int, doubleBuffer, interlaced bool, userData interface{}) *GVga {
	g := &GVga{
		Width:    width,
		Height:   height,
		Mode:     ModeBitmap,
		UserData: userData,
	}

	// Handle text mode (negative bits)
	if bits < 0 {
		g.Bits = 1
		g.Mode = ModeText
	} else {
		g.Bits = uint16(bits)
	}

	if interlaced {
		g.Mode |= ModeInterlaced
	}
	if doubleBuffer {
		g.Mode |= ModeDoubleBuffered
	}

	// Calculate derived values
	g.Colors = 1 << g.Bits
	g.PixelsPerByte = 8 / g.Bits
	g.RowBytes = width / g.PixelsPerByte
	g.FrameBytes = uint32(g.RowBytes) * uint32(height)

	// Text mode dimensions
	g.Rows = height / 8
	g.Cols = width / 8

	// Scaling for centering
	g.Multiplier = (FrameHeight + 1) / height
	vgaHeight := FrameHeight / g.Multiplier
	g.HeaderRows = (vgaHeight - height) / 2

	// Allocate frame buffers
	frameSize := g.FrameBytes
	if g.Mode&ModeText != 0 {
		frameSize /= 8
	}

	g.ShowFrame = make([]byte, frameSize)
	if doubleBuffer {
		g.DrawFrame = make([]byte, frameSize)
	} else {
		g.DrawFrame = g.ShowFrame
	}

	// Allocate and set default palette
	g.Palette = make([]Color, g.Colors)
	switch g.Bits {
	case 1:
		g.SetPalette(DefaultPalette16[:2], 0, 2)
	case 2:
		g.SetPalette(DefaultPalette16[:4], 0, 4)
	case 4:
		g.SetPalette(DefaultPalette16, 0, 16)
	case 8:
		// Set default 256 color palette
		for i := 0; i < 256; i++ {
			red := ((i & 0xE0) >> 5) + 1   // 3 bits
			green := ((i & 0x1C) >> 2) + 1 // 3 bits
			blue := (i & 0x03) + 1         // 2 bits
			g.Palette[i] = RGB5(uint8(red*4-1), uint8(green*4-1), uint8(blue*8-1))
		}
		g.SetPalette(DefaultPalette16, 0, 16)
	}

	// Set default font
	g.Font = DefaultFont

	return g
}

// Start begins VGA output
func (g *GVga) Start() {
	// Configure sync pins
	PinHSYNC.Configure(machine.PinConfig{Mode: machine.PinOutput})
	PinVSYNC.Configure(machine.PinConfig{Mode: machine.PinOutput})
	PinHSYNC.High()
	PinVSYNC.High()

	// Configure RGB pins
	for pin := PinRGBBase; pin < PinRGBBase+PinRGBCount; pin++ {
		pin.Configure(machine.PinConfig{Mode: machine.PinOutput})
		pin.Low()
	}

	// Build and load PIO program for HSYNC
	g.initPIO()

	// Start the render loop
	g.running = true
	g.renderLoop()
}

// Stop stops VGA output
func (g *GVga) Stop() {
	g.running = false
}

// Sync waits for the current frame to finish displaying
func (g *GVga) Sync() {
	// Wait for frame to complete (simple polling approach)
	currentFrame := g.frameCount
	for g.frameCount == currentFrame && g.running {
		// Busy wait
	}
}

// Swap swaps draw and show buffers (for double buffering)
func (g *GVga) Swap(copy bool) {
	if g.Mode&ModeDoubleBuffered == 0 {
		return
	}
	if copy && g.DrawFrame != nil && g.ShowFrame != nil {
		for i := range g.ShowFrame {
			g.ShowFrame[i] = g.DrawFrame[i]
		}
	}
	g.DrawFrame, g.ShowFrame = g.ShowFrame, g.DrawFrame
}

// initPIO sets up the PIO state machine for HSYNC generation
func (g *GVga) initPIO() {
	asm := pio.AssemblerV0{}

	// HSYNC program: generates HSYNC pulse and IRQ for synchronization
	// High for ~704 cycles (active 640 + front 16 + back 48)
	// Low for 96 cycles (sync pulse)
	hsyncProgram := []uint16{
		asm.Pull(false, true).Encode(),                  // 0: pull block
		asm.Mov(pio.MovDestX, pio.MovSrcOSR).Encode(),   // 1: mov x, osr
		asm.Jmp(pio.JmpXNZeroDec, 2).Encode(),           // 2: jmp x-- 2
		asm.Set(pio.SetDestPins, 0).Delay(31).Encode(),  // 3: set pins, 0 [31]
		asm.Set(pio.SetDestPins, 0).Delay(31).Encode(),  // 4: set pins, 0 [31]
		asm.Set(pio.SetDestPins, 0).Delay(30).Encode(),  // 5: set pins, 0 [30]
		asm.IRQSet(false, 0).Encode(),                   // 6: irq 0
		asm.Set(pio.SetDestPins, 1).Encode(),            // 7: set pins, 1
		asm.Jmp(pio.JmpAlways, 1).Encode(),              // 8: jmp 1 (wrap back)
	}

	Pio = pio.PIO0
	var err error
	offset, err := Pio.AddProgram(hsyncProgram, -1)
	if err != nil {
		println("Failed to load PIO program:", err.Error())
		return
	}
	hsyncOffset = uint8(offset)

	hsyncSM = Pio.StateMachine(0)
	hsyncSM.TryClaim()

	cfg := pio.DefaultStateMachineConfig()
	cfg.SetSetPins(PinHSYNC, 1)

	// Clock divider: 125 MHz / 25 MHz = 5 for pixel clock
	cfg.SetClkDivIntFrac(5, 0)

	// Configure HSYNC pin for PIO
	PinHSYNC.Configure(machine.PinConfig{Mode: Pio.PinMode()})
	hsyncSM.SetPindirsConsecutive(PinHSYNC, 1, true)

	hsyncSM.Init(offset, cfg)

	// Push the high period count (calibrated for ~31.468 kHz HSYNC)
	highCount := uint32(1172)
	hsyncSM.TxPut(highCount)

	hsyncSM.SetEnabled(true)
}

// renderLoop is the main VGA rendering loop
func (g *GVga) renderLoop() {
	for g.running {
		// Wait for PIO IRQ 0 (end of sync pulse)
		for Pio.GetIRQ()&1 == 0 {
			// Busy wait for IRQ
		}
		Pio.ClearIRQ(1) // Clear IRQ 0

		// Back porch delay (~48 pixels at 25MHz = ~1.9µs)
		for i := 0; i < 60; i++ {
			_ = gpioOut.Get()
		}

		// Determine what to output on this line
		if g.lineCount < g.HeaderRows {
			// Top border - output border color
			g.renderBlankLine(g.BorderColors[BorderTop])
		} else if g.lineCount < g.HeaderRows+g.Height {
			// Active video region
			scanline := g.lineCount - g.HeaderRows
			g.renderScanline(int(scanline))
		} else {
			// Bottom border - output border color
			g.renderBlankLine(g.BorderColors[BorderBottom])
		}

		// Front porch - clear RGB
		gpioOutClr.Set(rgbMask)

		g.lineCount++

		// Handle VSYNC
		if g.lineCount == VSyncStart {
			PinVSYNC.Low()
		} else if g.lineCount == VSyncEnd {
			PinVSYNC.High()
		} else if g.lineCount >= VTotal {
			g.lineCount = 0
			g.frameCount++
		}
	}
}

// renderBlankLine outputs a solid color line
func (g *GVga) renderBlankLine(color Color) {
	// Output solid color for the full line width
	gpioOut.Set(uint32(color))
	for i := 0; i < 800; i++ { // ~640 pixels worth of delay
		_ = gpioOut.Get()
	}
}

// renderScanline renders one line from the frame buffer
func (g *GVga) renderScanline(scanline int) {
	switch g.Bits {
	case 1:
		g.renderScanline1BPP(scanline)
	case 2:
		g.renderScanline2BPP(scanline)
	case 4:
		g.renderScanline4BPP(scanline)
	case 8:
		g.renderScanline8BPP(scanline)
	}
}

// renderScanline1BPP renders a 1bpp scanline
func (g *GVga) renderScanline1BPP(scanline int) {
	// Calculate row offset in frame buffer
	rowOffset := scanline * int(g.Width) / 8
	if g.Mode&ModeText != 0 {
		// Text mode: lookup character and font data
		charRow := scanline / 8
		fontLine := scanline % 8
		rowOffset = charRow * int(g.Cols)

		for col := 0; col < int(g.Cols); col++ {
			charIdx := g.ShowFrame[rowOffset+col]
			fontByte := g.Font.Data[int(charIdx)*8+fontLine]

			// Output 8 pixels from font byte
			colors := g.PaletteBuf[int(fontByte)*8:]
			for i := 0; i < 8; i++ {
				gpioOut.Set(uint32(colors[i]))
				// Small delay for pixel timing
				_ = gpioOut.Get()
				_ = gpioOut.Get()
				_ = gpioOut.Get()
			}
		}
	} else {
		// Bitmap mode
		for byteIdx := 0; byteIdx < int(g.Width)/8; byteIdx++ {
			b := g.ShowFrame[rowOffset+byteIdx]
			colors := g.PaletteBuf[int(b)*8:]
			for i := 0; i < 8; i++ {
				gpioOut.Set(uint32(colors[i]))
				// Small delay for pixel timing
				_ = gpioOut.Get()
				_ = gpioOut.Get()
				_ = gpioOut.Get()
			}
		}
	}
}

// renderScanline2BPP renders a 2bpp scanline
func (g *GVga) renderScanline2BPP(scanline int) {
	rowOffset := scanline * int(g.Width) / 4
	for byteIdx := 0; byteIdx < int(g.Width)/4; byteIdx++ {
		b := g.ShowFrame[rowOffset+byteIdx]
		colors := g.PaletteBuf[int(b)*4:]
		for i := 0; i < 4; i++ {
			gpioOut.Set(uint32(colors[i]))
			// Delay for 2 pixel widths at this color depth
			for j := 0; j < 6; j++ {
				_ = gpioOut.Get()
			}
		}
	}
}

// renderScanline4BPP renders a 4bpp scanline
func (g *GVga) renderScanline4BPP(scanline int) {
	rowOffset := scanline * int(g.Width) / 2
	for byteIdx := 0; byteIdx < int(g.Width)/2; byteIdx++ {
		b := g.ShowFrame[rowOffset+byteIdx]
		colors := g.PaletteBuf[int(b)*2:]
		for i := 0; i < 2; i++ {
			gpioOut.Set(uint32(colors[i]))
			// Delay for 4 pixel widths
			for j := 0; j < 12; j++ {
				_ = gpioOut.Get()
			}
		}
	}
}

// renderScanline8BPP renders an 8bpp scanline
func (g *GVga) renderScanline8BPP(scanline int) {
	rowOffset := scanline * int(g.Width)
	for x := 0; x < int(g.Width); x++ {
		colorIdx := g.ShowFrame[rowOffset+x]
		gpioOut.Set(uint32(g.Palette[colorIdx]))
		// Small delay for pixel timing
		_ = gpioOut.Get()
		_ = gpioOut.Get()
		_ = gpioOut.Get()
	}
}

// Destroy frees resources (for completeness, though Go has GC)
func (g *GVga) Destroy() {
	g.Stop()
	g.DrawFrame = nil
	g.ShowFrame = nil
	g.Palette = nil
	g.PaletteBuf = nil
}
