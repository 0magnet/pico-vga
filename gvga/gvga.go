// Package gvga provides VGA graphics for RP2040/Pico
// This is a TinyGo port of the GVga C library by drfrancintosh
// It uses the scanvideo package for hardware-timed VGA output via PIO+DMA
package gvga

import (
	"time"

	"github.com/0magnet/pico-vga/scanvideo"
)

// Composable scanline command offsets - must match scanvideo/pio.go
const (
	COMPOSABLE_EOL_SKIP_ALIGN  = 0
	COMPOSABLE_EOL_ALIGN       = 1
	COMPOSABLE_COLOR_RUN       = 3
	COMPOSABLE_RAW_RUN         = 7
	COMPOSABLE_RAW_1P          = 11
	COMPOSABLE_RAW_2P          = 13
)

// Palette lookup buffer size
const (
	_8_PIXELS_PER_BYTE = 8
	_4_PIXELS_PER_BYTE = 4
	_2_PIXELS_PER_BYTE = 2
)

// Global palette buffer for fast scanline rendering (matches C _gvga_paletteBuf)
var paletteBuf [256 * 8]uint16

// Package-level variable for render loop (matches scanvideo_minimal pattern)
var currentGVga *GVga

// Package-level mode for core1 initialization (matches C pattern)
var pendingMode *scanvideo.Mode

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

	// Build palette lookup table
	g.buildPaletteBuf()

	// Set default font
	g.Font = DefaultFont

	// Setup video mode based on width
	g.vgaMode = &scanvideo.Mode640x480_60
	if width == 320 {
		g.vgaMode = &scanvideo.Mode320x240_60
	}

	return g
}

// getVgaMode returns the scanvideo mode for this gvga instance
func (g *GVga) getVgaMode() *scanvideo.Mode {
	if g.vgaMode != nil {
		return g.vgaMode
	}
	return &scanvideo.Mode640x480_60
}

// Start begins VGA output using scanvideo
// Matches working scanvideo_minimal pattern exactly:
// 1. Setup scanvideo
// 2. Start render loop goroutine (pre-fills buffers)
// 3. Brief sleep to allow pre-fill
// 4. Enable timing
func (g *GVga) Start() {
	println("gvga.Start: entering")

	// Select mode based on width
	var mode *scanvideo.Mode
	if g.Width == 320 {
		mode = &scanvideo.Mode320x240_60
	} else {
		mode = &scanvideo.Mode640x480_60
	}
	println("gvga.Start: width=", g.Width, "using mode width=", mode.Width)

	// Store gvga context for render loop
	g.running = true
	currentGVga = g

	// Setup scanvideo (like scanvideo_minimal)
	if !scanvideo.Setup(mode) {
		println("gvga.Start: scanvideo setup failed")
		return
	}
	println("gvga.Start: Setup succeeded")

	// Start render loop goroutine BEFORE enabling timing
	// This allows pre-filling scanline buffers (matches scanvideo_minimal)
	go renderLoopFunc()
	println("gvga.Start: render loop started")

	// Sleep to let render loop pre-fill buffers (matches scanvideo_minimal)
	time.Sleep(50 * time.Millisecond)

	// Enable timing
	scanvideo.TimingEnable(true)
	println("gvga.Start: timing enabled")
}

// Stop stops VGA output
func (g *GVga) Stop() {
	g.running = false
	scanvideo.TimingEnable(false)
}

