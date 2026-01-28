// TinyGo port of GVga hello_world example
// Matches C version: apps/a0_hello_world/src/main.c
package main

import (
	"machine"
	"runtime"
	"time"

	"github.com/0magnet/pico-vga/gvga"
	"github.com/0magnet/pico-vga/scanvideo"
)

// Palette matching C version: WHITE, RED, GREEN, BLUE, YELLOW, CYAN, VIOLET, BLACK, ...
var palette = []gvga.Color{
	gvga.White, gvga.Red, gvga.Green, gvga.Blue,
	gvga.Yellow, gvga.Cyan, gvga.Magenta, gvga.Black,
	gvga.RGB5(15, 15, 15), gvga.RGB5(15, 0, 0), gvga.RGB5(0, 15, 0), gvga.RGB5(0, 0, 15),
	gvga.RGB5(15, 15, 0), gvga.RGB5(0, 15, 15), gvga.RGB5(15, 15, 0), gvga.RGB5(7, 7, 7),
}

type helloWorldState struct {
	width, height int
	x, y          int
	dx, dy        int
	color1, color2 uint16
}

var state helloWorldState

func main() {
	// Initialize serial first
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(500 * time.Millisecond)
	println("GVga hello_world starting...")

	// Initialize LED
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	println("LED configured")

	// Use 320x240 for now (working mode), with double buffering
	width := 320
	height := 240
	bits := 1
	doubleBuffer := true // Enable double buffer to prevent jitter/tearing
	interlaced := false

	println("Calling gvga.Init...")
	g := gvga.Init(uint16(width), uint16(height), bits, doubleBuffer, interlaced, nil)
	if g == nil {
		println("gvga.Init failed!")
		for {
			led.Low()
			time.Sleep(100 * time.Millisecond)
			led.High()
			time.Sleep(100 * time.Millisecond)
		}
	}
	println("gvga.Init succeeded, Width=", g.Width, "Height=", g.Height)

	// Set palette
	println("Setting palette...")
	g.SetPalette(palette, 0, len(palette))
	println("Palette set")

	// Initialize state
	initHelloWorld(&state, width, height)
	println("State initialized")

	// Pre-fill BOTH frame buffers before starting video to ensure clean initial state
	// This prevents the render loop from seeing garbage data on startup
	println("Pre-filling frame buffers...")
	g.Clear(0)
	drawHelloWorld(g, &state)
	// Don't call Swap yet - we need to fill ShowFrame directly
	// Copy DrawFrame to ShowFrame so both have identical content
	copy(g.ShowFrame, g.DrawFrame)
	println("Frame buffers initialized")

	// Start video
	println("Starting video...")
	g.Start()
	scanlineOff, timingOff := scanvideo.GetProgramOffsets()
	println("Video started, scanline offset=", scanlineOff, "timing offset=", timingOff)

	// Wait for a few frames to ensure pre-filled buffers are consumed
	// This prevents half-line artifacts from buffer timing at startup
	g.Sync()
	g.Sync()
	println("Startup sync complete")

	// Main loop - SIMPLIFIED to match scanvideo_minimal exactly
	loopCount := 0
	for {
		loopCount++
		if loopCount%100 == 0 {
			led.Set(!led.Get())
			stats := scanvideo.GetDebugStats()
			bufs := scanvideo.GetBufferCounts()
			println("loop:", loopCount, "render:", g.RenderCount)
			println("  IRQ0:", stats.IRQ0Count, "IRQ1:", stats.IRQ1Count, "DMA:", stats.DMAStarts)
			println("  Hits:", stats.BufferHits, "Miss:", stats.BufferMisses, "Frames:", stats.FramesComplete)
			println("  Bufs: free=", bufs.Free, "gen=", bufs.Generated, "use=", bufs.InUse, "cur=", bufs.Current)
			println("  YRepeat:", bufs.YRepeat, "/", bufs.YTarget)
		}

		// Draw content with explicit yields to let render loop run
		runtime.Gosched() // Yield before drawing
		g.Clear(0)
		runtime.Gosched() // Yield after clear
		drawHelloWorld(g, &state)
		runtime.Gosched() // Yield after draw
		moveHelloWorld(&state)
		g.Swap(false) // false = no copy, proper double buffering (matches C)

		// Check for commands
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			if b == 'r' {
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			} else if b == 'd' {
				// Comprehensive debug dump with pixel verification
				println("=== PIXEL VERIFICATION ===")
				println("Width:", g.Width, "Height:", g.Height, "Bits:", g.Bits)
				println("Palette[0]=", hexU16(uint16(g.Palette[0])), "(WHITE)")
				println("Palette[1]=", hexU16(uint16(g.Palette[1])), "(RED)")
				println("")

				// Show framebuffer bytes for scanline 0
				println("Scanline 0 framebuffer (first 8 bytes):")
				for i := 0; i < 8; i++ {
					println("  byte[", i, "]=", g.ShowFrame[i], "binary=", binU8(g.ShowFrame[i]))
				}
				println("")

				// Render scanline 0 and show the pixel data
				buf := make([]uint32, 330)
				dataUsed := g.DebugRenderScanlineFull(0, buf)
				println("Rendered scanline 0: dataUsed=", dataUsed, "words")

				// Decode RAW_RUN header
				cmd := buf[0] & 0xFFFF
				println("Command word: cmd=", cmd, "(should be 7=RAW_RUN)")
				println("")

				// Decode first 16 pixels from the rendered buffer
				println("First 16 rendered pixels (from RAW_RUN buffer):")
				// RAW_RUN format: buf[0]=(p0<<16)|cmd, buf[1]=(p1<<16)|count, buf[2]=(p3<<16)|p2, ...
				p0 := (buf[0] >> 16) & 0xFFFF
				p1 := (buf[1] >> 16) & 0xFFFF
				p2 := buf[2] & 0xFFFF
				p3 := (buf[2] >> 16) & 0xFFFF
				p4 := buf[3] & 0xFFFF
				p5 := (buf[3] >> 16) & 0xFFFF
				p6 := buf[4] & 0xFFFF
				p7 := (buf[4] >> 16) & 0xFFFF
				// Next 8 pixels from byte 1
				p8 := buf[5] & 0xFFFF
				p9 := (buf[5] >> 16) & 0xFFFF
				p10 := buf[6] & 0xFFFF
				p11 := (buf[6] >> 16) & 0xFFFF
				p12 := buf[7] & 0xFFFF
				p13 := (buf[7] >> 16) & 0xFFFF
				p14 := buf[8] & 0xFFFF
				p15 := (buf[8] >> 16) & 0xFFFF

				// Show pixels with color names
				white := uint16(g.Palette[0])
				red := uint16(g.Palette[1])
				println("Pixels 0-7 (from byte 0):")
				printPixel(0, uint16(p0), white, red)
				printPixel(1, uint16(p1), white, red)
				printPixel(2, uint16(p2), white, red)
				printPixel(3, uint16(p3), white, red)
				printPixel(4, uint16(p4), white, red)
				printPixel(5, uint16(p5), white, red)
				printPixel(6, uint16(p6), white, red)
				printPixel(7, uint16(p7), white, red)
				println("Pixels 8-15 (from byte 1):")
				printPixel(8, uint16(p8), white, red)
				printPixel(9, uint16(p9), white, red)
				printPixel(10, uint16(p10), white, red)
				printPixel(11, uint16(p11), white, red)
				printPixel(12, uint16(p12), white, red)
				printPixel(13, uint16(p13), white, red)
				printPixel(14, uint16(p14), white, red)
				printPixel(15, uint16(p15), white, red)

				// Verify: for byte=0xFF, all pixels should be RED
				// for byte=0x00, all pixels should be WHITE
				println("")
				println("Verification:")
				byte0 := g.ShowFrame[0]
				println("  Byte 0 =", byte0, "- pixels should be:", expectedColors(byte0))
				byte1 := g.ShowFrame[1]
				println("  Byte 1 =", byte1, "- pixels should be:", expectedColors(byte1))

				// Request DMA capture for next frame
				scanvideo.EnableDMACapture()
				println("DMA capture enabled - press 'c' to see captured data")

				// Check PIO status
				pio := scanvideo.GetPIOStatus()
				println("")
				println("PIO Status:")
				println("  Scanline SM enabled:", pio.ScanlineSMEnabled)
				println("  Timing SM enabled:", pio.TimingSMEnabled)
				println("  Scanline ADDR:", pio.ScanlineAddr)
				println("  Timing ADDR:", pio.TimingAddr)
				println("  FSTAT:", hexU32(pio.ScanlineFSTAT))
				println("  IRQ raw:", pio.IRQRaw)
				println("===================")
			} else if b == 'c' {
				// Show captured DMA buffer (actual data sent to PIO)
				captured, scanline, dataUsed, words := scanvideo.GetDMACapture()
				if !captured {
					println("No DMA capture yet - press 'd' first")
				} else {
					println("=== ACTUAL DMA/PIO DATA ===")
					println("Scanline:", scanline, "DataUsed:", dataUsed)
					println("First 10 words sent to PIO:")
					for i := 0; i < 10; i++ {
						println("  DMA[", i, "]=", hexU32(words[i]))
					}
					// Decode pixels from DMA buffer
					println("Decoded pixels from DMA buffer:")
					p0 := (words[0] >> 16) & 0xFFFF
					p1 := (words[1] >> 16) & 0xFFFF
					p2 := words[2] & 0xFFFF
					p3 := (words[2] >> 16) & 0xFFFF
					white := uint16(g.Palette[0])
					red := uint16(g.Palette[1])
					println("  p0=", hexU16(uint16(p0)), colorName(uint16(p0), white, red))
					println("  p1=", hexU16(uint16(p1)), colorName(uint16(p1), white, red))
					println("  p2=", hexU16(uint16(p2)), colorName(uint16(p2), white, red))
					println("  p3=", hexU16(uint16(p3)), colorName(uint16(p3), white, red))
					println("=========================")
				}
			} else if b == 's' {
				// Capture first 10 scanlines to see actual pixel data
				println("Capturing scanlines 0-9...")
				scanvideo.EnableMultiDMACapture(0)
			} else if b == 'm' {
				// Capture mid-screen scanlines
				println("Capturing scanlines 100-109...")
				scanvideo.EnableMultiDMACapture(100)
				// Wait a bit for capture to complete
				time.Sleep(100 * time.Millisecond)
				count, lines, used, data, addrs := scanvideo.GetMultiDMACapture()
				println("=== SCANLINE CAPTURE ===")
				println("Captured", count, "scanlines")
				white := uint16(g.Palette[0])
				red := uint16(g.Palette[1])
				for i := 0; i < count; i++ {
					println("--- Scanline", lines[i], "dataUsed=", used[i], "addr=", hexU32(addrs[i]), "---")
					// Decode first 8 pixels
					// Format depends on command: COLOR_RUN or RAW_RUN
					cmd := data[i][0] & 0xFFFF
					if cmd == 3 { // COLOR_RUN
						color := (data[i][0] >> 16) & 0xFFFF
						runLen := data[i][1] & 0xFFFF
						println("  COLOR_RUN: color=", hexU16(uint16(color)), colorName(uint16(color), white, red), "len=", runLen)
					} else if cmd == 7 { // RAW_RUN
						p0 := (data[i][0] >> 16) & 0xFFFF
						p1 := (data[i][1] >> 16) & 0xFFFF
						p2 := data[i][2] & 0xFFFF
						p3 := (data[i][2] >> 16) & 0xFFFF
						p4 := data[i][3] & 0xFFFF
						p5 := (data[i][3] >> 16) & 0xFFFF
						p6 := data[i][4] & 0xFFFF
						p7 := (data[i][4] >> 16) & 0xFFFF
						println("  RAW_RUN pixels 0-7:")
						println("   ", colorName(uint16(p0), white, red), colorName(uint16(p1), white, red), colorName(uint16(p2), white, red), colorName(uint16(p3), white, red), colorName(uint16(p4), white, red), colorName(uint16(p5), white, red), colorName(uint16(p6), white, red), colorName(uint16(p7), white, red))
					} else {
						println("  Unknown cmd:", cmd)
					}
				}
				println("========================")
			}
		}

		// Small delay - matches scanvideo_minimal
		time.Sleep(10 * time.Millisecond)
	}
}

