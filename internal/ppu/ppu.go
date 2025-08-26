package ppu

// InterruptRequester is a callback signature to request IF bits (0:VBlank, 1:STAT, etc.).
type InterruptRequester func(bit int)

// PPU models VRAM/OAM, LCDC/STAT regs, LY/LYC, and basic timing.
// It exposes CPU-facing Read/Write for VRAM/OAM and PPU IO regs.
type PPU struct {
	// memory
	vram [0x2000]byte // 0x8000–0x9FFF
	oam  [0xA0]byte   // 0xFE00–0xFE9F

	// regs
	lcdc byte // FF40
	stat byte // FF41 (mode bits 0-1, coincidence flag bit2, enables bits3-6)
	scy  byte // FF42
	scx  byte // FF43
	ly   byte // FF44
	lyc  byte // FF45
	bgp  byte // FF47
	obp0 byte // FF48
	obp1 byte // FF49
	wy   byte // FF4A
	wx   byte // FF4B

	dot int // dots within current line [0..455]

	req InterruptRequester
}

func New(req InterruptRequester) *PPU { return &PPU{req: req} }

// CPURead returns bytes for VRAM, OAM, and PPU IO registers. Returns 0xFF for others.
func (p *PPU) CPURead(addr uint16) byte {
	switch {
	case addr >= 0x8000 && addr <= 0x9FFF:
	// VRAM is inaccessible to CPU during mode 3 (return 0xFF)
	if (p.stat & 0x03) == 3 { return 0xFF }
	return p.vram[addr-0x8000]
	case addr >= 0xFE00 && addr <= 0xFE9F:
	// OAM is inaccessible during modes 2 and 3
	m := p.stat & 0x03
	if m == 2 || m == 3 { return 0xFF }
	return p.oam[addr-0xFE00]
	case addr == 0xFF40:
		return p.lcdc
	case addr == 0xFF41:
	// On DMG, bit7 reads as 1; bit6..3 are enables; bit2 coincidence; bit1..0 mode
	return 0x80 | (p.stat & 0x7F)
	case addr == 0xFF42:
		return p.scy
	case addr == 0xFF43:
		return p.scx
	case addr == 0xFF44:
		return p.ly
	case addr == 0xFF45:
		return p.lyc
	case addr == 0xFF47:
		return p.bgp
	case addr == 0xFF48:
		return p.obp0
	case addr == 0xFF49:
		return p.obp1
	case addr == 0xFF4A:
		return p.wy
	case addr == 0xFF4B:
		return p.wx
	default:
		return 0xFF
	}
}

// CPUWrite handles writes to VRAM, OAM, and PPU IO regs. Others are ignored here.
func (p *PPU) CPUWrite(addr uint16, value byte) {
	switch {
	case addr >= 0x8000 && addr <= 0x9FFF:
	if (p.stat & 0x03) == 3 { return }
	p.vram[addr-0x8000] = value
	case addr >= 0xFE00 && addr <= 0xFE9F:
	m := p.stat & 0x03
	if m == 2 || m == 3 { return }
	p.oam[addr-0xFE00] = value
	case addr == 0xFF40:
		prev := p.lcdc
		p.lcdc = value
		if (p.lcdc&0x80) == 0 && (prev&0x80) != 0 {
			// Turning LCD off resets LY/mode
			p.ly = 0
			p.dot = 0
			p.setMode(0)
			p.updateLYC()
		} else if (p.lcdc&0x80) != 0 && (prev&0x80) == 0 {
			// Turning LCD on: start at LY=0, mode 2 (OAM)
			p.ly = 0
			p.dot = 0
			p.setMode(2)
			p.updateLYC()
		}
	case addr == 0xFF41:
		p.stat = (p.stat & 0x07) | (value & 0x78)
	case addr == 0xFF42:
		p.scy = value
	case addr == 0xFF43:
		p.scx = value
	case addr == 0xFF44:
		p.ly = 0
		p.dot = 0
		p.updateLYC()
		if (p.lcdc & 0x80) != 0 {
			p.setMode(2)
		}
	case addr == 0xFF45:
		p.lyc = value
		p.updateLYC()
	case addr == 0xFF47:
		p.bgp = value
	case addr == 0xFF48:
		p.obp0 = value
	case addr == 0xFF49:
		p.obp1 = value
	case addr == 0xFF4A:
		p.wy = value
	case addr == 0xFF4B:
		p.wx = value
	}
}

// Tick advances PPU state by the given number of dots (CPU cycles).
func (p *PPU) Tick(cycles int) {
	if cycles <= 0 {
		return
	}
	for i := 0; i < cycles; i++ {
		if (p.lcdc & 0x80) == 0 { // LCD off
			continue
		}
		p.dot++
		// Mode scheduling
		var mode byte
		if p.ly >= 144 {
			mode = 1
		} else {
			switch {
			case p.dot < 80:
				mode = 2
			case p.dot < 80+172:
				mode = 3
			default:
				mode = 0
			}
		}
		p.setMode(mode)

		if p.dot >= 456 {
			p.dot = 0
			p.ly++
			if p.ly == 144 {
				// Enter VBlank
				if p.req != nil {
					p.req(0)
				} // VBlank IF
				if (p.stat & (1 << 4)) != 0 {
					if p.req != nil {
						p.req(1)
					}
				} // STAT VBlank
			} else if p.ly > 153 {
				p.ly = 0
			}
			p.updateLYC()
			// Set mode for new line start (dot=0)
			if p.ly >= 144 {
				p.setMode(1)
			} else {
				p.setMode(2)
			}
		}
	}
}

func (p *PPU) setMode(mode byte) {
	prev := p.stat & 0x03
	if prev == mode {
		return
	}
	p.stat = (p.stat &^ 0x03) | (mode & 0x03)
	switch mode {
	case 0: // HBlank
		if (p.stat & (1 << 3)) != 0 {
			if p.req != nil {
				p.req(1)
			}
		}
	case 2: // OAM
		if (p.stat & (1 << 5)) != 0 {
			if p.req != nil {
				p.req(1)
			}
		}
	}
}

func (p *PPU) updateLYC() {
	if p.ly == p.lyc {
		p.stat |= 1 << 2
		if (p.stat & (1 << 6)) != 0 {
			if p.req != nil {
				p.req(1)
			}
		}
	} else {
		p.stat &^= 1 << 2
	}
}

// Expose palettes and scroll for renderer convenience (optional helpers)
func (p *PPU) BGP() byte  { return p.bgp }
func (p *PPU) OBP0() byte { return p.obp0 }
func (p *PPU) OBP1() byte { return p.obp1 }
func (p *PPU) LCDC() byte { return p.lcdc }
func (p *PPU) SCY() byte  { return p.scy }
func (p *PPU) SCX() byte  { return p.scx }
func (p *PPU) WY() byte   { return p.wy }
func (p *PPU) WX() byte   { return p.wx }
