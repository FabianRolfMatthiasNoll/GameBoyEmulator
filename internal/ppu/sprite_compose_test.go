package ppu

import "testing"

func TestComposeSpriteLinePriorityAndTransparency(t *testing.T) {
	mem := mockVRAM{}
	// Sprite tile with a single opaque leftmost pixel at bit7: lo=0x01<<7 -> 0x80, hi=0
	base := uint16(0x8000)
	mem[base+0] = 0x80
	mem[base+1] = 0x00
	sprites := []Sprite{{X: 10, Y: 5, Tile: 0, Attr: 0, OAMIndex: 0}}
	var bgci [160]byte
	out := ComposeSpriteLine(mem, sprites, 5, bgci, false)
	if out[10] == 0 {
		t.Fatalf("expected sprite pixel at x=10")
	}
	// With priority behind BG and bgci non-zero, pixel must be skipped
	sprites[0].Attr = 1 << 7
	bgci[10] = 1
	out = ComposeSpriteLine(mem, sprites, 5, bgci, false)
	if out[10] != 0 {
		t.Fatalf("expected sprite pixel to be hidden behind BG")
	}
}

func TestComposeSpriteLineTieBreaker(t *testing.T) {
	mem := mockVRAM{}
	// Two sprites overlap at x=20; both opaque full row (lo=0xFF, hi=0)
	base := uint16(0x8000)
	mem[base+0] = 0xFF
	mem[base+1] = 0x00
	s0 := Sprite{X: 19, Y: 0, Tile: 0, Attr: 0, OAMIndex: 5}
	s1 := Sprite{X: 20, Y: 0, Tile: 0, Attr: 0, OAMIndex: 3}
	var bgci [160]byte
	out := ComposeSpriteLine(mem, []Sprite{s0, s1}, 0, bgci, false)
	// At x=20, s0 contributes col=1 (exists) and s1 contributes col=0; leftmost X wins -> s1 (X=20) should win
	if out[20] == 0 {
		t.Fatalf("expected a sprite at x=20")
	}
}

func TestComposeSpriteLinePaletteSelection(t *testing.T) {
	mem := mockVRAM{}
	base := uint16(0x8000)
	// Make an opaque pixel at bit7
	mem[base+0] = 0x80
	mem[base+1] = 0x00
	// Two overlapping sprites at same X; one selects OBP0, the other OBP1; leftmost X rule should pick X=10
	s0 := Sprite{X: 10, Y: 0, Tile: 0, Attr: 0 << 4, OAMIndex: 2}   // OBP0
	s1 := Sprite{X: 11, Y: 0, Tile: 0, Attr: 1 << 4, OAMIndex: 1} // OBP1 but appears to the right, shouldn't win at x=10
	var bgci [160]byte
	ci, pal := ComposeSpriteLineExt(mem, []Sprite{s0, s1}, 0, bgci, false)
	if ci[10] == 0 {
		t.Fatalf("expected sprite pixel at x=10")
	}
	if pal[10] != 0 {
		t.Fatalf("expected OBP0 at x=10, got pal=%d", pal[10])
	}
	// Now put both with same X but different OAM index; lower OAM index should win and carry its palette
	s0 = Sprite{X: 12, Y: 0, Tile: 0, Attr: 0 << 4, OAMIndex: 5} // OBP0, higher index
	s1 = Sprite{X: 12, Y: 0, Tile: 0, Attr: 1 << 4, OAMIndex: 3} // OBP1, lower index
	ci, pal = ComposeSpriteLineExt(mem, []Sprite{s0, s1}, 0, bgci, false)
	if ci[12] == 0 {
		t.Fatalf("expected sprite pixel at x=12")
	}
	if pal[12] != 1 {
		t.Fatalf("expected OBP1 at x=12 due to lower OAM index, got pal=%d", pal[12])
	}
}

