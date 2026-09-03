//go:build debug

package main

// Debug build - verbose output enabled
const debugEnabled = true

func debugPrint(args ...interface{}) {
	if debugEnabled {
		// Print each argument with space separator
		for i, arg := range args {
			if i > 0 {
				print(" ")
			}
			switch v := arg.(type) {
			case string:
				print(v)
			case int:
				print(v)
			case uint32:
				print(hex(v))
			case bool:
				if v {
					print("true")
				} else {
					print("false")
				}
			default:
				print("?")
			}
		}
		println()
	}
}

func debugHex(name string, val uint32) {
	if debugEnabled {
		println(name, hex(val))
	}
}

func debugRegister(name string, val uint32, fields map[string]struct{ shift, mask uint32 }) {
	if debugEnabled {
		println(name, "=", hex(val))
		for fname, f := range fields {
			fval := (val >> f.shift) & f.mask
			println("  ", fname, "=", fval)
		}
	}
}
