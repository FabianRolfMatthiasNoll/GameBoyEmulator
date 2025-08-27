package ppu

// renderBGScanlineUsingFetcher renders 160 BG pixels for the given LY using the isolated fetcher.
// Inputs:
// - mem: VRAM reader
// - mapBase: 0x9800 or 0x9C00
// - tileData8000: true -> 0x8000 addressing; false -> 0x8800 signed addressing
// - scx, scy: scroll registers
// - ly: current scanline (0..143)
// Output: 160 color indices (0..3)
func RenderBGScanlineUsingFetcher(mem VRAMReader, mapBase uint16, tileData8000 bool, scx, scy, ly byte) [160]byte {
	var out [160]byte

	// Compute BG coordinates.
	bgY := uint16(ly) + uint16(scy)
	fineY := byte(bgY & 7)
	mapY := (bgY >> 3) & 31 // 0..31 rows

	startX := uint16(scx)
	tileX := (startX >> 3) & 31
	fineX := int(startX & 7)

	// Map index address for the first tile column.
	tileIndexAddr := mapBase + mapY*32 + tileX

	var q fifo
	f := newBGFetcher(mem, &q)
	f.Configure(mapBase, tileData8000, tileIndexAddr, fineY)
	f.Fetch()
	// Discard scx fractional pixels.
	for i := 0; i < fineX; i++ {
		_, _ = q.Pop()
	}

	// Produce 160 pixels, fetching new tiles as the FIFO empties.
	for x := 0; x < 160; x++ {
		if q.Len() == 0 {
			// Advance to next tile in map row (wrap at 32 tiles).
			tileX = (tileX + 1) & 31
			tileIndexAddr = mapBase + mapY*32 + tileX
			f.Configure(mapBase, tileData8000, tileIndexAddr, fineY)
			f.Fetch()
		}
		px, _ := q.Pop()
		out[x] = px
	}
	return out
}

// RenderWindowScanlineUsingFetcher renders the window layer for a scanline using the fetcher.
// It fills pixels starting at wxStart (WX-7) using winLine as the vertical line within the window.
// Pixels before wxStart are left as 0 (BG color index 0) so callers can blend.
func RenderWindowScanlineUsingFetcher(mem VRAMReader, mapBase uint16, tileData8000 bool, wxStart int, winLine byte) [160]byte {
	var out [160]byte
	if wxStart >= 160 {
		return out
	}
	if wxStart < 0 {
		wxStart = 0
	}
	// Compute window tile row and fineY
	mapY := (uint16(winLine) >> 3) & 31
	fineY := winLine & 7
	tileX := uint16(0)
	tileIndexAddr := mapBase + mapY*32 + tileX
	var q fifo
	f := newBGFetcher(mem, &q)
	f.Configure(mapBase, tileData8000, tileIndexAddr, fineY)
	f.Fetch()
	for x := wxStart; x < 160; x++ {
		if q.Len() == 0 {
			tileX = (tileX + 1) & 31
			tileIndexAddr = mapBase + mapY*32 + tileX
			f.Configure(mapBase, tileData8000, tileIndexAddr, fineY)
			f.Fetch()
		}
		px, _ := q.Pop()
		out[x] = px
	}
	return out
}
