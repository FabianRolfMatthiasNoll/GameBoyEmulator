package emu

import (
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/bus"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cart"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cpu"
)

type Buttons struct {
	A, B, Start, Select   bool
	Up, Down, Left, Right bool
}

type Machine struct {
	cfg  Config
	w, h int
	fb   []byte // RGBA 160x144*4
	bgci []byte // background/window color index buffer (0..3) per pixel for sprite priority
	// core components
	bus *bus.Bus
	cpu *cpu.CPU
}

func New(cfg Config) *Machine {
	return &Machine{
		cfg: cfg, w: 160, h: 144,
		fb:   make([]byte, 160*144*4),
		bgci: make([]byte, 160*144),
	}
}

func (m *Machine) LoadCartridge(rom []byte, boot []byte) error {
	// Parse header (just for logging/validation for now)
	romHeader, err := cart.ParseHeader(rom)
	if err != nil {
		return err
	}
	_ = romHeader
	// Wire bus+cpu. For now, ROM-only cartridge via bus.New.
	b := bus.New(rom)
	c := cpu.New(b)
	// Run without boot ROM for now (typical DMG post-boot state)
	c.ResetNoBoot()
	c.SetPC(0x0100)
	m.bus = b
	m.cpu = c
	return nil
}

func (m *Machine) StepFrame() {
	// Advance CPU for approximately one frame worth of cycles (~70224)
	if m.cpu != nil {
		target := 70224
		acc := 0
		for acc < target {
			acc += m.cpu.Step()
		}
	}
	// Render background, window, then sprites
	m.renderBG()
	m.renderWindow()
	m.renderSprites()
}

func (m *Machine) Framebuffer() []byte { return m.fb }
func (m *Machine) SetButtons(b Buttons) {
	if m.bus == nil {
		return
	}
	// Map buttons to joypad mask
	var mask byte
	if b.Right {
		mask |= bus.JoypRight
	}
	if b.Left {
		mask |= bus.JoypLeft
	}
	if b.Up {
		mask |= bus.JoypUp
	}
	if b.Down {
		mask |= bus.JoypDown
	}
	if b.A {
		mask |= bus.JoypA
	}
	if b.B {
		mask |= bus.JoypB
	}
	if b.Select {
		mask |= bus.JoypSelectBtn
	}
	if b.Start {
		mask |= bus.JoypStart
	}
	m.bus.SetJoypadState(mask)
}

// --- simple DMG background renderer ---
func (m *Machine) renderBG() {
	if m.bus == nil {
		return
	}
	lcdc := m.bus.Read(0xFF40)
	// If LCD off or BG disabled, screen is white and BG CI=0 everywhere (on DMG window is also disabled)
	if (lcdc&0x80) == 0 || (lcdc&0x01) == 0 { // LCD off or BG disabled
		for i := 0; i < len(m.fb); i += 4 {
			m.fb[i+0] = 0xFF
			m.fb[i+1] = 0xFF
			m.fb[i+2] = 0xFF
			m.fb[i+3] = 0xFF
		}
		// bg color index = 0 for all pixels
		for i := range m.bgci {
			m.bgci[i] = 0
		}
		return
	}
	scy := m.bus.Read(0xFF42)
	scx := m.bus.Read(0xFF43)
	bgp := m.bus.Read(0xFF47)
	// palette mapping for color index 0..3 to DMG shade (0=white,3=black)
	shade := func(ci byte) byte {
		// extract 2-bit entry from BGP
		shift := ci * 2
		pal := (bgp >> shift) & 0x03
		// map 00->white, 01->light gray, 10->dark gray, 11->black
		switch pal {
		case 0:
			return 0xFF
		case 1:
			return 0xC0
		case 2:
			return 0x60
		default:
			return 0x00
		}
	}
	// bg tile map base
	bgMapBase := uint16(0x9800)
	if (lcdc & 0x08) != 0 { // LCDC bit3
		bgMapBase = 0x9C00
	}
	// tile data base mode
	tileData8000 := (lcdc & 0x10) != 0 // true: 0x8000 indexing, false: 0x8800 signed

	for y := 0; y < 144; y++ {
		bgy := byte((uint16(scy) + uint16(y)) & 0xFF)
		tileRow := uint16(bgy/8) * 32
		fineY := bgy % 8
		for x := 0; x < 160; x++ {
			bgx := byte((uint16(scx) + uint16(x)) & 0xFF)
			tileCol := uint16(bgx / 8)
			tileIndexAddr := bgMapBase + tileRow + tileCol
			tileNum := m.bus.Read(tileIndexAddr)
			var tileAddr uint16
			if tileData8000 {
				tileAddr = 0x8000 + uint16(tileNum)*16 + uint16(fineY)*2
			} else {
				// signed index relative to 0x9000
				tileAddr = 0x9000 + uint16(int8(tileNum))*16 + uint16(fineY)*2
			}
			lo := m.bus.Read(tileAddr)
			hi := m.bus.Read(tileAddr + 1)
			bit := 7 - (bgx % 8)
			ci := ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
			s := shade(ci)
			i := (y*m.w + x) * 4
			m.fb[i+0] = s
			m.fb[i+1] = s
			m.fb[i+2] = s
			m.fb[i+3] = 0xFF
			// record BG color index for priority with sprites
			m.bgci[y*m.w+x] = ci
		}
	}
}

