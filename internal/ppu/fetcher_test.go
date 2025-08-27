package ppu

import "testing"

func TestFIFO(t *testing.T) {
	var q fifo
	if q.Len() != 0 {
		t.Fatal("new fifo not empty")
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("pop from empty should fail")
	}
	for i := 0; i < 32; i++ {
		if !q.Push(byte(i)) {
			t.Fatal("unexpected full")
		}
	}
	if q.Push(0) {
		t.Fatal("should be full")
	}
	for i := 0; i < 32; i++ {
		v, ok := q.Pop()
		if !ok {
			t.Fatal("unexpected empty")
		}
		if v != byte(i)&3 {
			t.Fatalf("got %d want %d", v, byte(i)&3)
		}
	}
}

type mockVRAM map[uint16]byte

func (m mockVRAM) Read(addr uint16) byte { return m[addr] }

func TestBGFetcherFetchesEightPixels(t *testing.T) {
	// Construct a tile row that yields ci = 0..3 pattern across 8 pixels: 00,01,10,11,...
	// lo: 01010101 (0x55), hi: 00110011 (0x33) -> ci sequence: 1,2,1,2,...? Actually compute explicitly.
	mem := mockVRAM{}
	// tile index addr -> tileNum=0
	mem[0x9800] = 0
	// tile row at 0x8000
	mem[0x8000] = 0x55
	mem[0x8001] = 0x33
	var q fifo
	f := newBGFetcher(mem, &q)
	f.Configure(0x9800, true, 0x9800, 0)
	f.Fetch()
	if q.Len() != 8 {
		t.Fatalf("expected 8 pixels in fifo, got %d", q.Len())
	}
	// Manually compute expected cis from lo=0x55, hi=0x33
	lo, hi := byte(0x55), byte(0x33)
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi>>b)&1)<<1 | ((lo >> b) & 1)
		got, _ := q.Pop()
		if got != want {
			t.Fatalf("px %d got %d want %d", i, got, want)
		}
	}
}

func TestBGFetcherSignedTileAddressing8800(t *testing.T) {
	mem := mockVRAM{}
	// map at 0x9C00 points to tile index 0xFF (-1)
	mapBase := uint16(0x9C00)
	mem[mapBase] = 0xFF
	// For 0x8800 signed addressing, index 0 is at 0x9000; -1 => 0x8FF0
	fineY := byte(5) // row 5 -> offset 10 bytes into tile (each row 2 bytes)
	rowAddr := uint16(0x8FF0) + uint16(fineY)*2
	lo, hi := byte(0xA5), byte(0x5A)
	mem[rowAddr] = lo
	mem[rowAddr+1] = hi

	var q fifo
	f := newBGFetcher(mem, &q)
	// tileData8000=false => use 0x8800 signed addressing
	f.Configure(mapBase, false, mapBase, fineY)
	f.Fetch()
	if q.Len() != 8 {
		t.Fatalf("expected 8 pixels in fifo, got %d", q.Len())
	}
	for i := 0; i < 8; i++ {
		b := 7 - byte(i)
		want := ((hi>>b)&1)<<1 | ((lo >> b) & 1)
		got, _ := q.Pop()
		if got != want {
			t.Fatalf("px %d got %d want %d", i, got, want)
		}
	}
}
