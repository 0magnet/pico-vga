//go:build debug

package main

import (
	"machine"
	"time"

	"github.com/0magnet/pico-vga/gvga"
	"github.com/0magnet/pico-vga/scanvideo"
)

const debugEnabled = true

func initSerial() {
	machine.Serial.Configure(machine.UARTConfig{BaudRate: 115200})
	time.Sleep(500 * time.Millisecond)
}

func debugPrint(args ...interface{}) {
	println(args...)
}

func checkSerialCommands(g *gvga.GVga, led machine.Pin) {
	if machine.Serial.Buffered() > 0 {
		b, _ := machine.Serial.ReadByte()
		handleSerialCommand(b, g, led)
	}
}

func handleSerialCommand(b byte, g *gvga.GVga, led machine.Pin) {
	switch b {
	case 'r':
		println("Rebooting...")
		time.Sleep(100 * time.Millisecond)
		machine.EnterBootloader()
	case 'f':
		freezeAnimation = !freezeAnimation
		if freezeAnimation {
			println("Animation FROZEN")
		} else {
			println("Animation RESUMED")
		}
	case 'b':
		println("=== FRAMEBUFFER DEBUG ===")
		println("ShowFrame len=", len(g.ShowFrame), "DrawFrame len=", len(g.DrawFrame))
		println("ShowFrame[0:8]=", g.ShowFrame[0], g.ShowFrame[1], g.ShowFrame[2], g.ShowFrame[3],
			g.ShowFrame[4], g.ShowFrame[5], g.ShowFrame[6], g.ShowFrame[7])
		idx100 := 100 * int(g.Width) / 8
		println("ShowFrame[scanline100]=", g.ShowFrame[idx100], g.ShowFrame[idx100+1])
		println("Palette[0]=", uint16(g.Palette[0]), "Palette[1]=", uint16(g.Palette[1]))
		println("=========================")
	case 'd':
		println("=== DEBUG STATS ===")
		stats := scanvideo.GetDebugStats()
		bufs := scanvideo.GetBufferCounts()
		println("Render:", g.RenderCount)
		println("IRQ0:", stats.IRQ0Count, "IRQ1:", stats.IRQ1Count)
		println("Hits:", stats.BufferHits, "Miss:", stats.BufferMisses)
		println("Bufs: free=", bufs.Free, "gen=", bufs.Generated)
		println("===================")
	}
}

func printLoopStats(loopCount int, g *gvga.GVga) {
	if loopCount%100 == 0 {
		stats := scanvideo.GetDebugStats()
		bufs := scanvideo.GetBufferCounts()
		println("loop:", loopCount, "render:", g.RenderCount)
		println("  Hits:", stats.BufferHits, "Miss:", stats.BufferMisses, "Frames:", stats.FramesComplete)
		println("  Bufs: free=", bufs.Free, "gen=", bufs.Generated)
	}
}
