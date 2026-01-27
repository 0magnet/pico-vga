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
// Must match the offsets from scanvideo.pio exactly
// See pio.go for the authoritative values (COMPOSABLE_* constants)

// Standard VGA timing definitions (matches C pico-extras vga_modes.c)
var (
	// VGA 640x480 @ 60Hz - standard timing (non-48MHz mode)
	// From C: vga_timing_640x480_60_default
	Timing640x480_60 = Timing{
		ClockFreq:     25000000, // 25 MHz pixel clock
		HActive:       640,
		VActive:       480,
		HFrontPorch:   16,
		HPulse:        96,       // Standard: 96 pixels
		HTotal:        800,
		HSyncPolarity: 0,        // Active low (standard VGA)
		VFrontPorch:   10,       // Standard: 10 lines
		VPulse:        2,
		VTotal:        525,      // Standard: 525 lines
		VSyncPolarity: 0,        // Active low (standard VGA)
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

// RGB565 creates a 16-bit color from 8-bit components
// Format: bits 0-4=Red, 6-10=Green, 11-15=Blue (bit 5 unused)
// Matches C GVGA_COLOR: (b<<11)|(g<<6)|(r<<0)
func RGB565(r, g, b uint8) uint16 {
	return uint16(r>>3) | (uint16(g>>3) << 6) | (uint16(b>>3) << 11)
}

// RGB565_5bit creates a 16-bit color from 5-bit R,G,B components
// Matches C GVGA_COLOR: (b<<11)|(g<<6)|(r<<0)
func RGB565_5bit(r, g, b uint8) uint16 {
	return uint16(r) | (uint16(g) << 6) | (uint16(b) << 11)
}
