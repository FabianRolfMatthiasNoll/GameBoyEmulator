package ppu

// Minimal, isolated BG fetcher + FIFO used for future PPU refactor.
// This is not wired into rendering yet; unit tests validate behavior.

// VRAMReader provides read-only access for the fetcher or scanline helpers.
// It abstracts how VRAM bytes are fetched (tests vs. live PPU).
type VRAMReader interface {
	Read(addr uint16) byte
}

// fifo is a simple ring buffer for 2-bit color indices (0..3).
type fifo struct {
	buf  [32]byte // room for several tiles
	head int
	tail int
	size int
}

func (q *fifo) Clear()   { q.head, q.tail, q.size = 0, 0, 0 }
func (q *fifo) Len() int { return q.size }
func (q *fifo) Push(ci byte) bool {
	if q.size == len(q.buf) {
		return false
	}
	q.buf[q.tail] = ci & 0x03
	q.tail = (q.tail + 1) % len(q.buf)
	q.size++
	return true
}
func (q *fifo) Pop() (byte, bool) {
	if q.size == 0 {
		return 0, false
	}
	v := q.buf[q.head]
	q.head = (q.head + 1) % len(q.buf)
	q.size--
	return v, true
}

// bgFetcher pulls one tile row (8 pixels) into the FIFO.
type bgFetcher struct {
	mem           VRAMReader
	fifo          *fifo
	mapBase       uint16 // 0x9800 or 0x9C00
	tileData8000  bool   // true: 0x8000 addressing; false: 0x8800 signed
	tileIndexAddr uint16 // tile index address within map
	fineY         byte   // 0..7 within tile
}

func newBGFetcher(mem VRAMReader, f *fifo) *bgFetcher { return &bgFetcher{mem: mem, fifo: f} }

// Configure sets tilemap and addressing mode for the next fetch.
func (fch *bgFetcher) Configure(mapBase uint16, tileData8000 bool, tileIndexAddr uint16, fineY byte) {
	fch.mapBase = mapBase
	fch.tileData8000 = tileData8000
	fch.tileIndexAddr = tileIndexAddr
	fch.fineY = fineY & 7
}

// Fetch pushes 8 pixels (color indices) for the current tile row to the FIFO.
func (fch *bgFetcher) Fetch() {
	tileNum := fch.mem.Read(fch.tileIndexAddr)
	var base uint16
	if fch.tileData8000 {
		base = 0x8000 + uint16(tileNum)*16 + uint16(fch.fineY)*2
	} else {
		base = 0x9000 + uint16(int8(tileNum))*16 + uint16(fch.fineY)*2
	}
	lo := fch.mem.Read(base)
	hi := fch.mem.Read(base + 1)
	for px := 0; px < 8; px++ {
		bit := 7 - byte(px)
		ci := ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
		_ = fch.fifo.Push(ci)
	}
}
