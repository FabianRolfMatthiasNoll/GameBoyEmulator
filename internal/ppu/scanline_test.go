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
	out := RenderBGScanlineUsingFetcher(mem, mapBase, true, 5, 0, 0)
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

func TestScanlineFetcherSCYRowSelectAndMapWrap(t *testing.T) {
	// We want bgY = ly+scy to select map row 1 (i.e., 8..15) and fineY=3
	// Choose ly=0, scy=11 -> bgY=11, mapY=1, fineY=3
	mapBase := uint16(0x9800)
	mem := mockVRAM{}
	fineY := byte(3)
	// two tiles at start of map row 1 (offset 32)
	mem[mapBase+32+0] = 0
	mem[mapBase+32+1] = 1
	// populate tile rows for fineY=3
	base0 := uint16(0x8000+0*16) + uint16(fineY)*2
	mem[base0] = 0x12
	mem[base0+1] = 0x34
	base1 := uint16(0x8000+1*16) + uint16(fineY)*2
	mem[base1] = 0x56
	mem[base1+1] = 0x78
	out := RenderBGScanlineUsingFetcher(mem, mapBase, true, 0, 11, 0)
	// First 8 pixels = tile0 row (0x12,0x34)
	lo0, hi0 := byte(0x12), byte(0x34)
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi0>>b)&1)<<1 | ((lo0 >> b) & 1)
		if out[i] != want {
			t.Fatalf("tile0 px %d got %d want %d", i, out[i], want)
		}
	}
	// Next 8 pixels = tile1 row (0x56,0x78)
	lo1, hi1 := byte(0x56), byte(0x78)
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi1>>b)&1)<<1 | ((lo1 >> b) & 1)
		if out[8+i] != want {
			t.Fatalf("tile1 px %d got %d want %d", i, out[8+i], want)
		}
	}
}
