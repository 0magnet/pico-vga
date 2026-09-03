// Package gvga provides VGA graphics for RP2040/Pico
// Ported from GVga C library by drfrancintosh
package gvga

import (
	"machine"

	"github.com/0magnet/pico-vga/scanvideo"
)

// Color is 16-bit RGB format matching C GVGA_COLOR macro
// Format: bits 0-4=Red, 6-10=Green, 11-15=Blue (note: bit 5 unused)
// Matches C: #define GVGA_COLOR(r,g,b) (((b)<<11u)|((g)<<6)|((r)<<0))
type Color uint16

// RGB creates a Color from 5-bit R, 5-bit G, 5-bit B values
// Matches C: GVGA_COLOR(r,g,b) = (b<<11)|(g<<6)|(r<<0)
func RGB(r, g, b uint8) Color {
	return Color(uint16(r&0x1F) | (uint16(g&0x1F) << 6) | (uint16(b&0x1F) << 11))
}

// RGB5 creates a Color from 5-bit R, G, B values
// Matches C: GVGA_COLOR(r,g,b) = (b<<11)|(g<<6)|(r<<0)
func RGB5(r, g, b uint8) Color {
	return Color(uint16(r&0x1F) | (uint16(g&0x1F) << 6) | (uint16(b&0x1F) << 11))
}

// Standard colors - matches C GVGA_COLOR format: bits 0-4=Red, 6-10=Green, 11-15=Blue
// GVGA_COLOR(r,g,b) = (b<<11)|(g<<6)|(r<<0)
const (
	Black   Color = 0x0000                   // GVGA_COLOR(0, 0, 0)
	Red     Color = 0x001F                   // GVGA_COLOR(31, 0, 0) = 31 << 0
	Green   Color = 0x07C0                   // GVGA_COLOR(0, 31, 0) = 31 << 6
	Blue    Color = 0xF800                   // GVGA_COLOR(0, 0, 31) = 31 << 11
	Cyan    Color = 0xFFC0                   // GVGA_COLOR(0, 31, 31) = Green | Blue
	Magenta Color = 0xF81F                   // GVGA_COLOR(31, 0, 31) = Red | Blue
	Yellow  Color = 0x07DF                   // GVGA_COLOR(31, 31, 0) = Red | Green
	White   Color = 0xFFDF                   // GVGA_COLOR(31, 31, 31)
)

// Mode flags
const (
	ModeBitmap        = 0x01
	ModeText          = 0x02
	ModeInterlaced    = 0x04
	ModeDoubleBuffered = 0x08
)

// Border positions
const (
	BorderTop    = 0
	BorderBottom = 1
	BorderLeft   = 2
	BorderRight  = 3
)

// Pin configuration for Pimoroni Pico VGA Demo Base
const (
	PinHSYNC   = machine.GPIO16
	PinVSYNC   = machine.GPIO17
	PinRGBBase = machine.GPIO0
	PinRGBCount = 16
)

// VGA timing constants for 640x480@60Hz
const (
	FrameWidth  = 640
	FrameHeight = 480

	// Horizontal timing (in pixels at 25MHz)
	HActive     = 640
	HFrontPorch = 16
	HSyncPulse  = 96
	HBackPorch  = 48
	HTotal      = 800

	// Vertical timing (in lines)
	VActive     = 480
	VFrontPorch = 10
	VSyncPulse  = 2
	VBackPorch  = 33
	VTotal      = 525

	VSyncStart = VActive + VFrontPorch     // 490
	VSyncEnd   = VSyncStart + VSyncPulse   // 492
)

// Pixels per byte for each bit depth
const (
	PixelsPerByte1BPP = 8
	PixelsPerByte2BPP = 4
	PixelsPerByte4BPP = 2
	PixelsPerByte8BPP = 1
)

// Font represents a bitmap font
type Font struct {
	Width     uint8
	Height    uint8
	FirstChar uint8
	LastChar  uint8
	Data      []byte
}

// GVga is the main VGA graphics context
type GVga struct {
	// Frame buffer pointers
	DrawFrame []byte // Frame being drawn to
	ShowFrame []byte // Frame being displayed

	// Palette (indexed colors -> RGB565)
	Palette []Color

	// Pre-computed palette lookup for scanline rendering
	PaletteBuf []uint16

	// Border colors
	BorderColors [4]Color

	// Display properties
	Width        uint16
	Height       uint16
	Bits         uint16 // Color depth (1, 2, 4, or 8)
	Colors       uint16 // Number of colors (2, 4, 16, or 256)
	RowBytes     uint16 // Bytes per row in frame buffer
	PixelsPerByte uint16
	FrameBytes   uint32

	// Scaling
	Multiplier uint16 // Y scale factor
	HeaderRows uint16 // Blank rows at top for centering

	// Mode flags
	Mode uint16

	// Text mode properties
	Rows uint16 // Character rows
	Cols uint16 // Character columns

	// Font
	Font *Font

	// User data
	UserData interface{}

	// Internal state
	running       bool
	lineCount     uint16
	frameCount    uint32
	vgaMode       *scanvideo.Mode
	RenderCount   uint32 // Public counter for debug

	// Synchronization (matches C mutex pattern)
	// rendering is true while render loop is actively processing scanlines (0 to Height-1)
	// Swap waits for this to be false before modifying ShowFrame
	rendering     bool
	// swapPending is set by Swap() to tell render loop to wait before starting new frame
	swapPending   bool
}

// ScanlineRenderFunc is the function signature for scanline renderers
type ScanlineRenderFunc func(gvga *GVga, buf []uint32, width, scanline int) int
