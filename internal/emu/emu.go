package emu

import (
	"bytes"
	"encoding/gob"
	"os"

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
	bus     *bus.Bus
	cpu     *cpu.CPU
	romPath string
	bootROM []byte
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
	// If a boot ROM is provided, install it and start from 0x0000 with boot enabled.
	if len(boot) >= 0x100 {
		b.SetBootROM(boot)
	}
	c := cpu.New(b)
	if len(boot) >= 0x100 {
		// Boot ROM path: start at 0x0000; do not force post-boot IO
		c.SP = 0xFFFE
		c.PC = 0x0000
		c.IME = false
	} else {
		// No boot ROM: initialize to DMG post-boot state
		c.ResetNoBoot()
		c.SetPC(0x0100)
	}
	m.bus = b
	m.cpu = c
	m.bootROM = nil
	if len(boot) >= 0x100 {
		m.bootROM = make([]byte, 0x100)
		copy(m.bootROM, boot[:0x100])
	}
	// Apply DMG post-boot IO defaults only when no boot ROM is used
	if len(boot) < 0x100 {
		m.applyDMGPostBootIO()
	}
	return nil
}

// LoadROMFromFile replaces the current cartridge with a ROM from disk, preserving boot ROM setting.
func (m *Machine) LoadROMFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var boot []byte
	if len(m.bootROM) >= 0x100 {
		boot = m.bootROM
	}
	if err := m.LoadCartridge(data, boot); err != nil {
		return err
	}
	m.romPath = path
	return nil
}

// ROMPath returns the currently loaded ROM file path, if any.
func (m *Machine) ROMPath() string {
	return m.romPath
}

// SetROMPath sets the current ROM path (used by UI for state/save association).
// This does not reload the ROM and should be called only after a successful cartridge load.
func (m *Machine) SetROMPath(path string) { m.romPath = path }

// SetBootROM sets the DMG boot ROM to be used when loading ROMs or executing with boot.
func (m *Machine) SetBootROM(data []byte) {
	if len(data) >= 0x100 {
		m.bootROM = make([]byte, 0x100)
		copy(m.bootROM, data[:0x100])
	} else {
		m.bootROM = nil
	}
	if m.bus != nil {
		m.bus.SetBootROM(m.bootROM)
	}
}

// HasBootROM reports whether a DMG boot ROM is configured on this machine.
func (m *Machine) HasBootROM() bool { return len(m.bootROM) >= 0x100 }

// ResetPostBoot resets CPU and IO to DMG post-boot state (no boot ROM), keeping the loaded cartridge.
func (m *Machine) ResetPostBoot() {
	if m.cpu == nil || m.bus == nil {
		return
	}
	m.cpu.ResetNoBoot()
	m.cpu.SetPC(0x0100)
	m.applyDMGPostBootIO()
}

// ResetWithBoot re-enables the boot ROM (if present) and restarts execution from 0x0000.
func (m *Machine) ResetWithBoot() {
	if m.cpu == nil || m.bus == nil || len(m.bootROM) < 0x100 {
		// Fallback to post-boot reset if no boot ROM
		m.ResetPostBoot()
		return
	}
	m.bus.SetBootROM(m.bootROM)
	m.cpu.SP = 0xFFFE
	m.cpu.PC = 0x0000
	m.cpu.IME = false
}

// applyDMGPostBootIO sets a minimal set of IO registers to DMG post-boot defaults,
// so ROMs can start from PC=0x0100 without a boot ROM and still have LCD enabled.
func (m *Machine) applyDMGPostBootIO() {
	if m == nil || m.bus == nil {
		return
	}
	b := m.bus
	// Joypad: no group selected, high bits set
	b.Write(0xFF00, 0xCF)
	// Timers
	b.Write(0xFF05, 0x00) // TIMA
	b.Write(0xFF06, 0x00) // TMA
	b.Write(0xFF07, 0x00) // TAC (disabled)
	// PPU regs (enable LCD, BG/window; default palettes)
	b.Write(0xFF40, 0x91) // LCDC: LCD on, BG on, tile data 8000, BG map 9800, sprites on 8x8
	b.Write(0xFF42, 0x00) // SCY
	b.Write(0xFF43, 0x00) // SCX
	b.Write(0xFF45, 0x00) // LYC
	b.Write(0xFF47, 0xFC) // BGP
	b.Write(0xFF48, 0xFF) // OBP0
	b.Write(0xFF49, 0xFF) // OBP1
	b.Write(0xFF4A, 0x00) // WY
	b.Write(0xFF4B, 0x00) // WX
	// IE: none enabled by default
	b.Write(0xFFFF, 0x00)
	// APU defaults (power on + route all to both, medium volume)
	b.Write(0xFF26, 0x80) // NR52 power
	b.Write(0xFF24, 0x77) // NR50: Vin off, L=7, R=7
	b.Write(0xFF25, 0xFF) // NR51: route all ch to both
	// Leave channels off until games configure them
}

