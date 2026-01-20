package gvga

// Graphics primitives for GVga

// Set sets a single pixel at (x, y) to the given pen color
func (g *GVga) Set(x, y int, pen uint16) {
	if x < 0 || x >= int(g.Width) || y < 0 || y >= int(g.Height) {
		return
	}
	switch g.Bits {
	case 1:
		g.set1bpp(x, y, pen)
	case 2:
		g.set2bpp(x, y, pen)
	case 4:
		g.set4bpp(x, y, pen)
	case 8:
		g.set8bpp(x, y, pen)
	}
}

func (g *GVga) set1bpp(x, y int, pen uint16) {
	row := y * int(g.Width) / 8
	byteIdx := row + x/8
	mask := uint8(1 << (7 - (x % 8)))
	if pen != 0 {
		g.DrawFrame[byteIdx] |= mask
	} else {
		g.DrawFrame[byteIdx] &^= mask
	}
}

func (g *GVga) set2bpp(x, y int, pen uint16) {
	row := y * int(g.Width) / 4
	byteIdx := row + x/4
	shift := uint((3 - (x % 4)) * 2)
	mask := uint8(0x03 << shift)
	g.DrawFrame[byteIdx] = (g.DrawFrame[byteIdx] &^ mask) | uint8((pen&0x03)<<shift)
}

func (g *GVga) set4bpp(x, y int, pen uint16) {
	row := y * int(g.Width) / 2
	byteIdx := row + x/2
	if x%2 == 0 {
		// High nibble
		g.DrawFrame[byteIdx] = (g.DrawFrame[byteIdx] & 0x0F) | uint8((pen&0x0F)<<4)
	} else {
		// Low nibble
		g.DrawFrame[byteIdx] = (g.DrawFrame[byteIdx] & 0xF0) | uint8(pen&0x0F)
	}
}

func (g *GVga) set8bpp(x, y int, pen uint16) {
	byteIdx := y*int(g.Width) + x
	g.DrawFrame[byteIdx] = uint8(pen)
}

// Line draws a line from (x0, y0) to (x1, y1) using Bresenham's algorithm
func (g *GVga) Line(x0, y0, x1, y1 int, pen uint16) {
	dx := abs(x1 - x0)
	dy := abs(y1 - y0)
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
	if y0 >= y1 {
		sy = -1
	}
	err := dx - dy

	for {
		g.Set(x0, y0, pen)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Clear fills the entire frame buffer with the given pen color
func (g *GVga) Clear(pen uint16) {
	var fillByte uint8
	switch g.Bits {
	case 1:
		if pen != 0 {
			fillByte = 0xFF
		} else {
			fillByte = 0x00
		}
	case 2:
		fillByte = uint8((pen & 0x03) | ((pen & 0x03) << 2) | ((pen & 0x03) << 4) | ((pen & 0x03) << 6))
	case 4:
		fillByte = uint8((pen & 0x0F) | ((pen & 0x0F) << 4))
	case 8:
		fillByte = uint8(pen)
	}
	for i := range g.DrawFrame {
		g.DrawFrame[i] = fillByte
	}
}

// Box draws a rectangle outline from (x0, y0) to (x1, y1)
func (g *GVga) Box(x0, y0, x1, y1 int, pen uint16) {
	g.Line(x0, y0, x1, y0, pen) // Top
	g.Line(x1, y0, x1, y1, pen) // Right
	g.Line(x1, y1, x0, y1, pen) // Bottom
	g.Line(x0, y1, x0, y0, pen) // Left
}

// BoxFill draws a filled rectangle from (x0, y0) to (x1, y1)
func (g *GVga) BoxFill(x0, y0, x1, y1 int, pen uint16) {
	for y := y0; y <= y1; y++ {
		g.Line(x0, y, x1, y, pen)
	}
}

// Lookup tables for character rendering
var bitDoubler = [16]uint8{0, 3, 12, 15, 48, 51, 60, 63, 192, 195, 204, 207, 240, 243, 252, 255}
var bitQuadrupler = [4]uint8{0, 0x0F, 0xF0, 0xFF}

// Char draws a character at pixel position (x, y)
func (g *GVga) Char(x, y int, c uint8, pen uint16) {
	if g.Font == nil {
		return
	}
	pen = pen & (g.Colors - 1)
	var penMask uint8 = 0xFF
	row := y * int(g.RowBytes)
	col := x / int(g.PixelsPerByte)

	switch g.Bits {
	case 1:
		if pen != 0 {
			penMask = 0xFF
		} else {
			penMask = 0x00
		}
	case 2:
		penMask = uint8(pen | pen<<2 | pen<<4 | pen<<6)
	case 4:
		penMask = uint8(pen | pen<<4)
	case 8:
		penMask = uint8(pen)
	}

	fontData := g.Font.Data[int(c)*int(g.Font.Height):]
	for i := 0; i < int(g.Font.Height); i++ {
		byteVal := fontData[i]
		ptr := row + col
		if ptr >= len(g.DrawFrame) {
			break
		}
		switch g.Bits {
		case 1:
			g.DrawFrame[ptr] = byteVal & penMask
		case 2:
			if ptr+1 < len(g.DrawFrame) {
				g.DrawFrame[ptr] = bitDoubler[byteVal/16] & penMask
				g.DrawFrame[ptr+1] = bitDoubler[byteVal%16] & penMask
			}
		case 4:
			if ptr+3 < len(g.DrawFrame) {
				g.DrawFrame[ptr] = bitQuadrupler[(byteVal&0xC0)>>6] & penMask
				g.DrawFrame[ptr+1] = bitQuadrupler[(byteVal&0x30)>>4] & penMask
				g.DrawFrame[ptr+2] = bitQuadrupler[(byteVal&0x0C)>>2] & penMask
				g.DrawFrame[ptr+3] = bitQuadrupler[byteVal&0x03] & penMask
			}
		case 8:
			if ptr+7 < len(g.DrawFrame) {
				for j := 0; j < 8; j++ {
					if byteVal&(1<<(7-j)) != 0 {
						g.DrawFrame[ptr+j] = uint8(pen)
					} else {
						g.DrawFrame[ptr+j] = 0
					}
				}
			}
		}
		row += int(g.RowBytes)
	}
}

// Text draws a string at pixel position (x, y)
func (g *GVga) Text(x, y int, text string, pen uint16) {
	for _, c := range text {
		g.Char(x, y, uint8(c), pen)
		x += 8
	}
}
