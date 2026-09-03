// Minimal gvga test - just shows a solid color screen
// This matches scanvideo minimal but uses gvga for rendering
package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/gvga"
	"github.com/0magnet/pico-vga/scanvideo"
)

func main() {
	// Initialize serial
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(2 * time.Second)
	println("gvga minimal test")

	// Initialize LED
	led := machine.LED
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})

	// Use 320x240 mode like the working scanvideo minimal
	width := uint16(320)
	height := uint16(240)
	bits := 1
	doubleBuffer := false
	interlaced := false

	println("Initializing gvga...")
	g := gvga.Init(width, height, bits, doubleBuffer, interlaced, nil)
	if g == nil {
		println("gvga.Init failed!")
		for {
			led.Low()
			time.Sleep(100 * time.Millisecond)
			led.High()
			time.Sleep(100 * time.Millisecond)
		}
	}
	println("gvga.Init succeeded")

	// Set a simple 2-color palette: BLACK and RED
	palette := []gvga.Color{gvga.Black, gvga.Red}
	g.SetPalette(palette, 0, 2)
	println("Palette set")

	// Fill screen with color 1 (RED)
	g.Clear(1)
	println("Screen cleared to RED")

	// Debug: print palette and paletteBuf
	println("Palette[0]=", uint16(g.Palette[0]), "Palette[1]=", uint16(g.Palette[1]))
	// Check paletteBuf for byte 0x00 (all zeros = color 0) and 0xFF (all ones = color 1)
	pb0 := gvga.DebugPaletteBuf(8)   // First 8 entries (byte=0x00)
	println("paletteBuf[0-7] (byte 0x00):", pb0[0], pb0[1], pb0[2], pb0[3])
	// Test what a rendered scanline would look like
	dataUsed, firstWords := g.DebugRenderScanline(0)
	println("Scanline 0: dataUsed=", dataUsed)
	println("  words[0]=", firstWords[0], "words[1]=", firstWords[1])
	println("  words[2]=", firstWords[2], "words[3]=", firstWords[3])

	// Debug: check frame buffer
	println("ShowFrame[0]=", g.ShowFrame[0], "len=", len(g.ShowFrame))

	// Debug: check paletteBuf for 1BPP
	// For byte=0xFF (all 1s), paletteBuf should have 8 copies of Palette[1]=RED
	pb255 := gvga.DebugPaletteBuf(256*8 - 8 + 8) // Get entries for byte 0xFF
	println("paletteBuf for 0xFF (all 1s):")
	println("  [0-3]:", pb255[255*8+0], pb255[255*8+1], pb255[255*8+2], pb255[255*8+3])
	println("  [4-7]:", pb255[255*8+4], pb255[255*8+5], pb255[255*8+6], pb255[255*8+7])
	println("Expected RED=", uint16(gvga.Red))

	// Start video
	println("Starting video...")
	g.Start()
	println("Video started")

	// Main loop - just blink LED
	ledState := false
	counter := 0
	for {
		counter++
		if counter%100 == 0 {
			ledState = !ledState
			led.Set(ledState)
			stats := scanvideo.GetDebugStats()
			println("cnt:", counter, "render:", g.RenderCount)
			println("  IRQ0:", stats.IRQ0Count, "IRQ1:", stats.IRQ1Count, "DMA:", stats.DMAStarts)

			// Periodic debug: show frame buffer and palette state
			if counter == 100 {
				println("DEBUG: ShowFrame[0]=", g.ShowFrame[0], "Palette[0]=", uint16(g.Palette[0]), "Palette[1]=", uint16(g.Palette[1]))
				// Check paletteBuf for byte 0xFF (all bits set = color 1)
				pb := gvga.DebugPaletteBuf(256 * 8)
				println("paletteBuf[0xFF*8]:", pb[255*8], pb[255*8+1], pb[255*8+2], pb[255*8+3])
				// Also check what a rendered scanline looks like
				dataUsed, firstWords := g.DebugRenderScanline(0)
				println("Scanline0: used=", dataUsed)
				println("  w0=", firstWords[0], "w1=", firstWords[1], "w2=", firstWords[2])
			}
		}

		// Check for commands
		if machine.Serial.Buffered() > 0 {
			b, _ := machine.Serial.ReadByte()
			switch b {
			case 'r':
				println("Rebooting...")
				time.Sleep(100 * time.Millisecond)
				machine.EnterBootloader()
			case 'd':
				println("=== DEBUG ===")
				println("ShowFrame[0-7]:", g.ShowFrame[0], g.ShowFrame[1], g.ShowFrame[2], g.ShowFrame[3],
					g.ShowFrame[4], g.ShowFrame[5], g.ShowFrame[6], g.ShowFrame[7])
				println("Palette[0]:", uint16(g.Palette[0]), "Palette[1]:", uint16(g.Palette[1]))
				println("UseColorRun:", gvga.UseColorRunFor1BPP)
			case 'c':
				gvga.UseColorRunFor1BPP = true
				println("Switched to COLOR_RUN mode")
			case 'w':
				gvga.UseColorRunFor1BPP = false
				println("Switched to RAW_RUN mode")
			}
		}

		time.Sleep(10 * time.Millisecond)
	}
}
