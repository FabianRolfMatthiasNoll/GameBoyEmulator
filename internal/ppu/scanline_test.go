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

func TestScanlineFetcherMapBase9C00(t *testing.T) {
	mapBase := uint16(0x9C00)
	mem := mockVRAM{}
	// first tiles 2,3
	mem[mapBase+0] = 2
	mem[mapBase+1] = 3
	fineY := byte(0)
	base2 := uint16(0x8000+2*16) + uint16(fineY)*2
	mem[base2] = 0x3C
	mem[base2+1] = 0xA0
	base3 := uint16(0x8000+3*16) + uint16(fineY)*2
	mem[base3] = 0x0F
	mem[base3+1] = 0xF0
	out := RenderBGScanlineUsingFetcher(mem, mapBase, true, 0, 0, 0)
	// First 8 from tile2
	lo, hi := byte(0x3C), byte(0xA0)
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi>>b)&1)<<1 | ((lo >> b) & 1)
		if out[i] != want {
			t.Fatalf("tile2 px %d got %d want %d", i, out[i], want)
		}
	}
	// Next 8 from tile3
	lo, hi = 0x0F, 0xF0
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi>>b)&1)<<1 | ((lo >> b) & 1)
		if out[8+i] != want {
			t.Fatalf("tile3 px %d got %d want %d", i, out[8+i], want)
		}
	}
}

func TestScanlineFetcherSignedTileData8800(t *testing.T) {
	mapBase := uint16(0x9800)
	mem := mockVRAM{}
	// tile index -1 (0xFF) at first column
	mem[mapBase+0] = 0xFF
	// For 0x8800 addressing with tile=-1: base row at 0x8FF0
	rowAddr := uint16(0x8FF0)
	mem[rowAddr] = 0xC3
	mem[rowAddr+1] = 0x18
	out := RenderBGScanlineUsingFetcher(mem, mapBase, false, 0, 0, 0)
	lo, hi := byte(0xC3), byte(0x18)
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi>>b)&1)<<1 | ((lo >> b) & 1)
		if out[i] != want {
			t.Fatalf("signed tile px %d got %d want %d", i, out[i], want)
		}
	}
}