// renderLoopFunc is package-level render loop (matches C render_loop in gvga.c)
func renderLoopFunc() {
	println("renderLoopFunc: starting")
	g := currentGVga
	if g == nil {
		println("ERROR: currentGVga is nil!")
		return
	}
	println("renderLoopFunc: g.Width=", g.Width, "g.Height=", g.Height, "g.HeaderRows=", g.HeaderRows)

	for {
		// Get a scanline buffer (blocks until one is available)
		buf := scanvideo.BeginScanlineGeneration(true)
		if buf == nil {
			continue
		}

		g.RenderCount++
		scanline := scanvideo.ScanlineNumber(buf.ScanlineID)

		// Render appropriate content based on scanline position
		if scanline < g.HeaderRows {
			// Top border
			buf.DataUsed = g.renderBlankLine(buf.Data, int(g.Width), int(scanline), g.BorderColors[BorderTop])
		} else if scanline < g.Height+g.HeaderRows {
			// Active display region
			buf.DataUsed = g.renderScanline(buf.Data, int(g.Width), int(scanline-g.HeaderRows))
		} else {
			// Bottom border
			buf.DataUsed = g.renderBlankLine(buf.Data, int(g.Width), int(scanline), g.BorderColors[BorderBottom])
		}

		buf.Status = scanvideo.ScanlineOK
		scanvideo.EndScanlineGeneration(buf)
	}
}

// Sync waits for the current frame to finish displaying
func (g *GVga) Sync() {
	scanvideo.WaitForVblank()
}

// Swap swaps draw and show buffers (for double buffering)
func (g *GVga) Swap(doCopy bool) {
	if g.Mode&ModeDoubleBuffered == 0 {
		return
	}

	// Wait for vblank to avoid tearing
	scanvideo.WaitForVblank()

	if doCopy && g.DrawFrame != nil && g.ShowFrame != nil {
		copy(g.ShowFrame, g.DrawFrame)
	}
	g.DrawFrame, g.ShowFrame = g.ShowFrame, g.DrawFrame
}

// renderLoopInternal is the main VGA rendering loop (runs on core1 via goroutine)
// Simplified to match scanvideo_minimal exactly for debugging
func (g *GVga) renderLoopInternal() {
	println("gvga.renderLoopInternal: starting")
	scanlineCount := uint32(0)

	for g.running {
		// Get a scanline buffer from scanvideo
		buf := scanvideo.BeginScanlineGeneration(true)
		if buf == nil {
			continue
		}

		scanlineCount++
		g.RenderCount = scanlineCount

		scanline := scanvideo.ScanlineNumber(buf.ScanlineID)

		// Debug: print periodically
		if scanlineCount%10000 == 1 {
			println("gvga: scanline", scanline, "Width", g.Width)
		}

		// Simplified render - directly write to buf.Data like scanvideo_minimal does
		// Use scanvideo's constants directly to match exactly
		bgColor := uint32(0x001F) // RED for visibility
		buf.Data[0] = uint32(scanvideo.COMPOSABLE_COLOR_RUN) | (bgColor << 16)
		buf.Data[1] = uint32(g.Width-3) | (uint32(scanvideo.COMPOSABLE_RAW_1P) << 16)
		buf.Data[2] = uint32(scanvideo.COMPOSABLE_EOL_ALIGN) // LOW 16 bits for PIO
		buf.DataUsed = 3
		buf.Status = scanvideo.ScanlineOK

		scanvideo.EndScanlineGeneration(buf)
	}
	println("gvga.renderLoopInternal: exiting")
}

// renderScanline dispatches to the appropriate bit-depth renderer
func (g *GVga) renderScanline(buf []uint32, width, scanline int) uint16 {
	switch g.Bits {
	case 1:
		return g.renderScanline1BPP(buf, width, scanline)
	case 2:
		return g.renderScanline2BPP(buf, width, scanline)
	case 4:
		return g.renderScanline4BPP(buf, width, scanline)
	case 8:
		return g.renderScanline8BPP(buf, width, scanline)
	default:
		return g.renderBlankLine(buf, width, scanline, Black)
	}
}

// Test configuration for RAW_RUN pixel count (must be multiple of 8)
var TestRawPixels = 8

// UseColorRunFor1BPP enables simpler COLOR_RUN rendering instead of RAW_RUN for debugging
// Set to false to use RAW_RUN (matching C source _scanline_render_1bpp)
var UseColorRunFor1BPP = false // Use RAW_RUN to show actual pixels

