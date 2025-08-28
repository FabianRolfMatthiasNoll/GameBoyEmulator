package ppu

import (
	"bytes"
	"encoding/gob"
)

// InterruptRequester is a callback signature to request IF bits (0:VBlank, 1:STAT, etc.).
type InterruptRequester func(bit int)

// PPU models VRAM/OAM, LCDC/STAT regs, LY/LYC, and basic timing.
// It exposes CPU-facing Read/Write for VRAM/OAM and PPU IO regs.
type PPU struct {
	// memory
	vram  [0x2000]byte // 0x8000–0x9FFF
	vram1 [0x2000]byte // CGB: VRAM bank 1 (0x8000–0x9FFF)
	oam   [0xA0]byte   // 0xFE00–0xFE9F

	// CGB color palettes (CRAM)
	bgPal  [64]byte // 8 palettes * 4 colors * 2 bytes
	objPal [64]byte // same for OBJ
	bcps   byte     // FF68: BG palette index (bits0-5 addr, bit7 auto-inc)
	ocps   byte     // FF6A: OBJ palette index

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

	// Per-scanline register snapshot captured at start of each visible line (mode 2)
	lineRegs [154]LineRegs

	// Internal window line counter (increments each line when window is active)
	winLineCounter byte
}

func New(req InterruptRequester) *PPU {
	p := &PPU{req: req}
	// Initialize CGB palettes to white so colors are visible before games set them.
	// Each palette has 4 colors; color stored as RGB555 little-endian. White = 0x7FFF => lo=FF hi=7F.
	for i := 0; i < 64; i += 2 {
		p.bgPal[i] = 0xFF
		p.bgPal[i+1] = 0x7F
		p.objPal[i] = 0xFF
		p.objPal[i+1] = 0x7F
	}
	return p
}

// LineRegs represents the PPU-visible registers relevant for rendering a scanline.
type LineRegs struct {
	LCDC    byte
	SCY     byte
	SCX     byte
	BGP     byte
	OBP0    byte
	OBP1    byte
	WY      byte
	WX      byte
	WinLine byte
}

// CPURead returns bytes for VRAM, OAM, and PPU IO registers. Returns 0xFF for others.
func (p *PPU) CPURead(addr uint16) byte {
	switch {
	case addr >= 0x8000 && addr <= 0x9FFF:
		// VRAM is inaccessible to CPU during mode 3 (return 0xFF)
		if (p.stat & 0x03) == 3 {
			return 0xFF
		}
		return p.vram[addr-0x8000]
	case addr >= 0xFE00 && addr <= 0xFE9F:
		// OAM is inaccessible during modes 2 and 3
		m := p.stat & 0x03
		if m == 2 || m == 3 {
			return 0xFF
		}
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
	case addr == 0xFF68: // BCPS/BGPI
		return 0x40 | (p.bcps & 0xBF)
	case addr == 0xFF69: // BCPD/BGPD
		idx := int(p.bcps & 0x3F)
		return p.bgPal[idx]
	case addr == 0xFF6A: // OCPS/OBPI
		return 0x40 | (p.ocps & 0xBF)
	case addr == 0xFF6B: // OCPD/OBPD
		idx := int(p.ocps & 0x3F)
		return p.objPal[idx]
	default:
		return 0xFF
	}
}

