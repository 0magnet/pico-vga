//go:build !debug

package main

// Release build - debug output disabled
const debugEnabled = false

func debugPrint(args ...interface{}) {}
func debugHex(name string, val uint32) {}
func debugRegister(name string, val uint32, fields map[string]struct{ shift, mask uint32 }) {}