// renderScanline1BPP renders a 1bpp scanline
// When UseColorRunFor1BPP is true, uses simple COLOR_RUN (for debugging)
// When false, uses RAW_RUN matching C _scanline_render_1bpp exactly
func (g *GVga) renderScanline1BPP(buf []uint32, width, scanline int) uint16 {
	if UseColorRunFor1BPP {
		return g.renderScanline1BPP_ColorRun(buf, width, scanline)
	}
	return g.renderScanline1BPP_RawRun(buf, width, scanline)
}

// renderScanline1BPP_ColorRun uses simple COLOR_RUN for solid lines
// This is simpler and more reliable for debugging
func (g *GVga) renderScanline1BPP_ColorRun(buf []uint32, width, scanline int) uint16 {
	idx := scanline * width / _8_PIXELS_PER_BYTE

	// Check first byte to determine dominant color
	var color uint16
	if g.ShowFrame[idx] == 0xFF {
		color = uint16(g.Palette[1]) // All 1s = color 1
	} else {
		color = uint16(g.Palette[0]) // All 0s = color 0
	}

	// Simple COLOR_RUN for the whole line
	buf[0] = uint32(COMPOSABLE_COLOR_RUN) | (uint32(color) << 16)
	buf[1] = uint32(width-3) | (uint32(COMPOSABLE_RAW_1P) << 16)
	// After RAW_1P outputs black (0), "out pc" reads next 16 bits for jump target
	// EOL_ALIGN must be in LOW 16 bits (PIO reads low bits first with shift-right)
	buf[2] = uint32(COMPOSABLE_EOL_ALIGN)
	return 3
}

// renderScanline1BPP_RawRun renders using RAW_RUN (matches C _scanline_render_1bpp exactly)
func (g *GVga) renderScanline1BPP_RawRun(buf []uint32, width, scanline int) uint16 {
	ptr := 0
	idx := scanline * width / _8_PIXELS_PER_BYTE
	row := g.ShowFrame[idx:]
	cols := width / _8_PIXELS_PER_BYTE

	// First byte - 8 pixels
	b := row[0]
	colors := paletteBuf[int(b)*_8_PIXELS_PER_BYTE:]
	rowIdx := 1

	// RAW_RUN header + first 8 pixels
	buf[ptr] = uint32(COMPOSABLE_RAW_RUN) | (uint32(colors[0]) << 16) // RAW_RUN | p0
	ptr++
	buf[ptr] = uint32(width-3) | (uint32(colors[1]) << 16) // count | p1
	ptr++
	buf[ptr] = uint32(colors[2]) | (uint32(colors[3]) << 16) // p2 | p3
	ptr++
	buf[ptr] = uint32(colors[4]) | (uint32(colors[5]) << 16) // p4 | p5
	ptr++
	buf[ptr] = uint32(colors[6]) | (uint32(colors[7]) << 16) // p6 | p7
	ptr++
	cols--

	// Remaining bytes (8 pixels each)
	for i := 0; i < cols; i++ {
		b = row[rowIdx]
		rowIdx++
		colors = paletteBuf[int(b)*_8_PIXELS_PER_BYTE:]

		buf[ptr] = uint32(colors[0]) | (uint32(colors[1]) << 16)
		ptr++
		buf[ptr] = uint32(colors[2]) | (uint32(colors[3]) << 16)
		ptr++
		buf[ptr] = uint32(colors[4]) | (uint32(colors[5]) << 16)
		ptr++
		buf[ptr] = uint32(colors[6]) | (uint32(colors[7]) << 16)
		ptr++
	}

	// End of line - must end with black pixel
	buf[ptr] = uint32(COMPOSABLE_RAW_1P) | (0 << 16)
	ptr++
	// EOL_ALIGN must be in LOW 16 bits (PIO reads low bits first with shift-right)
	buf[ptr] = uint32(COMPOSABLE_EOL_ALIGN)
	ptr++

	return uint16(ptr)
}