// SaveBattery tries to persist external cartridge RAM to a provided sink via the BatteryBacked interface.
// The actual file IO is managed by the caller (e.g., cmd/gbemu).
func (m *Machine) SaveBattery() ([]byte, bool) {
	if m == nil || m.bus == nil {
		return nil, false
	}
	if bb, ok := m.bus.Cart().(interface{ SaveRAM() []byte }); ok {
		data := bb.SaveRAM()
		if len(data) == 0 {
			return nil, false
		}
		return data, true
	}
	return nil, false
}

// LoadBattery loads external RAM bytes into the cartridge if supported.
func (m *Machine) LoadBattery(data []byte) bool {
	if m == nil || m.bus == nil {
		return false
	}
	if bb, ok := m.bus.Cart().(interface{ LoadRAM([]byte) }); ok {
		bb.LoadRAM(data)
		return true
	}
	return false
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

// SetSerialWriter connects an io.Writer to receive bytes written to the serial port (FF01/FF02).
// Useful for running test ROMs that report via serial.
func (m *Machine) SetSerialWriter(w interface{ Write([]byte) (int, error) }) {
	if m != nil && m.bus != nil {
		m.bus.SetSerialWriter(w)
	}
}

// APUPullSamples returns up to max mono int16 samples from the APU ring buffer.
func (m *Machine) APUPullSamples(max int) []int16 {
	if m == nil || m.bus == nil || m.bus.APU() == nil {
		return nil
	}
	return m.bus.APU().PullSamples(max)
}

// APUPullStereo returns up to max stereo frames as interleaved int16 L,R pairs.
func (m *Machine) APUPullStereo(max int) []int16 {
	if m == nil || m.bus == nil || m.bus.APU() == nil {
		return nil
	}
	return m.bus.APU().PullStereo(max)
}

// APUBufferedStereo returns the number of stereo frames ready in the APU buffer.
func (m *Machine) APUBufferedStereo() int {
	if m == nil || m.bus == nil || m.bus.APU() == nil {
		return 0
	}
	return m.bus.APU().StereoAvailable()
}

// --- Save/Load state ---
type machineState struct {
	Bus []byte
	CPU []byte
}

func (m *Machine) SaveState() []byte {
	if m == nil || m.bus == nil || m.cpu == nil {
		return nil
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	_ = enc.Encode(machineState{Bus: m.bus.SaveState(), CPU: m.cpu.SaveState()})
	return buf.Bytes()
}

func (m *Machine) LoadState(data []byte) error {
	if m == nil || m.bus == nil || m.cpu == nil {
		return nil
	}
	var s machineState
	dec := gob.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&s); err != nil {
		return err
	}
	m.bus.LoadState(s.Bus)
	m.cpu.LoadState(s.CPU)
	return nil
}

func (m *Machine) SaveStateToFile(path string) error {
	data := m.SaveState()
	if len(data) == 0 {
		return nil
	}
	return os.WriteFile(path, data, 0644)
}

func (m *Machine) LoadStateFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return m.LoadState(data)
}
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
	// We render using per-scanline snapshots captured by the PPU at mode-2 start.
	// Fallback to live regs will be applied if snapshot is zero.
	for y := 0; y < 144; y++ {
		// Snapshot this line's regs
		lr := m.bus.PPU().LineRegs(y)
		// If snapshot is zero (e.g., before first capture), use current live regs as a fallback
		if lr.LCDC == 0 {
			lr.LCDC = m.bus.Read(0xFF40)
			lr.SCY = m.bus.Read(0xFF42)
			lr.SCX = m.bus.Read(0xFF43)
			lr.BGP = m.bus.Read(0xFF47)
		}
		// If LCD off or BG disabled for this line per snapshot, paint the line white and clear bgci
		if (lr.LCDC&0x80) == 0 || (lr.LCDC&0x01) == 0 {
			for x := 0; x < 160; x++ {
				i := (y*m.w + x) * 4
				m.fb[i+0] = 0xFF
				m.fb[i+1] = 0xFF
				m.fb[i+2] = 0xFF
				m.fb[i+3] = 0xFF
				m.bgci[y*m.w+x] = 0
			}
			continue
		}
		// Compute BG source using snapshot regs for tilemap/tiledata to keep stable per-line behavior
		bgMapBase := uint16(0x9800)
		if (lr.LCDC & 0x08) != 0 {
			bgMapBase = 0x9C00
		}
		tileData8000 := (lr.LCDC & 0x10) != 0
		bgy := byte((uint16(lr.SCY) + uint16(y)) & 0xFF)
		tileRow := uint16(bgy/8) * 32
		fineY := bgy % 8
		for x := 0; x < 160; x++ {
			bgx := byte((uint16(lr.SCX) + uint16(x)) & 0xFF)
			tileCol := uint16(bgx / 8)
			tileIndexAddr := bgMapBase + tileRow + tileCol
			tileNum := m.bus.PPU().RawVRAM(tileIndexAddr)
			var tileAddr uint16
			if tileData8000 {
				tileAddr = 0x8000 + uint16(tileNum)*16 + uint16(fineY)*2
			} else {
				tileAddr = 0x9000 + uint16(int8(tileNum))*16 + uint16(fineY)*2
			}
			lo := m.bus.PPU().RawVRAM(tileAddr)
			hi := m.bus.PPU().RawVRAM(tileAddr + 1)
			bit := 7 - (bgx % 8)
			ci := ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
			// map BG color using the snapshot palette
			bgp := lr.BGP
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
			s := shade(ci)
			i := (y*m.w + x) * 4
			m.fb[i+0] = s
			m.fb[i+1] = s
			m.fb[i+2] = s
			m.fb[i+3] = 0xFF
			m.bgci[y*m.w+x] = ci
		}
	}
}

