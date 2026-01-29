//go:build !debug

package main

import (
	"machine"

	"github.com/0magnet/pico-vga/gvga"
)

const debugEnabled = false

func initSerial() {
	// No-op when debug disabled
}

func debugPrint(args ...interface{}) {
	// No-op when debug disabled
}

func checkSerialCommands(g *gvga.GVga, led machine.Pin) {
	// No-op when debug disabled
}

func printLoopStats(loopCount int, g *gvga.GVga) {
	// No-op when debug disabled
}
