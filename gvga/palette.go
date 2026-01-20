package gvga

// Default 16-color palette
var DefaultPalette16 = []Color{
	Black, Red,     // 1-bit colors
	Green, Blue,    // additional colors for 2-bits
	Yellow, Cyan, Magenta, White, // additional colors for 3-bits
	RGB5(15, 15, 15), RGB5(15, 0, 0), RGB5(0, 15, 0), RGB5(0, 0, 15), // half-bright
	RGB5(15, 15, 0), RGB5(0, 15, 15), RGB5(15, 15, 0), RGB5(7, 7, 7), // half-bright
}

// computePalette1BPP pre-computes palette lookup for 1bpp mode
// For each of 256 possible byte values, compute 8 palette lookups (one per pixel)
func (g *GVga) computePalette1BPP() {
	if g.PaletteBuf == nil {
		g.PaletteBuf = make([]uint16, 256*8)
	}
	for i := 0; i < 256; i++ {
		for j := 0; j < 8; j++ {
			bit := uint8(1 << (7 - j))
			var index uint8
			if i&int(bit) != 0 {
				index = 1
			}
			g.PaletteBuf[i*8+j] = uint16(g.Palette[index])
		}
	}
}

// computePalette2BPP pre-computes palette lookup for 2bpp mode
// For each of 256 possible byte values, compute 4 palette lookups
func (g *GVga) computePalette2BPP() {
	if g.PaletteBuf == nil {
		g.PaletteBuf = make([]uint16, 256*4)
	}
	for i := 0; i < 256; i++ {
		chew0 := (i & 0xC0) >> 6
		chew1 := (i & 0x30) >> 4
		chew2 := (i & 0x0C) >> 2
		chew3 := i & 0x03
		g.PaletteBuf[i*4+0] = uint16(g.Palette[chew0])
		g.PaletteBuf[i*4+1] = uint16(g.Palette[chew1])
		g.PaletteBuf[i*4+2] = uint16(g.Palette[chew2])
		g.PaletteBuf[i*4+3] = uint16(g.Palette[chew3])
	}
}

// computePalette4BPP pre-computes palette lookup for 4bpp mode
// For each of 256 possible byte values, compute 2 palette lookups
func (g *GVga) computePalette4BPP() {
	if g.PaletteBuf == nil {
		g.PaletteBuf = make([]uint16, 256*2)
	}
	for i := 0; i < 256; i++ {
		nybble0 := (i & 0xF0) >> 4
		nybble1 := i & 0x0F
		g.PaletteBuf[i*2+0] = uint16(g.Palette[nybble0])
		g.PaletteBuf[i*2+1] = uint16(g.Palette[nybble1])
	}
}

// SetPalette sets palette entries starting at 'start' index
func (g *GVga) SetPalette(palette []Color, start, count int) {
	for i := 0; i < count && start+i < len(g.Palette); i++ {
		g.Palette[start+i] = palette[i]
	}

	// Set border colors to first palette entry
	g.BorderColors[BorderTop] = g.Palette[0]
	g.BorderColors[BorderBottom] = g.Palette[0]
	g.BorderColors[BorderLeft] = g.Palette[0]
	g.BorderColors[BorderRight] = g.Palette[0]

	// Recompute palette lookup table
	switch g.Bits {
	case 1:
		g.computePalette1BPP()
	case 2:
		g.computePalette2BPP()
	case 4:
		g.computePalette4BPP()
	// 8bpp doesn't need pre-computation (direct palette indexing)
	}
}

// SetBorderColors sets the border colors for all four sides
func (g *GVga) SetBorderColors(top, bottom, left, right Color) {
	g.BorderColors[BorderTop] = top
	g.BorderColors[BorderBottom] = bottom
	g.BorderColors[BorderLeft] = left
	g.BorderColors[BorderRight] = right
}
