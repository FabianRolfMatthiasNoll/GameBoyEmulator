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

// Sprite describes the minimal fields required to composite a sprite onto a scanline.
type Sprite struct {
	X        int  // screen X (0..159)
	Y        int  // screen Y top (0..143). The function uses lineY to compute row.
	Tile     byte // tile index (0..255), 0x8000 addressing
	Attr     byte // bit7 priority, bit6 yflip, bit5 xflip, bit4 palette select
	OAMIndex int  // index in OAM for tie-breaking
}

// ComposeSpriteLine returns per-pixel OBJ color indices for a scanline (0 means transparent/no OBJ).
// It applies OBJ-to-BG priority: if sprite has priority bit set (behind BG) and bgci[x]!=0, the pixel is skipped.
// It also enforces leftmost-X and then OAM index as tie-breakers for overlapping sprites at a given x.
func ComposeSpriteLineExt(mem VRAMReader, sprites []Sprite, lineY int, bgci [160]byte, sprite16 bool) (ciOut [160]byte, palSel [160]byte) {
	// Track, per screen X, the sprite X chosen (for leftmost-X rule) and its OAM index
	var bestSX [160]int
	var bestIdx [160]int
	for i := 0; i < 160; i++ {
		bestSX[i] = 9999
		bestIdx[i] = 9999
	}
	for _, s := range sprites {
		// Determine if this sprite covers the scanline
		height := 8
		if sprite16 {
			height = 16
		}
		if lineY < s.Y || lineY >= s.Y+height {
			continue
		}
		// Quick reject if completely off-screen horizontally
		if s.X >= 160 || s.X+7 < 0 {
			continue
		}
		row := lineY - s.Y
		// vertical flip
		if (s.Attr & (1 << 6)) != 0 {
			if sprite16 {
				row = height - 1 - row
			} else {
				row = 7 - row
			}
		}
		// Select tile index and row within tile
		tIndex := s.Tile
		if sprite16 {
			tIndex &= 0xFE
			if row >= 8 {
				tIndex++
			}
		}
		rowWithin := row & 7
		base := uint16(0x8000) + uint16(tIndex)*16 + uint16(rowWithin)*2
		lo := mem.Read(base)
		hi := mem.Read(base + 1)
		// Precompute color indices for the 8 pixels in this row (left to right)
		var rowPix [8]byte
		for b := 0; b < 8; b++ {
			bit := 7 - byte(b)
			rowPix[b] = ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
		}
		for col := 0; col < 8; col++ {
			screenX := s.X + col
			if screenX < 0 || screenX >= 160 {
				continue
			}
			// horizontal flip via index mirroring
			idx := col
			if (s.Attr & (1 << 5)) != 0 {
				idx = 7 - col
			}
			ci := rowPix[idx]
			if ci == 0 {
				continue
			}
			// OBJ-to-BG priority: if bit7 set and BG pixel is non-zero, skip
			if (s.Attr & (1 << 7)) != 0 {
				if bgci[screenX] != 0 {
					continue
				}
			}
			// leftmost sprite X wins; if equal X, lower OAM index wins
			if s.X < bestSX[screenX] || (s.X == bestSX[screenX] && s.OAMIndex < bestIdx[screenX]) {
				ciOut[screenX] = ci
				palSel[screenX] = (s.Attr >> 4) & 1
				bestSX[screenX] = s.X
				bestIdx[screenX] = s.OAMIndex
			}
		}
	}
	return ciOut, palSel
}

// Back-compat wrapper that returns only color indices.
func ComposeSpriteLine(mem VRAMReader, sprites []Sprite, lineY int, bgci [160]byte, sprite16 bool) [160]byte {
	ci, _ := ComposeSpriteLineExt(mem, sprites, lineY, bgci, sprite16)
	return ci
}