// Window rendering (BG-like but with separate tilemap and WX/WY offsets)
func (m *Machine) renderWindow() {
	if m.bus == nil {
		return
	}
	lcdc := m.bus.Read(0xFF40)
	if (lcdc & 0x80) == 0 {
		return
	} // LCD off
	if (lcdc & 0x01) == 0 {
		return
	} // BG disabled disables window on DMG
	if (lcdc & 0x20) == 0 {
		return
	} // window disabled
	wy := int(m.bus.Read(0xFF4A))
	wx := int(m.bus.Read(0xFF4B)) - 7
	if wy >= 144 || wx >= 160 {
		return
	}
	bgp := m.bus.Read(0xFF47)
	shade := func(ci byte) byte {
		shift := ci * 2
		pal := (bgp >> shift) & 0x03
		switch pal {
		case 0:
			return 0xFF
		case 1:
			return 0xC0
		case 2:
			return 0x60
		default:
			return 0x00
		}
	}
	winMapBase := uint16(0x9800)
	if (lcdc & 0x40) != 0 { // bit6
		winMapBase = 0x9C00
	}
	tileData8000 := (lcdc & 0x10) != 0

	for y := wy; y < 144; y++ {
		winY := byte(y - wy)
		tileRow := uint16(winY/8) * 32
		fineY := winY % 8
		for x := max(0, wx); x < 160; x++ {
			winX := byte(x - wx)
			tileCol := uint16(winX / 8)
			tileIndexAddr := winMapBase + tileRow + tileCol
			tileNum := m.bus.Read(tileIndexAddr)
			var tileAddr uint16
			if tileData8000 {
				tileAddr = 0x8000 + uint16(tileNum)*16 + uint16(fineY)*2
			} else {
				tileAddr = 0x9000 + uint16(int8(tileNum))*16 + uint16(fineY)*2
			}
			lo := m.bus.Read(tileAddr)
			hi := m.bus.Read(tileAddr + 1)
			bit := 7 - (winX % 8)
			ci := ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
			s := shade(ci)
			i := (y*m.w + x) * 4
			m.fb[i+0] = s
			m.fb[i+1] = s
			m.fb[i+2] = s
			m.fb[i+3] = 0xFF
			// window is part of BG layer for priority decisions
			m.bgci[y*m.w+x] = ci
		}
	}
}

// Sprite rendering (8x8 only; ignores 8x16 for now). Honors OBP0/OBP1 and basic priority.
func (m *Machine) renderSprites() {
	if m.bus == nil {
		return
	}
	lcdc := m.bus.Read(0xFF40)
	if (lcdc & 0x80) == 0 {
		return
	} // LCD off
	if (lcdc & 0x02) == 0 {
		return
	} // OBJ disabled
	// sprite size (false:8x8, true:8x16)
	sprite16 := (lcdc & 0x04) != 0
	obp0 := m.bus.Read(0xFF48)
	obp1 := m.bus.Read(0xFF49)
	shadeP := func(pal byte, ci byte) byte {
		shift := ci * 2
		p := (pal >> shift) & 0x03
		switch p {
		case 0:
			return 0xFF
		case 1:
			return 0xC0
		case 2:
			return 0x60
		default:
			return 0x00
		}
	}

	// For each scanline, we could limit to 10 sprites; here, keep it simple and approximate
	for y := 0; y < 144; y++ {
		// Gather candidate sprites for this line in OAM order (priority).
		candidates := make([][4]byte, 0, 10)
		for i := 0; i < 40 && len(candidates) < 10; i++ {
			base := uint16(0xFE00 + i*4)
			sy := int(m.bus.Read(base)) - 16
			sx := int(m.bus.Read(base+1)) - 8
			tile := m.bus.Read(base + 2)
			attr := m.bus.Read(base + 3)
			height := 8
			if sprite16 {
				height = 16
			}
			if sy <= y && y < sy+height {
				candidates = append(candidates, [4]byte{byte(sy), byte(sx), tile, attr})
			}
		}
		if len(candidates) == 0 {
			continue
		}
		for x := 0; x < 160; x++ {
			// Draw first visible, non-zero pixel that respects priority
			for _, s := range candidates {
				sy := int(int8(s[0])) // stored as byte but already adjusted
				sx := int(int8(s[1]))
				tile := s[2]
				attr := s[3]
				if x < sx || x >= sx+8 {
					continue
				}
				// row/col within the sprite
				row := int(y - sy)
				col := int(x - sx)
				// flips
				if (attr & (1 << 6)) != 0 { // Y flip
					if sprite16 {
						row = 15 - row
					} else {
						row = 7 - row
					}
				}
				if (attr & (1 << 5)) != 0 { // X flip
					col = 7 - col
				}
				// fetch tile row, handling 8x16 sprites (tile index LSB ignored)
				tIndex := tile
				if sprite16 {
					tIndex &= 0xFE // ignore LSB
					if row >= 8 {
						tIndex++
					}
				}
				rowWithin := row & 7
				tileAddr := uint16(0x8000) + uint16(tIndex)*16 + uint16(rowWithin)*2
				lo := m.bus.Read(tileAddr)
				hi := m.bus.Read(tileAddr + 1)
				bit := 7 - byte(col%8)
				ci := ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
				if ci == 0 { // color 0 is transparent for OBJ
					continue
				}
				// priority: if attr bit7 set and BG pixel is not color 0, skip drawing
				if (attr & (1 << 7)) != 0 {
					// check bg/window color index buffer
					if m.bgci[y*m.w+x] != 0 {
						continue
					}
				}
				pal := obp0
				if (attr & (1 << 4)) != 0 {
					pal = obp1
				}
				sgray := shadeP(pal, ci)
				i := (y*m.w + x) * 4
				m.fb[i+0] = sgray
				m.fb[i+1] = sgray
				m.fb[i+2] = sgray
				m.fb[i+3] = 0xFF
				break // done for this pixel
			}
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// func (m *Machine) DrainAudio(max int) []float32 { ... later ... }