// CPUWrite handles writes to VRAM, OAM, and PPU IO regs. Others are ignored here.
func (p *PPU) CPUWrite(addr uint16, value byte) {
	switch {
	case addr >= 0x8000 && addr <= 0x9FFF:
		if (p.stat & 0x03) == 3 {
			return
		}
		p.vram[addr-0x8000] = value
	case addr >= 0xFE00 && addr <= 0xFE9F:
		m := p.stat & 0x03
		if m == 2 || m == 3 {
			return
		}
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
			p.winLineCounter = 0
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
		p.winLineCounter = 0
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
	case addr == 0xFF68: // BCPS/BGPI
		p.bcps = value & 0xBF // bit6 reads/writes as 0; keep bit7 as auto-inc flag
	case addr == 0xFF69: // BCPD/BGPD
		// Ignore writes during Mode 3 (approximation)
		if (p.stat & 0x03) == 3 {
			return
		}
		idx := int(p.bcps & 0x3F)
		p.bgPal[idx] = value
		if (p.bcps & 0x80) != 0 { // auto-increment
			p.bcps = (p.bcps & 0xC0) | byte((idx+1)&0x3F)
		}
	case addr == 0xFF6A: // OCPS/OBPI
		p.ocps = value & 0xBF
	case addr == 0xFF6B: // OCPD/OBPD
		if (p.stat & 0x03) == 3 {
			return
		}
		idx := int(p.ocps & 0x3F)
		p.objPal[idx] = value
		if (p.ocps & 0x80) != 0 {
			p.ocps = (p.ocps & 0xC0) | byte((idx+1)&0x3F)
		}
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
				p.winLineCounter = 0
			}
			p.updateLYC()
			// Set mode for new line start (dot=0)
			if p.ly >= 144 {
				p.setMode(1)
			} else {
				p.setMode(2)
				// Update window line counter for THIS line based on visibility
				// On DMG, window display requires both BG (bit0) and window (bit5) enabled.
				windowVisible := (p.lcdc&0x20) != 0 && (p.lcdc&0x01) != 0 && p.ly >= p.wy && p.wx <= 166
				if windowVisible {
					if p.ly == p.wy {
						p.winLineCounter = 0
					} else if p.ly > p.wy {
						p.winLineCounter++
					}
				}
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
	case 3: // Entering mode 3: latch per-line regs for rendering
		p.captureLineRegs()
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

func (p *PPU) captureLineRegs() {
	if p.ly < 144 {
		p.lineRegs[p.ly] = LineRegs{
			LCDC:    p.lcdc,
			SCY:     p.scy,
			SCX:     p.scx,
			BGP:     p.bgp,
			OBP0:    p.obp0,
			OBP1:    p.obp1,
			WY:      p.wy,
			WX:      p.wx,
			WinLine: p.winLineCounter,
		}
	}
}

// LineRegs returns the captured register snapshot for a given scanline (0..153).
func (p *PPU) LineRegs(y int) LineRegs {
	if y < 0 || y >= len(p.lineRegs) {
		return LineRegs{}
	}
	return p.lineRegs[y]
}

// RawVRAM returns VRAM bytes without CPU access restrictions; for renderer use only.
func (p *PPU) RawVRAM(addr uint16) byte {
	if addr >= 0x8000 && addr <= 0x9FFF {
		return p.vram[addr-0x8000]
	}
	return 0xFF
}

// RawVRAMBank returns a byte from the specified VRAM bank (0 or 1) without access restrictions.
func (p *PPU) RawVRAMBank(bank int, addr uint16) byte {
	if addr < 0x8000 || addr > 0x9FFF {
		return 0xFF
	}
	off := addr - 0x8000
	if bank == 0 {
		return p.vram[off]
	}
	return p.vram1[off]
}

// RawOAM returns OAM bytes without CPU access restrictions; for renderer use only.
func (p *PPU) RawOAM(addr uint16) byte {
	if addr >= 0xFE00 && addr <= 0xFE9F {
		return p.oam[addr-0xFE00]
	}
	return 0xFF
}

// --- CGB palette helpers ---
// decodeRGB555 converts little-endian 15-bit color to 8-bit per channel (simple scale).
func decodeRGB555(lo, hi byte) (r, g, b byte) {
	v := uint16(lo) | (uint16(hi) << 8)
	r5 := byte(v & 0x1F)
	g5 := byte((v >> 5) & 0x1F)
	b5 := byte((v >> 10) & 0x1F)
	// scale 5-bit to 8-bit by left shift and OR with upper bits for a simple approximation
	r = (r5 << 3) | (r5 >> 2)
	g = (g5 << 3) | (g5 >> 2)
	b = (b5 << 3) | (b5 >> 2)
	return
}

// BGColorRGB returns the RGB color for given BG palette index (0..7) and color index (0..3).
func (p *PPU) BGColorRGB(palIdx, colorIdx byte) (r, g, b byte) {
	pi := int(palIdx&7)*8 + int(colorIdx&3)*2
	lo := p.bgPal[pi]
	hi := p.bgPal[pi+1]
	return decodeRGB555(lo, hi)
}

// OBJColorRGB returns the RGB color for given OBJ palette index (0..7) and color index (1..3; 0 transparent).
func (p *PPU) OBJColorRGB(palIdx, colorIdx byte) (r, g, b byte) {
	pi := int(palIdx&7)*8 + int(colorIdx&3)*2
	lo := p.objPal[pi]
	hi := p.objPal[pi+1]
	return decodeRGB555(lo, hi)
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

// --- Save/Load state ---
type ppuState struct {
	VRAM     [0x2000]byte
	VRAM1    [0x2000]byte
	OAM      [0xA0]byte
	BGPal    [64]byte
	OBJPal   [64]byte
	BCPS     byte
	OCPS     byte
	LCDC     byte
	STAT     byte
	SCY      byte
	SCX      byte
	LY       byte
	LYC      byte
	BGP      byte
	OBP0     byte
	OBP1     byte
	WY       byte
	WX       byte
	DOT      int
	LineRegs [154]LineRegs
	WinLine  byte
}

func (p *PPU) SaveState() []byte {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	s := ppuState{
		VRAM: p.vram, VRAM1: p.vram1, OAM: p.oam,
		BGPal: p.bgPal, OBJPal: p.objPal, BCPS: p.bcps, OCPS: p.ocps,
		LCDC: p.lcdc, STAT: p.stat, SCY: p.scy, SCX: p.scx, LY: p.ly, LYC: p.lyc,
		BGP: p.bgp, OBP0: p.obp0, OBP1: p.obp1, WY: p.wy, WX: p.wx,
		DOT: p.dot, LineRegs: p.lineRegs, WinLine: p.winLineCounter,
	}
	_ = enc.Encode(s)
	return buf.Bytes()
}

func (p *PPU) LoadState(data []byte) {
	var s ppuState
	dec := gob.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&s); err != nil {
		return
	}
	p.vram = s.VRAM
	p.vram1 = s.VRAM1
	p.oam = s.OAM
	p.bgPal = s.BGPal
	p.objPal = s.OBJPal
	p.bcps = s.BCPS
	p.ocps = s.OCPS
	p.lcdc, p.stat, p.scy, p.scx, p.ly, p.lyc = s.LCDC, s.STAT, s.SCY, s.SCX, s.LY, s.LYC
	p.bgp, p.obp0, p.obp1, p.wy, p.wx = s.BGP, s.OBP0, s.OBP1, s.WY, s.WX
	p.dot = s.DOT
	p.lineRegs = s.LineRegs
	p.winLineCounter = s.WinLine
}
