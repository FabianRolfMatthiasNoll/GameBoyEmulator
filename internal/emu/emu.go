package emu

import (
	"bytes"
	"encoding/gob"
	"os"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/bus"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cart"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cpu"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/ppu"
)

type Buttons struct {
	A, B, Start, Select   bool
	Up, Down, Left, Right bool
}

type Machine struct {
	cfg   Config
	w, h  int
	fb    []byte // RGBA 160x144*4
	bgci  []byte // BG/window color index (0..3) per pixel for priority
	bgpal []byte // BG palette index (0..7) per pixel when in CGB path
	bgpri []bool // BG priority flag per pixel when in CGB path
	// core components
	bus        *bus.Bus
	cpu        *cpu.CPU
	romPath    string
	bootROM    []byte
	cgbBootROM []byte
	// ROM capability (from header): if false, do not expose CGB hardware even if toggle is on
	cgbCapable bool
}

func New(cfg Config) *Machine {
	return &Machine{
		cfg: cfg, w: 160, h: 144,
		fb:    make([]byte, 160*144*4),
		bgci:  make([]byte, 160*144),
		bgpal: make([]byte, 160*144),
		bgpri: make([]bool, 160*144),
	}
}

func (m *Machine) LoadCartridge(rom []byte, boot []byte) error {
	// Parse header (just for logging/validation for now)
	romHeader, err := cart.ParseHeader(rom)
	if err != nil {
		return err
	}
	_ = romHeader
	// Record whether the ROM supports or requires CGB features
	m.cgbCapable = false
	if romHeader != nil {
		if (romHeader.CGBFlag & 0x80) != 0 { // supports CGB (0x80) or CGB-only (0xC0)
			m.cgbCapable = true
		}
	}
	// Decide whether to use the supplied boot ROM. If a DMG boot ROM is provided
	// (256 bytes) but the game is CGB-capable, ignore it; without a proper CGB boot ROM,
	// start directly at $0100 with CGB post-boot semantics so the game detects CGB.
	useBoot := len(boot) >= 0x100
	if romHeader != nil && (romHeader.CGBFlag&0x80) != 0 && len(boot) == 0x100 {
		useBoot = false
	}
	// Wire bus+cpu. For now, ROM-only cartridge via bus.New.
	b := bus.New(rom)
	if useBoot {
		b.SetBootROM(boot)
	}
	c := cpu.New(b)
	if useBoot {
		// Boot ROM path: start at 0x0000; do not force post-boot IO
		c.SP = 0xFFFE
		c.PC = 0x0000
		c.IME = false
	} else {
		// No boot ROM: initialize to DMG post-boot state
		c.ResetNoBoot()
		c.SetPC(0x0100)
		// If the game is CGB-capable, set A=$11 so it detects CGB hardware per Pan Docs
		if romHeader != nil && (romHeader.CGBFlag&0x80) != 0 {
			c.A = 0x11
		}
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
	// Auto-enable CGB color path if ROM indicates CGB support
	if romHeader != nil {
		// CGBFlag: 0x80 = supports CGB (works on DMG), 0xC0 = CGB only
		if romHeader.CGBFlag&0x80 != 0 {
			m.cfg.UseCGBBG = true
			if m.bus != nil {
				m.bus.SetCGBMode(true)
			}
		} else {
			// for pure DMG, default to classic
			m.cfg.UseCGBBG = false
			if m.bus != nil {
				m.bus.SetCGBMode(false)
			}
		}
	}
	return nil
}

// SetUseFetcherBG toggles the BG renderer between classic and fetcher-based path.
func (m *Machine) SetUseFetcherBG(on bool) { m.cfg.UseFetcherBG = on }

// SetUseCGBBG toggles the CGB BG/Window/Sprite rendering path using CGB attributes and palettes.
func (m *Machine) SetUseCGBBG(on bool) {
	m.cfg.UseCGBBG = on
	if m.bus != nil {
		// Only expose CGB hardware if the loaded ROM is CGB-capable
		m.bus.SetCGBMode(on && m.cgbCapable)
	}
}

// UseCGBBG reports whether the CGB rendering path is enabled.
func (m *Machine) UseCGBBG() bool { return m.cfg.UseCGBBG && m.cgbCapable }

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

// SetCGBBootROM sets the CGB boot ROM used when starting CGB-capable games.
func (m *Machine) SetCGBBootROM(data []byte) {
	if len(data) >= 0x800 {
		m.cgbBootROM = make([]byte, 0x800)
		copy(m.cgbBootROM, data[len(data)-0x800:])
	} else if len(data) >= 0x900 {
		m.cgbBootROM = make([]byte, 0x800)
		copy(m.cgbBootROM, data[len(data)-0x800:])
	} else {
		m.cgbBootROM = nil
	}
	if m.bus != nil {
		m.bus.SetCGBBootROM(m.cgbBootROM)
	}
}

// HasBootROM reports whether a DMG boot ROM is configured on this machine.
func (m *Machine) HasBootROM() bool { return len(m.bootROM) >= 0x100 }

// HasCGBBootROM reports whether a CGB boot ROM is configured.
func (m *Machine) HasCGBBootROM() bool { return len(m.cgbBootROM) >= 0x800 }

// ResetPostBoot resets CPU and IO to DMG post-boot state (no boot ROM), keeping the loaded cartridge.
func (m *Machine) ResetPostBoot() {
	if m.cpu == nil || m.bus == nil {
		return
	}
	m.cpu.ResetNoBoot()
	m.cpu.SetPC(0x0100)
	m.applyDMGPostBootIO()
	m.bus.EnableBoot(0)
}

// ResetWithBoot re-enables the boot ROM (if present) and restarts execution from 0x0000.
func (m *Machine) ResetWithBoot() {
	if m.cpu == nil || m.bus == nil || len(m.bootROM) < 0x100 {
		// Fallback to post-boot reset if no boot ROM
		m.ResetPostBoot()
		return
	}
	m.bus.SetBootROM(m.bootROM)
	m.bus.EnableBoot(1)
	m.cpu.SP = 0xFFFE
	m.cpu.PC = 0x0000
	m.cpu.IME = false
}

// ResetWithCGBBoot enables the CGB boot ROM and restarts from 0x0000.
func (m *Machine) ResetWithCGBBoot() {
	if m.cpu == nil || m.bus == nil || len(m.cgbBootROM) < 0x800 {
		m.ResetPostBoot()
		return
	}
	m.bus.SetCGBBootROM(m.cgbBootROM)
	m.bus.EnableBoot(2)
	m.cpu.SP = 0xFFFE
	m.cpu.PC = 0x0000
	m.cpu.IME = false
}

// ResetCGBPostBoot simulates the CGB boot hand-off: enables CGB hardware, sets A=0x11, and jumps to $0100.
// If compat is true (DMG ROM on CGB), this represents DMG compatibility mode; we still enable CGB hardware
// so palettes and VBK/SVBK exist, but DMG games will keep grayscale unless we implement compatibility palettes.
func (m *Machine) ResetCGBPostBoot(compat bool) {
	if m.cpu == nil || m.bus == nil {
		return
	}
	// Expose CGB hardware to the CPU
	m.bus.SetCGBMode(true)
	// Clear any boot mapping
	m.bus.EnableBoot(0)
	// CPU state like CGB after boot
	m.cpu.ResetNoBoot()
	m.cpu.SetPC(0x0100)
	m.cpu.A = 0x11 // indicate CGB hardware per Pan Docs
	// Set minimal IO similar to applyDMGPostBootIO
	m.applyDMGPostBootIO()
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

// APUClearAudioLatency drops all buffered stereo frames to re-sync audio with video.
func (m *Machine) APUClearAudioLatency() {
	if m == nil || m.bus == nil || m.bus.APU() == nil {
		return
	}
	m.bus.APU().ClearStereoBuffer()
}

// APUCapBufferedStereo trims the buffered frames to at most target frames.
func (m *Machine) APUCapBufferedStereo(target int) {
	if m == nil || m.bus == nil || m.bus.APU() == nil {
		return
	}
	m.bus.APU().TrimStereoTo(target)
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
	// CGB path with attributes: use banked VRAM and true color via CGB palettes.
	if m.cfg.UseCGBBG && m.cgbCapable {
		for y := 0; y < 144; y++ {
			lr := m.bus.PPU().LineRegs(y)
			if lr.LCDC == 0 {
				lr.LCDC = m.bus.Read(0xFF40)
				lr.SCY = m.bus.Read(0xFF42)
				lr.SCX = m.bus.Read(0xFF43)
				lr.BGP = m.bus.Read(0xFF47)
			}
			if (lr.LCDC&0x80) == 0 || (lr.LCDC&0x01) == 0 {
				for x := 0; x < 160; x++ {
					i := (y*m.w + x) * 4
					m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = 0xFF, 0xFF, 0xFF, 0xFF
					m.bgci[y*m.w+x] = 0
					m.bgpal[y*m.w+x] = 0
					m.bgpri[y*m.w+x] = false
				}
				continue
			}
			mapBase := uint16(0x9800)
			if (lr.LCDC & 0x08) != 0 {
				mapBase = 0x9C00
			}
			attrsBase := mapBase // attributes in bank 1 at same addresses
			tileData8000 := (lr.LCDC & 0x10) != 0
			vr := vramBankedAdapter{ppu: m.bus.PPU()}
			line, pals, pris := ppu.RenderBGScanlineCGB(vr, mapBase, attrsBase, tileData8000, lr.SCX, lr.SCY, byte(y))
			// If CGB BG palettes haven't been written yet (DMG ROM or early boot), fallback to DMG BGP grayscale.
			useDMGPal := !m.bus.PPU().BGPalReady()
			var bgp byte
			if useDMGPal {
				bgp = lr.BGP
				if lr.LCDC == 0 {
					bgp = m.bus.Read(0xFF47)
				}
			}
			shade := func(ci byte) (byte, byte, byte) {
				shift := ci * 2
				pal := (bgp >> shift) & 0x03
				switch pal {
				case 0:
					return 0xFF, 0xFF, 0xFF
				case 1:
					return 0xC0, 0xC0, 0xC0
				case 2:
					return 0x60, 0x60, 0x60
				default:
					return 0x00, 0x00, 0x00
				}
			}
			for x := 0; x < 160; x++ {
				pal := pals[x]
				ci := line[x]
				var r, g, b byte
				if useDMGPal {
					r, g, b = shade(ci)
				} else {
					r, g, b = m.bus.PPU().BGColorRGB(pal, ci)
				}
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = r, g, b, 0xFF
				m.bgci[y*m.w+x] = ci
				m.bgpal[y*m.w+x] = pal
				m.bgpri[y*m.w+x] = pris[x]
			}
		}
		return
	}
	// Optional fast path using fetcher/FIFO per-scanline renderer for BG layer
	if m.cfg.UseFetcherBG {
		for y := 0; y < 144; y++ {
			lr := m.bus.PPU().LineRegs(y)
			if lr.LCDC == 0 {
				lr.LCDC = m.bus.Read(0xFF40)
				lr.SCY = m.bus.Read(0xFF42)
				lr.SCX = m.bus.Read(0xFF43)
				lr.BGP = m.bus.Read(0xFF47)
			}
			if (lr.LCDC&0x80) == 0 || (lr.LCDC&0x01) == 0 {
				for x := 0; x < 160; x++ {
					i := (y*m.w + x) * 4
					m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = 0xFF, 0xFF, 0xFF, 0xFF
					m.bgci[y*m.w+x] = 0
				}
				continue
			}
			bgMapBase := uint16(0x9800)
			if (lr.LCDC & 0x08) != 0 {
				bgMapBase = 0x9C00
			}
			tileData8000 := (lr.LCDC & 0x10) != 0
			// Adapter to PPU RawVRAM for VRAMReader interface
			vr := vramReaderAdapter{ppu: m.bus.PPU()}
			line := ppu.RenderBGScanlineUsingFetcher(vr, bgMapBase, tileData8000, lr.SCX, lr.SCY, byte(y))
			// Shade and write out
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
			for x := 0; x < 160; x++ {
				ci := line[x]
				s := shade(ci)
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = s, s, s, 0xFF
				m.bgci[y*m.w+x] = ci
			}
		}
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

// vramReaderAdapter adapts the live PPU RawVRAM to the ppu.VRAMReader interface used by the fetcher.
type vramReaderAdapter struct{ ppu *ppu.PPU }

func (a vramReaderAdapter) Read(addr uint16) byte { return a.ppu.RawVRAM(addr) }

// vramBankedAdapter adapts PPU RawVRAM/RawVRAMBank to the CGB banked VRAM reader interface.
type vramBankedAdapter struct{ ppu *ppu.PPU }

func (a vramBankedAdapter) ReadBank(bank int, addr uint16) byte {
	if bank == 0 {
		return a.ppu.RawVRAM(addr)
	}
	return a.ppu.RawVRAMBank(1, addr)
}

func (m *Machine) renderWindow() {
	if m.bus == nil {
		return
	}
	if m.cfg.UseCGBBG && m.cgbCapable {
		for y := 0; y < 144; y++ {
			lr := m.bus.PPU().LineRegs(y)
			if lr.LCDC == 0 {
				lr.LCDC = m.bus.Read(0xFF40)
				lr.WY = m.bus.Read(0xFF4A)
				lr.WX = m.bus.Read(0xFF4B)
			}
			if (lr.LCDC&0x80) == 0 || (lr.LCDC&0x01) == 0 || (lr.LCDC&0x20) == 0 {
				continue
			}
			if y < int(lr.WY) || int(lr.WY) >= 144 {
				continue
			}
			winXStart := int(lr.WX) - 7
			if winXStart >= 160 {
				continue
			}
			mapBase := uint16(0x9800)
			if (lr.LCDC & 0x40) != 0 {
				mapBase = 0x9C00
			}
			attrsBase := mapBase
			tileData8000 := (lr.LCDC & 0x10) != 0
			vr := vramBankedAdapter{ppu: m.bus.PPU()}
			line, pals, pris := ppu.RenderWindowScanlineCGB(vr, mapBase, attrsBase, tileData8000, winXStart, lr.WinLine)
			useDMGPal := !m.bus.PPU().BGPalReady()
			var bgp byte
			if useDMGPal {
				bgp = m.bus.Read(0xFF47)
			}
			shade := func(ci byte) (byte, byte, byte) {
				shift := ci * 2
				pal := (bgp >> shift) & 0x03
				switch pal {
				case 0:
					return 0xFF, 0xFF, 0xFF
				case 1:
					return 0xC0, 0xC0, 0xC0
				case 2:
					return 0x60, 0x60, 0x60
				default:
					return 0x00, 0x00, 0x00
				}
			}
			for x := max(0, winXStart); x < 160; x++ {
				pal := pals[x]
				ci := line[x]
				var r, g, b byte
				if useDMGPal {
					r, g, b = shade(ci)
				} else {
					r, g, b = m.bus.PPU().BGColorRGB(pal, ci)
				}
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = r, g, b, 0xFF
				m.bgci[y*m.w+x] = ci
				m.bgpal[y*m.w+x] = pal
				m.bgpri[y*m.w+x] = pris[x]
			}
		}
		return
	}
	// Optional fetcher-based window rendering
	if m.cfg.UseFetcherBG {
		for y := 0; y < 144; y++ {
			lr := m.bus.PPU().LineRegs(y)
			if lr.LCDC == 0 {
				lr.LCDC = m.bus.Read(0xFF40)
				lr.WY = m.bus.Read(0xFF4A)
				lr.WX = m.bus.Read(0xFF4B)
				lr.BGP = m.bus.Read(0xFF47)
			}
			if (lr.LCDC&0x80) == 0 || (lr.LCDC&0x01) == 0 || (lr.LCDC&0x20) == 0 {
				continue
			}
			if y < int(lr.WY) || int(lr.WY) >= 144 {
				continue
			}
			winXStart := int(lr.WX) - 7
			if winXStart >= 160 {
				continue
			}
			winMapBase := uint16(0x9800)
			if (lr.LCDC & 0x40) != 0 {
				winMapBase = 0x9C00
			}
			tileData8000 := (lr.LCDC & 0x10) != 0
			// Drive window scanline via fetcher
			vr := vramReaderAdapter{ppu: m.bus.PPU()}
			line := ppu.RenderWindowScanlineUsingFetcher(vr, winMapBase, tileData8000, winXStart, lr.WinLine)
			// Shade and blend onto framebuffer starting at winXStart
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
				ci := line[x]
				s := shade(ci)
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = s, s, s, 0xFF
				m.bgci[y*m.w+x] = ci
			}
		}
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
	if m.cfg.UseCGBBG && m.cgbCapable {
		// CGB OBJ path: use bank, 8 palettes, and CGB priority.
		for y := 0; y < 144; y++ {
			lr := m.bus.PPU().LineRegs(y)
			lcdc := lr.LCDC
			if lcdc == 0 {
				lcdc = m.bus.Read(0xFF40)
			}
			if (lcdc&0x80) == 0 || (lcdc&0x02) == 0 {
				continue
			}
			sprite16 := (lcdc & 0x04) != 0
			// Collect up to 10 sprites covering this line in OAM order
			type oamE struct {
				sy, sx     int
				tile, attr byte
				index      int
			}
			list := make([]oamE, 0, 10)
			for i := 0; i < 40 && len(list) < 10; i++ {
				base := uint16(0xFE00 + i*4)
				sy := int(m.bus.PPU().RawOAM(base)) - 16
				sx := int(m.bus.PPU().RawOAM(base+1)) - 8
				tile := m.bus.PPU().RawOAM(base + 2)
				attr := m.bus.PPU().RawOAM(base + 3)
				h := 8
				if sprite16 {
					h = 16
				}
				if sy <= y && y < sy+h {
					list = append(list, oamE{sy, sx, tile, attr, i})
				}
			}
			if len(list) == 0 {
				continue
			}
			// CGB OBJ drawing priority: OAM order only
			// For each x, find first non-transparent pixel in OAM order, then apply BG-vs-OBJ rules.
			// Snapshot OBP0/OBP1 for DMG fallback shading if OBJ CGB palettes aren't ready yet
			obp0 := lr.OBP0
			obp1 := lr.OBP1
			if lr.LCDC == 0 { // if no snapshot captured yet, use live regs
				obp0 = m.bus.Read(0xFF48)
				obp1 = m.bus.Read(0xFF49)
			}
			useOBJPal := m.bus.PPU().OBJPalReady()
			for x := 0; x < 160; x++ {
				var drawn bool
				for _, s := range list {
					if x < s.sx || x >= s.sx+8 {
						continue
					}
					row := y - s.sy
					col := x - s.sx
					yflip := (s.attr & (1 << 6)) != 0
					xflip := (s.attr & (1 << 5)) != 0
					if yflip {
						if sprite16 {
							row = 15 - row
						} else {
							row = 7 - row
						}
					}
					if xflip {
						col = 7 - col
					}
					tIndex := s.tile
					if sprite16 {
						tIndex &= 0xFE
						if row >= 8 {
							tIndex++
						}
					}
					rowWithin := row & 7
					base := uint16(0x8000) + uint16(tIndex)*16 + uint16(rowWithin)*2
					bank := 0
					if (s.attr & (1 << 3)) != 0 {
						bank = 1
					}
					var lo, hi byte
					if bank == 0 {
						lo = m.bus.PPU().RawVRAM(base)
						hi = m.bus.PPU().RawVRAM(base + 1)
					} else {
						lo = m.bus.PPU().RawVRAMBank(1, base)
						hi = m.bus.PPU().RawVRAMBank(1, base+1)
					}
					bit := 7 - byte(col%8)
					ci := ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
					if ci == 0 {
						continue
					}
					// BG vs OBJ priority (CGB): if BG index is 0 -> OBJ wins; else if LCDC bit0 clear -> OBJ wins
					bgci := m.bgci[y*m.w+x]
					if bgci != 0 {
						if (lcdc & 0x01) == 0 {
							// OBJ wins
						} else {
							bgPri := m.bgpri[y*m.w+x]
							objBehind := (s.attr & (1 << 7)) != 0 // note: in CGB, clear means OBJ priority
							// If both BG attr bit7 and OBJ attr bit7 are clear -> OBJ wins; otherwise BG in front
							if bgPri || objBehind { // BG should be in front for non-zero BG
								continue
							}
						}
					}
					pal := s.attr & 0x07
					var r, g, b byte
					if useOBJPal {
						r, g, b = m.bus.PPU().OBJColorRGB(pal, ci)
					} else {
						// DMG sprite shading via OBP0/OBP1 to avoid white sprites before palettes are set
						// Choose DMG palette by attr bit4
						dmgPal := obp0
						if (s.attr & (1 << 4)) != 0 {
							dmgPal = obp1
						}
						shift := ci * 2
						p := (dmgPal >> shift) & 0x03
						var gray byte
						switch p {
						case 0:
							gray = 0xFF
						case 1:
							gray = 0xC0
						case 2:
							gray = 0x60
						default:
							gray = 0x00
						}
						r, g, b = gray, gray, gray
					}
					i := (y*m.w + x) * 4
					m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = r, g, b, 0xFF
					drawn = true
					break
				}
				_ = drawn
			}
		}
		return
	}
	if m.cfg.UseFetcherBG {
		// Compose sprites over the fetcher-produced bgci/fb per line.
		for y := 0; y < 144; y++ {
			lr := m.bus.PPU().LineRegs(y)
			lcdc := lr.LCDC
			if lcdc == 0 {
				lcdc = m.bus.Read(0xFF40)
			}
			if (lcdc&0x80) == 0 || (lcdc&0x02) == 0 {
				continue
			}
			sprite16 := (lcdc & 0x04) != 0
			obp0 := lr.OBP0
			obp1 := lr.OBP1
			if lr.LCDC == 0 {
				obp0 = m.bus.Read(0xFF48)
				obp1 = m.bus.Read(0xFF49)
			}
			// Gather up to 10 sprites that cover this line in OAM order
			sprites := make([]ppu.Sprite, 0, 10)
			for i := 0; i < 40 && len(sprites) < 10; i++ {
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
					sprites = append(sprites, ppu.Sprite{X: sx, Y: sy, Tile: tile, Attr: attr, OAMIndex: i})
				}
			}
			if len(sprites) == 0 {
				continue
			}
			// Compose sprite pixels over this line with per-pixel palette selection
			var bgciLine [160]byte
			copy(bgciLine[:], m.bgci[y*m.w:(y+1)*m.w])
			vr := vramReaderAdapter{ppu: m.bus.PPU()}
			sline, palSel := ppu.ComposeSpriteLineExt(vr, sprites, y, bgciLine, sprite16)
			// Shade sprites onto framebuffer where non-zero using OBP0/OBP1 based on palSel
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
			for x := 0; x < 160; x++ {
				ci := sline[x]
				if ci == 0 {
					continue
				}
				pal := obp0
				if palSel[x] == 1 {
					pal = obp1
				}
				gray := shadeP(pal, ci)
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = gray, gray, gray, 0xFF
			}
		}
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