// renderScanline2BPP renders a 2bpp scanline (matches C _scanline_render_2bpp)
func (g *GVga) renderScanline2BPP(buf []uint32, width, scanline int) uint16 {
	ptr := 0
	idx := scanline * width / _4_PIXELS_PER_BYTE
	row := g.ShowFrame[idx:]

	b := row[0]
	colors := paletteBuf[int(b)*_4_PIXELS_PER_BYTE:]

	// RAW_RUN header
	buf[ptr] = uint32(COMPOSABLE_RAW_RUN) | (uint32(colors[0]) << 16)
	ptr++
	buf[ptr] = uint32(width-3) | (uint32(colors[1]) << 16)
	ptr++
	buf[ptr] = uint32(colors[2]) | (uint32(colors[3]) << 16)
	ptr++

	// Remaining bytes
	for pixel := _4_PIXELS_PER_BYTE; pixel < width; pixel += _4_PIXELS_PER_BYTE {
		byteIdx := pixel / _4_PIXELS_PER_BYTE
		b = row[byteIdx]
		colors = paletteBuf[int(b)<<2:]

		buf[ptr] = uint32(colors[0]) | (uint32(colors[1]) << 16)
		ptr++
		buf[ptr] = uint32(colors[2]) | (uint32(colors[3]) << 16)
		ptr++
	}

	// End of line
	buf[ptr] = uint32(COMPOSABLE_RAW_1P) | (0 << 16)
	ptr++
	buf[ptr] = uint32(COMPOSABLE_EOL_ALIGN) // LOW 16 bits for PIO
	ptr++

	return uint16(ptr)
}

// renderScanline4BPP renders a 4bpp scanline (matches C _scanline_render_4bpp)
func (g *GVga) renderScanline4BPP(buf []uint32, width, scanline int) uint16 {
	ptr := 0
	idx := scanline * width / _2_PIXELS_PER_BYTE
	row := g.ShowFrame[idx:]

	b := row[0]
	colors := paletteBuf[int(b)*_2_PIXELS_PER_BYTE:]

	// RAW_RUN header
	buf[ptr] = uint32(COMPOSABLE_RAW_RUN) | (uint32(colors[0]) << 16)
	ptr++
	buf[ptr] = uint32(width-3) | (uint32(colors[1]) << 16)
	ptr++

	// Remaining bytes
	for pixel := _2_PIXELS_PER_BYTE; pixel < width; pixel += _2_PIXELS_PER_BYTE {
		byteIdx := pixel / _2_PIXELS_PER_BYTE
		b = row[byteIdx]
		colors = paletteBuf[int(b)*_2_PIXELS_PER_BYTE:]

		buf[ptr] = uint32(colors[0]) | (uint32(colors[1]) << 16)
		ptr++
	}

	// End of line
	buf[ptr] = uint32(COMPOSABLE_RAW_1P) | (0 << 16)
	ptr++
	buf[ptr] = uint32(COMPOSABLE_EOL_ALIGN) // LOW 16 bits for PIO
	ptr++

	return uint16(ptr)
}

// renderScanline8BPP renders an 8bpp scanline (matches C _scanline_render_8bpp)
func (g *GVga) renderScanline8BPP(buf []uint32, width, scanline int) uint16 {
	ptr := 0
	idx := scanline * width
	row := g.ShowFrame[idx:]

	// RAW_RUN header
	buf[ptr] = uint32(COMPOSABLE_RAW_RUN) | (uint32(g.Palette[row[0]]) << 16)
	ptr++
	buf[ptr] = uint32(width-3) | (uint32(g.Palette[row[1]]) << 16)
	ptr++

	// Remaining pixels (pairs)
	for pixel := 2; pixel < width; pixel += 2 {
		buf[ptr] = uint32(g.Palette[row[pixel]]) | (uint32(g.Palette[row[pixel+1]]) << 16)
		ptr++
	}

	// End of line
	buf[ptr] = uint32(COMPOSABLE_RAW_1P) | (0 << 16)
	ptr++
	buf[ptr] = uint32(COMPOSABLE_EOL_ALIGN) // LOW 16 bits for PIO
	ptr++

	return uint16(ptr)
}

