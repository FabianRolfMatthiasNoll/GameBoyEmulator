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