func initHelloWorld(s *helloWorldState, width, height int) {
	s.width = width
	s.height = height
	s.x = 20
	s.y = 20
	s.dx = 5
	s.dy = 5
	s.color1 = 1 // RED
	s.color2 = 2 // GREEN
}

func moveHelloWorld(s *helloWorldState) {
	s.x += s.dx
	s.y += s.dy
	if s.x > s.width-120 || s.x < 20 {
		s.dx = -s.dx
	}
	if s.y > s.height-70 || s.y < 20 {
		s.dy = -s.dy
	}
}

// Helper to format uint16 as hex string
func hexU16(v uint16) string {
	const hexChars = "0123456789ABCDEF"
	return "0x" + string(hexChars[(v>>12)&0xF]) + string(hexChars[(v>>8)&0xF]) +
		string(hexChars[(v>>4)&0xF]) + string(hexChars[v&0xF])
}

// Helper to format uint32 as hex string
func hexU32(v uint32) string {
	const hexChars = "0123456789ABCDEF"
	return "0x" + string(hexChars[(v>>28)&0xF]) + string(hexChars[(v>>24)&0xF]) +
		string(hexChars[(v>>20)&0xF]) + string(hexChars[(v>>16)&0xF]) +
		string(hexChars[(v>>12)&0xF]) + string(hexChars[(v>>8)&0xF]) +
		string(hexChars[(v>>4)&0xF]) + string(hexChars[v&0xF])
}

