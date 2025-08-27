package ppu

import "testing"

func TestScanlineFetcherSCXOffsetAndTileWrap(t *testing.T) {
	// Build a 32-tile row map at 0x9800 with sequential tile numbers 0..31.
	mapBase := uint16(0x9800)
	mem := mockVRAM{}
	fineY := byte(0)
	for tile := 0; tile < 32; tile++ {
		// map index
		mem[mapBase+uint16(tile)] = byte(tile)
		// tile row bytes at 0x8000 addressing
		base := uint16(0x8000+tile*16) + uint16(fineY)*2
		lo := byte(tile)
		hi := ^byte(tile)
		mem[base] = lo
		mem[base+1] = hi
	}

	// scx=5 should discard first 5 pixels of tile 0, then continue; 160 px output
	out := renderBGScanlineUsingFetcher(mem, mapBase, true, 5, 0, 0)
	// Validate the first 8-5=3 pixels match tile0 bits 2..0 and next pixels come from tile1 etc.
	lo0, hi0 := byte(0), ^byte(0)
	for i := 0; i < 3; i++ {
		b := 2 - byte(i)
		want := ((hi0>>b)&1)<<1 | ((lo0 >> b) & 1)
		if out[i] != want {
			t.Fatalf("px %d got %d want %d", i, out[i], want)
		}
	}
	lo1, hi1 := byte(1), ^byte(1)
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi1>>b)&1)<<1 | ((lo1 >> b) & 1)
		if out[3+i] != want {
			t.Fatalf("tile1 px %d got %d want %d", i, out[3+i], want)
		}
	}
}