func (m *Machine) renderWindow() {
	if m.bus == nil {
		return
	}
	// Render per-line using snapshots; do not early-return based on live regs to preserve mid-frame changes
	for y := 0; y < 144; y++ {
		// Snapshot this line
		lr := m.bus.PPU().LineRegs(y)
		if lr.LCDC == 0 {
			// Fallback to live regs if snapshot missing
			lr.LCDC = m.bus.Read(0xFF40)
			lr.WY = m.bus.Read(0xFF4A)
			lr.WX = m.bus.Read(0xFF4B)
			lr.BGP = m.bus.Read(0xFF47)
		}
		// Window is only considered if LCD on, BG enabled (DMG), and Window enabled for this line
		if (lr.LCDC&0x80) == 0 || (lr.LCDC&0x01) == 0 || (lr.LCDC&0x20) == 0 {
			continue
		}
		// Enforce window appears only when current line has reached WY
		if y < int(lr.WY) || int(lr.WY) >= 144 {
			continue
		}
		// Compute per-line window X start from snapshot (WX-7)
		winXStart := int(lr.WX) - 7
		if winXStart >= 160 {
			continue
		}
		// Tile selection uses per-line snapshot LCDC for stable behavior
		winMapBase := uint16(0x9800)
		if (lr.LCDC & 0x40) != 0 {
			winMapBase = 0x9C00
		}
		tileData8000 := (lr.LCDC & 0x10) != 0
		// Use the PPU's internal window line counter for Y within the window
		winY := lr.WinLine
		tileRow := uint16(winY/8) * 32
		fineY := winY % 8
		// Palette snapshot
		bgp := lr.BGP
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
		for x := max(0, winXStart); x < 160; x++ {
			winX := byte(x - winXStart)
			tileCol := uint16(winX / 8)
			tileIndexAddr := winMapBase + tileRow + tileCol
			tileNum := m.bus.PPU().RawVRAM(tileIndexAddr)
			var tileAddr uint16
			if tileData8000 {
				tileAddr = 0x8000 + uint16(tileNum)*16 + uint16(fineY)*2
			} else {
				tileAddr = 0x9000 + uint16(int8(tileNum))*16 + uint16(fineY)*2
			}
			lo := m.bus.PPU().RawVRAM(tileAddr)
			hi := m.bus.PPU().RawVRAM(tileAddr + 1)
			bit := 7 - (winX % 8)
			ci := ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
			s := shade(ci)
			i := (y*m.w + x) * 4
			m.fb[i+0] = s
			m.fb[i+1] = s
			m.fb[i+2] = s
			m.fb[i+3] = 0xFF
			m.bgci[y*m.w+x] = ci
		}
	}
}