func TestComposeSpriteLine16YFlip(t *testing.T) {
	mem := mockVRAM{}
	// Create two tiles for 8x16: tile 0 (top) opaque at bit7; tile 1 (bottom) opaque at bit6
	base := uint16(0x8000)
	// Top tile row 0: bit7 set
	mem[base+0] = 0x80
	mem[base+1] = 0x00
	// Bottom tile row 7: bit6 set (so y-flip row=0 maps to rowWithin=7)
	mem[base+16+14] = 0x40 // lo for row7
	mem[base+16+15] = 0x00 // hi for row7
	var bgci [160]byte
	// Non-flipped: at lineY=0 we should sample top tile row0 -> x=10 should be opaque at bit7
	s := Sprite{X: 10, Y: 0, Tile: 0, Attr: 0, OAMIndex: 0}
	ci, _ := ComposeSpriteLineExt(mem, []Sprite{s}, 0, bgci, true)
	if ci[10] == 0 {
		t.Fatalf("expected opaque pixel from top tile at x=10")
	}
	// With Y-flip at lineY=0 (row=0 -> 15), we read bottom tile rowWithin=7 where bit6 is set
	s = Sprite{X: 10, Y: 0, Tile: 0, Attr: 1 << 6, OAMIndex: 0} // y-flip
	ci, _ = ComposeSpriteLineExt(mem, []Sprite{s}, 0, bgci, true)
	// At x=11 (bit6), should be opaque due to bottom tile row7 pattern
	if ci[11] == 0 {
		t.Fatalf("expected opaque pixel from bottom tile at x=11 with Y-flip")
	}
}

func TestComposeSpriteLinePriorityBehindVsFront(t *testing.T) {
	mem := mockVRAM{}
	base := uint16(0x8000)
	// Opaque full row for simplicity
	mem[base+0] = 0xFF
	mem[base+1] = 0x00
	var bgci [160]byte
	bgci[20] = 1 // non-zero BG at x=20
	// Sprite A behind BG at x=20
	a := Sprite{X: 20, Y: 0, Tile: 0, Attr: 1 << 7, OAMIndex: 1}
	// Sprite B in front at same pixel, should be drawn
	b := Sprite{X: 20, Y: 0, Tile: 0, Attr: 0, OAMIndex: 2}
	ci, _ := ComposeSpriteLineExt(mem, []Sprite{a, b}, 0, bgci, false)
	if ci[20] == 0 {
		t.Fatalf("expected front sprite to draw over BG when another is behind BG")
	}
}

func TestComposeSpriteLineXFlip(t *testing.T) {
	mem := mockVRAM{}
	base := uint16(0x8000)
	// Row with only bit0 set (rightmost pixel when not flipped)
	mem[base+0] = 0x01
	mem[base+1] = 0x00
	var bgci [160]byte
	s := Sprite{X: 10, Y: 0, Tile: 0, Attr: 0, OAMIndex: 0}
	ci, _ := ComposeSpriteLineExt(mem, []Sprite{s}, 0, bgci, false)
	if ci[17] == 0 { // X=10 + col7 should be bit0 -> far right
		t.Fatalf("expected opaque pixel at x=17 without X-flip")
	}
	// With X-flip, bit0 should map to leftmost (col7 flipped to col0)
	s = Sprite{X: 10, Y: 0, Tile: 0, Attr: 1 << 5, OAMIndex: 0}
	ci, _ = ComposeSpriteLineExt(mem, []Sprite{s}, 0, bgci, false)
	if ci[10] == 0 {
		t.Fatalf("expected opaque pixel at x=10 with X-flip")
	}
}

func TestComposeSpriteLineMixedFlips16(t *testing.T) {
	mem := mockVRAM{}
	base := uint16(0x8000)
	// Define top tile: opaque at bit7; bottom tile: opaque at bit0
	mem[base+0] = 0x80
	mem[base+1] = 0x00
	mem[base+16+14] = 0x01 // bottom tile row7, bit0
	mem[base+16+15] = 0x00
	var bgci [160]byte
	// X+Y flip: at lineY=0 in 8x16, expect bottom tile row7 selected and bit0 mirrored to leftmost
	s := Sprite{X: 20, Y: 0, Tile: 0, Attr: (1 << 6) | (1 << 5), OAMIndex: 0}
	ci, _ := ComposeSpriteLineExt(mem, []Sprite{s}, 0, bgci, true)
	if ci[20] == 0 { // leftmost should be opaque after X-flip
		t.Fatalf("expected opaque pixel at x=20 with X+Y flip in 8x16")
	}
}
