// Package scanvideo provides VGA video output for RP2040 using PIO and DMA
// This is a TinyGo port of the pico-extras scanvideo library
package scanvideo

import (
	"machine"
)

// Pin configuration - Pimoroni Pico VGA Demo Base
const (
	ColorPinBase = machine.GPIO0  // RGB565 pins 0-15
	ColorPinCount = 16
	SyncPinBase  = machine.GPIO16 // HSYNC=16, VSYNC=17
)

// Timing defines VGA signal timing parameters
type Timing struct {
	ClockFreq uint32 // Pixel clock frequency in Hz

	HActive     uint16 // Horizontal active pixels
	VActive     uint16 // Vertical active lines

	HFrontPorch uint16 // Horizontal front porch in pixels
	HPulse      uint16 // Horizontal sync pulse width
	HTotal      uint16 // Total horizontal pixels per line
	HSyncPolarity uint8 // 0 = active low, 1 = active high

	VFrontPorch uint16 // Vertical front porch in lines
	VPulse      uint16 // Vertical sync pulse width
	VTotal      uint16 // Total vertical lines per frame
	VSyncPolarity uint8 // 0 = active low, 1 = active high

	EnableClock   uint8 // Enable pixel clock output
	ClockPolarity uint8 // Clock polarity
	EnableDEN     uint8 // Enable data enable signal
}

// Mode defines a video display mode
type Mode struct {
	DefaultTiming *Timing
	Width         uint16 // Logical width
	Height        uint16 // Logical height
	XScale        uint8  // Horizontal scale factor (1 = no scale)
	YScale        uint16 // Vertical scale factor (1 = no scale)
	YScaleDenom   uint16 // Y scale denominator for fractional scaling
}

// ScanlineBuffer holds pixel data for one scanline
type ScanlineBuffer struct {
	ScanlineID uint32   // Frame number (high 16 bits) | scanline number (low 16 bits)
	Data       []uint32 // Pixel data in composable format
	DataUsed   uint16   // Number of words actually used
	DataMax    uint16   // Maximum capacity
	Status     uint8    // SCANLINE_OK, SCANLINE_ERROR, SCANLINE_SKIPPED
	UserData   uintptr  // User-defined data
}

// Scanline status values
const (
	ScanlineOK      = 1
	ScanlineError   = 2
	ScanlineSkipped = 3
)

// Composable scanline commands - these are jump offsets into the PIO program
// The actual values depend on where instructions are loaded
const (
	ComposableColorRun     = 5  // Color run: | cmd | color | count-3 |
	ComposableRawRun       = 8  // Raw run: | cmd | color | n | <n+2 colors> |
	ComposableRaw1P        = 12 // Single pixel: | cmd | color |
	ComposableRaw2P        = 14 // Two pixels: | cmd | color | color |
	ComposableEOLAlign     = 3  // End of line (aligned)
	ComposableEOLSkipAlign = 0  // End of line skip (aligned)
)

// Standard VGA timing definitions
var (
	// VGA 640x480 @ 60Hz - standard timing
	Timing640x480_60 = Timing{
		ClockFreq:     25000000,
		HActive:       640,
		VActive:       480,
		HFrontPorch:   16,
		HPulse:        96,
		HTotal:        800,
		HSyncPolarity: 0, // Active low
		VFrontPorch:   10,
		VPulse:        2,
		VTotal:        525,
		VSyncPolarity: 0, // Active low
		EnableClock:   0,
		ClockPolarity: 0,
		EnableDEN:     0,
	}
)

// Standard video modes
var (
	Mode160x120_60 = Mode{
		DefaultTiming: &Timing640x480_60,
		Width:         160,
		Height:        120,
		XScale:        4,
		YScale:        4,
	}

	Mode320x240_60 = Mode{
		DefaultTiming: &Timing640x480_60,
		Width:         320,
		Height:        240,
		XScale:        2,
		YScale:        2,
	}

	Mode640x480_60 = Mode{
		DefaultTiming: &Timing640x480_60,
		Width:         640,
		Height:        480,
		XScale:        1,
		YScale:        1,
	}
)

// FrameNumber extracts the frame number from a scanline ID
func FrameNumber(scanlineID uint32) uint16 {
	return uint16(scanlineID >> 16)
}

// ScanlineNumber extracts the scanline number from a scanline ID
func ScanlineNumber(scanlineID uint32) uint16 {
	return uint16(scanlineID & 0xFFFF)
}

// RGB565 creates a 16-bit RGB565 color from 8-bit components
func RGB565(r, g, b uint8) uint16 {
	return uint16(b>>3)<<11 | uint16(g>>2)<<5 | uint16(r>>3)
}

// RGB565_5bit creates a 16-bit RGB565 color from 5-bit components
func RGB565_5bit(r, g, b uint8) uint16 {
	return uint16(b)<<11 | uint16(g)<<6 | uint16(r)
}