// renderBlankLine renders a solid color line (matches C _scanline_render_blank_line exactly)
func (g *GVga) renderBlankLine(buf []uint32, width, scanline int, color Color) uint16 {
	ptr := 0

	// COLOR_RUN format matching C exactly
	buf[ptr] = uint32(COMPOSABLE_COLOR_RUN) | (uint32(color) << 16)
	ptr++
	buf[ptr] = uint32(width-5) | (uint32(COMPOSABLE_RAW_2P) << 16)
	ptr++
	buf[ptr] = uint32(color) | (uint32(color) << 16)
	ptr++
	buf[ptr] = uint32(COMPOSABLE_RAW_1P) | (0 << 16)
	ptr++
	// EOL_ALIGN must be in LOW 16 bits (PIO reads low bits first with shift-right)
	buf[ptr] = uint32(COMPOSABLE_EOL_ALIGN)
	ptr++

	return uint16(ptr)
}

// buildPaletteBuf builds the palette lookup table
func (g *GVga) buildPaletteBuf() {
	switch g.Bits {
	case 1:
		g.buildPaletteBuf1BPP()
	case 2:
		g.buildPaletteBuf2BPP()
	case 4:
		g.buildPaletteBuf4BPP()
	}
	// 8bpp doesn't need a lookup table - uses palette directly
}

// buildPaletteBuf1BPP builds 1bpp palette lookup (matches C _gvga_palette_1bpp)
func (g *GVga) buildPaletteBuf1BPP() {
	for i := 0; i < 256; i++ {
		for j := 0; j < 8; j++ {
			bit := 1 << (7 - j)
			index := 0
			if i&bit != 0 {
				index = 1
			}
			paletteBuf[i*8+j] = uint16(g.Palette[index])
		}
	}
}

// buildPaletteBuf2BPP builds 2bpp palette lookup (matches C _gvga_palette_2bpp)
func (g *GVga) buildPaletteBuf2BPP() {
	for i := 0; i < 256; i++ {
		chew0 := (i & 0xc0) >> 6
		chew1 := (i & 0x30) >> 4
		chew2 := (i & 0x0c) >> 2
		chew3 := i & 0x03
		paletteBuf[i*4+0] = uint16(g.Palette[chew0])
		paletteBuf[i*4+1] = uint16(g.Palette[chew1])
		paletteBuf[i*4+2] = uint16(g.Palette[chew2])
		paletteBuf[i*4+3] = uint16(g.Palette[chew3])
	}
}

// buildPaletteBuf4BPP builds 4bpp palette lookup (matches C _gvga_palette_4bpp)
func (g *GVga) buildPaletteBuf4BPP() {
	for i := 0; i < 256; i++ {
		nybble0 := (i & 0xf0) >> 4
		nybble1 := i & 0x0f
		paletteBuf[i*2+0] = uint16(g.Palette[nybble0])
		paletteBuf[i*2+1] = uint16(g.Palette[nybble1])
	}
}

// Destroy frees resources
func (g *GVga) Destroy() {
	g.Stop()
	g.DrawFrame = nil
	g.ShowFrame = nil
	g.Palette = nil
}

// DebugPaletteBuf returns the first n entries from the global paletteBuf for debugging
func DebugPaletteBuf(n int) []uint16 {
	if n > len(paletteBuf) {
		n = len(paletteBuf)
	}
	result := make([]uint16, n)
	copy(result, paletteBuf[:n])
	return result
}

// DebugRenderScanline renders a single scanline to a provided buffer and returns debug info
func (g *GVga) DebugRenderScanline(scanline int) (dataUsed uint16, firstWords [8]uint32) {
	buf := make([]uint32, 400) // Large enough for any scanline
	dataUsed = g.renderScanline(buf, int(g.Width), scanline)
	for i := 0; i < 8 && i < len(buf); i++ {
		firstWords[i] = buf[i]
	}
	return
}

// DebugRenderScanlineFull renders a scanline into the provided buffer and returns dataUsed
func (g *GVga) DebugRenderScanlineFull(scanline int, buf []uint32) uint16 {
	return g.renderScanline(buf, int(g.Width), scanline)
}