// Sprite rendering with 8x8 and 8x16 support. Honors OBP0/OBP1 and BG priority via bgci.
func (m *Machine) renderSprites() {
	if m.bus == nil {
		return
	}
	// We'll use per-line snapshots for LCDC and palettes
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

	type oamEntry struct {
		sy, sx     int
		tile, attr byte
		index      int
	}
	for y := 0; y < 144; y++ {
		lr := m.bus.PPU().LineRegs(y)
		lcdc := lr.LCDC
		if lcdc == 0 {
			lcdc = m.bus.Read(0xFF40)
		}
		if (lcdc & 0x80) == 0 {
			continue
		} // LCD off
		if (lcdc & 0x02) == 0 {
			continue
		} // OBJ disabled
		// sprite size for this line
		sprite16 := (lcdc & 0x04) != 0
		obp0 := lr.OBP0
		obp1 := lr.OBP1
		if lr.LCDC == 0 { // fallback to live palettes if snapshot missing
			obp0 = m.bus.Read(0xFF48)
			obp1 = m.bus.Read(0xFF49)
		}
		// Gather up to 10 candidate sprites for this scanline in OAM order
		candidates := make([]oamEntry, 0, 10)
		for i := 0; i < 40 && len(candidates) < 10; i++ {
			base := uint16(0xFE00 + i*4)
			sy := int(m.bus.PPU().RawOAM(base)) - 16
			sx := int(m.bus.PPU().RawOAM(base+1)) - 8
			tile := m.bus.PPU().RawOAM(base + 2)
			attr := m.bus.PPU().RawOAM(base + 3)
			height := 8
			if sprite16 {
				height = 16
			}
			if sy <= y && y < sy+height {
				candidates = append(candidates, oamEntry{sy: sy, sx: sx, tile: tile, attr: attr, index: i})
			}
		}
		if len(candidates) == 0 {
			continue
		}
		// Stable sort by X ascending, then by OAM index to implement leftmost-X tie-breaker
		// (11 objects are never considered; we already capped to 10)
		for i := 0; i < len(candidates); i++ {
			for j := i + 1; j < len(candidates); j++ {
				if candidates[j].sx < candidates[i].sx || (candidates[j].sx == candidates[i].sx && candidates[j].index < candidates[i].index) {
					candidates[i], candidates[j] = candidates[j], candidates[i]
				}
			}
		}
		for x := 0; x < 160; x++ {
			bestFound := false
			bestX := 0
			bestIdx := 0
			bestGray := byte(0)
			for _, s := range candidates {
				sy := s.sy
				sx := s.sx
				tile := s.tile
				attr := s.attr
				if x < sx || x >= sx+8 {
					continue
				}
				// OBJ-to-BG priority: if bit7 set and BG/window pixel is non-zero, skip drawing
				if (attr & (1 << 7)) != 0 {
					if m.bgci[y*m.w+x] != 0 {
						continue
					}
				}
				row := int(y - sy)
				col := int(x - sx)
				if (attr & (1 << 6)) != 0 {
					if sprite16 {
						row = 15 - row
					} else {
						row = 7 - row
					}
				}
				if (attr & (1 << 5)) != 0 {
					col = 7 - col
				}
				tIndex := tile
				if sprite16 {
					tIndex &= 0xFE
					if row >= 8 {
						tIndex++
					}
				}
				rowWithin := row & 7
				tileAddr := uint16(0x8000) + uint16(tIndex)*16 + uint16(rowWithin)*2
				lo := m.bus.PPU().RawVRAM(tileAddr)
				hi := m.bus.PPU().RawVRAM(tileAddr + 1)
				bit := 7 - byte(col%8)
				ci := ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
				if ci == 0 {
					continue
				}
				if !bestFound || sx < bestX || (sx == bestX && s.index < bestIdx) {
					pal := obp0
					if (attr & (1 << 4)) != 0 {
						pal = obp1
					}
					bestGray = shadeP(pal, ci)
					bestX = sx
					bestIdx = s.index
					bestFound = true
				}
			}
			if bestFound {
				i := (y*m.w + x) * 4
				m.fb[i+0] = bestGray
				m.fb[i+1] = bestGray
				m.fb[i+2] = bestGray
				m.fb[i+3] = 0xFF
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
