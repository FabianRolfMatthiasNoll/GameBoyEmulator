package ppu

import "testing"

func TestWindowScanlineFetcherWXAndTiles(t *testing.T) {
	mem := mockVRAM{}
	mapBase := uint16(0x9800)
	// window map first two tiles 0,1
	mem[mapBase+0] = 0
	mem[mapBase+1] = 1
	// fineY=2 row bytes
	fineY := byte(2)
	base0 := uint16(0x8000) + 0*16 + uint16(fineY)*2
	mem[base0] = 0xAA
	mem[base0+1] = 0x0F
	base1 := uint16(0x8000) + 1*16 + uint16(fineY)*2
	mem[base1] = 0x55
	mem[base1+1] = 0xF0
	// WX-7 start at 20
	out := RenderWindowScanlineUsingFetcher(mem, mapBase, true, 20, fineY)
	// Before 20 must remain 0
	for x := 0; x < 20; x++ {
		if out[x] != 0 {
			t.Fatalf("pre-window px %d = %d, want 0", x, out[x])
		}
	}
	// First 8 window pixels from tile0
	lo0, hi0 := byte(0xAA), byte(0x0F)
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi0>>b)&1)<<1 | ((lo0 >> b) & 1)
		if out[20+i] != want {
			t.Fatalf("tile0 px %d got %d want %d", i, out[20+i], want)
		}
	}
	// Next 8 from tile1
	lo1, hi1 := byte(0x55), byte(0xF0)
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi1>>b)&1)<<1 | ((lo1 >> b) & 1)
		if out[28+i] != want {
			t.Fatalf("tile1 px %d got %d want %d", i, out[28+i], want)
		}
	}
}
