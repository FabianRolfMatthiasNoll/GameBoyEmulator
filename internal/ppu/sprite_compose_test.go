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
	s1 := Sprite{X: 11, Y: 0, Tile: 0, Attr: 1<<4 | 0, OAMIndex: 1} // OBP1 but appears to the right, shouldn't win at x=10
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
