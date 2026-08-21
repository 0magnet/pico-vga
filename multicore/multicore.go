// Package multicore provides direct core1 launch for RP2040
// NOTE: Currently disabled - TinyGo's -scheduler=cores has issues
// Use -scheduler=tasks with goroutines instead
package multicore

// Core1Func is the function type for core1 entry point
type Core1Func func()

// LaunchCore1 is a stub - multicore not currently supported
// Use goroutines with -scheduler=tasks instead
func LaunchCore1(fn Core1Func) bool {
	// Not implemented - TinyGo multicore has issues
	return false
}