// Helper to format uint8 as binary string
func binU8(v uint8) string {
	result := ""
	for i := 7; i >= 0; i-- {
		if v&(1<<i) != 0 {
			result += "1"
		} else {
			result += "0"
		}
	}
	return result
}

// Helper to print pixel with color name
func printPixel(idx int, value, white, red uint16) {
	colorName := "?"
	if value == white {
		colorName = "W"
	} else if value == red {
		colorName = "R"
	}
	println("  p", idx, "=", hexU16(value), colorName)
}

// Helper to describe expected colors for a byte value
func expectedColors(b uint8) string {
	// Each bit: 1=RED, 0=WHITE (MSB first)
	result := ""
	for i := 7; i >= 0; i-- {
		if b&(1<<i) != 0 {
			result += "R"
		} else {
			result += "W"
		}
	}
	return result
}

// Helper to get color name
func colorName(value, white, red uint16) string {
	if value == white {
		return "WHITE"
	} else if value == red {
		return "RED"
	}
	return "?"
}

func drawHelloWorld(g *gvga.GVga, s *helloWorldState) {
	width := s.width
	height := s.height
	X := s.x
	Y := s.y
	color1 := s.color1
	color2 := s.color2

	// Draw border boxes
	for i := 1; i < 16; i++ {
		g.Box(i-1, i-1, width-i, height-i, uint16(i)%g.Colors)
	}

	x := X
	y := Y
	h := 20
	w := 10
	H := h / 2
	W := w / 2
	dx := w + 10

	// H
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+w, y+0, x+w, y+h, color1)
	g.Line(x+0, y+H, x+w, y+H, color1)
	x += dx
	// E
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+0, y+0, x+w, y+0, color1)
	g.Line(x+0, y+H, x+w, y+H, color1)
	g.Line(x+0, y+h, x+w, y+h, color1)
	x += dx
	// L
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+0, y+h, x+w, y+h, color1)
	x += dx
	// L
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+0, y+h, x+w, y+h, color1)
	x += dx
	// O
	g.Line(x+0, y+0, x+0, y+h, color1)
	g.Line(x+w, y+0, x+w, y+h, color1)
	g.Line(x+0, y+0, x+w, y+0, color1)
	g.Line(x+0, y+h, x+w, y+h, color1)

	x = X
	y += h + 10
	// W
	g.Line(x+0, y+0, x+0, y+h, color2)
	g.Line(x+w, y+0, x+w, y+h, color2)
	g.Line(x+0, y+h, x+W, y+H, color2)
	g.Line(x+w, y+h, x+W, y+H, color2)
	x += dx
	// O
	g.Line(x+0, y+0, x+0, y+h, color2)
	g.Line(x+w, y+0, x+w, y+h, color2)
	g.Line(x+0, y+0, x+w, y+0, color2)
	g.Line(x+0, y+h, x+w, y+h, color2)
	x += dx
	// R
	g.Line(x+0, y+0, x+0, y+h, color2)
	g.Line(x+w, y+0, x+w, y+H, color2)
	g.Line(x+0, y+0, x+w, y+0, color2)
	g.Line(x+0, y+H, x+w, y+H, color2)
	g.Line(x+W, y+H, x+w, y+h, color2)
	x += dx
	// L
	g.Line(x+0, y+0, x+0, y+h, color2)
	g.Line(x+0, y+h, x+w, y+h, color2)
	x += dx
	// D
	g.Line(x+W/2, y+0, x+W/2, y+h, color2)
	g.Line(x+0, y+0, x+w, y+0, color2)
	g.Line(x+w, y+0, x+w, y+h, color2)
	g.Line(x+0, y+h, x+w, y+h, color2)
	x += dx
	// !
	g.Line(x+0, y+0, x+0, y+H+H/2, color2)
	g.Line(x+0, y+h-2, x+0, y+h, color2)
}
