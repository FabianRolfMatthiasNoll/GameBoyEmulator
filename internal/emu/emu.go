package emu

import (
	"bytes"
	"encoding/gob"
	"math"
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
	// If true, we are running a DMG ROM on CGB hardware (compatibility mode):
	// use BG palette 0 and OBJ palettes 0/1 with BGP/OBP mapping into CRAM.
	cgbCompat bool
	// Selected compatibility palette ID (0 = default) when in cgbCompat.
	// 0..len(cgbCompatSets)-1; out-of-range will wrap.
	cgbCompatID int

	romTitle string // decoded title from header (trimmed)
}

// Human-friendly names for the curated DMG-on-CGB compatibility palettes.
var cgbCompatSetNames = []string{
	"Green", "Sepia", "Blue", "Red", "Pastel", "Gray",
	"Teal Shade", "Purple Shade", "Amber Shade",
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
	if romHeader != nil {
		m.romTitle = romHeader.Title
	} else {
		m.romTitle = ""
	}
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
			m.cgbCompat = false
		} else {
			// for pure DMG, default to classic
			m.cfg.UseCGBBG = false
			if m.bus != nil {
				m.bus.SetCGBMode(false)
			}
			m.cgbCompat = false
		}
	}
	// If user had CGB colors toggled on and this ROM is DMG-only, enable compatibility mode now
	if !m.cgbCapable && m.cfg.UseCGBBG && m.bus != nil {
		m.bus.SetCGBMode(true)
		m.cgbCompat = true
		// pick palette based on header checksum heuristic
		if id, ok := computeCompatPaletteIDFromROM(rom); ok {
			m.cgbCompatID = id
		} else {
			m.cgbCompatID = 0
		}
		m.seedCGBCompatPalettes()
	}
	return nil
}

// seedCGBCompatPalettes initializes CGB CRAM for DMG compatibility mode with a sane default.
// This maps BGP/OBP grayscale to color hues to mimic GBC default compatibility palettes.
// For now, use a simple green-ish default similar to DMG-on-GBC default.
func (m *Machine) seedCGBCompatPalettes() {
	m.seedCGBCompatPalettesID(m.cgbCompatID)
}

// seedCGBCompatPalettesID writes one of a few curated DMG-on-CGB color sets into CRAM.
// The set provides 3 palettes: BG0, OBJ0, OBJ1. We approximate GBC boot behavior.
func (m *Machine) seedCGBCompatPalettesID(id int) {
	if m == nil || m.bus == nil || m.bus.PPU() == nil {
		return
	}
	// A few curated palettes (light -> dark). Colors are 8-bit per channel.
	type rgb struct{ r, g, b byte }
	type set struct {
		bg   [4]rgb
		obj0 [4]rgb
		obj1 [4]rgb
	}
	// Helper presets
	green := [4]rgb{{0xE0, 0xF8, 0xD0}, {0x88, 0xC0, 0x70}, {0x34, 0x68, 0x56}, {0x08, 0x18, 0x20}}
	sepia := [4]rgb{{0xF8, 0xE8, 0xC8}, {0xD0, 0xB8, 0x90}, {0x90, 0x70, 0x50}, {0x30, 0x20, 0x10}}
	blue := [4]rgb{{0xE0, 0xF0, 0xFF}, {0x80, 0xB0, 0xE8}, {0x30, 0x60, 0xA8}, {0x10, 0x18, 0x30}}
	red := [4]rgb{{0xFF, 0xE0, 0xE0}, {0xE8, 0x80, 0x80}, {0xB0, 0x30, 0x30}, {0x30, 0x10, 0x10}}
	pastel := [4]rgb{{0xFF, 0xFF, 0xFF}, {0xCC, 0xEE, 0xCC}, {0x99, 0xCC, 0xCC}, {0x66, 0x99, 0x99}}
	gray := [4]rgb{{0xFF, 0xFF, 0xFF}, {0xC0, 0xC0, 0xC0}, {0x60, 0x60, 0x60}, {0x00, 0x00, 0x00}}
	// Build sets (BG, OBJ0, OBJ1). Keep BG vivid; give sprites slight contrast variants.
	cgbCompatSets := []set{
		{bg: green, obj0: green, obj1: sepia},  // 0 default green with sepia alt
		{bg: sepia, obj0: sepia, obj1: green},  // 1 sepia bg
		{bg: blue, obj0: blue, obj1: pastel},   // 2 blue bg
		{bg: red, obj0: red, obj1: sepia},      // 3 red bg
		{bg: pastel, obj0: pastel, obj1: gray}, // 4 pastel
		{bg: gray, obj0: gray, obj1: green},    // 5 neutral gray
	}
	// Shade-based synthetic palettes generated from HSL hues
	genShade := func(h, s float64) [4]rgb {
		// Four luminance levels from light to dark
		L := []float64{0.92, 0.72, 0.45, 0.12}
		var arr [4]rgb
		for i := 0; i < 4; i++ {
			r8, g8, b8 := hslToRGB(h, s, L[i])
			arr[i] = rgb{r8, g8, b8}
		}
		return arr
	}
	teal := genShade(180, 0.55)
	purple := genShade(280, 0.60)
	amber := genShade(45, 0.70)
	// Append synthetic shade sets (BG, OBJ0 same; OBJ1 slightly desaturated via mixing)
	mix := func(a, b rgb, t float64) rgb {
		return rgb{
			byte(float64(a.r)*(1-t) + float64(b.r)*t),
			byte(float64(a.g)*(1-t) + float64(b.g)*t),
			byte(float64(a.b)*(1-t) + float64(b.b)*t),
		}
	}
	toGray := func(c rgb) rgb {
		y := byte(0.2126*float64(c.r) + 0.7152*float64(c.g) + 0.0722*float64(c.b))
		return rgb{y, y, y}
	}
	toObj1 := func(arr [4]rgb) [4]rgb {
		var out [4]rgb
		for i := 0; i < 4; i++ {
			out[i] = mix(arr[i], toGray(arr[i]), 0.25)
		}
		return out
	}
	cgbCompatSets = append(cgbCompatSets,
		set{bg: teal, obj0: teal, obj1: toObj1(teal)},
		set{bg: purple, obj0: purple, obj1: toObj1(purple)},
		set{bg: amber, obj0: amber, obj1: toObj1(amber)},
	)
	if id < 0 {
		id = 0
	}
	if id >= len(cgbCompatSets) {
		id = id % len(cgbCompatSets)
	}
	sel := cgbCompatSets[id]
	// Helper: write one RGB555 color via palette ports (auto-increment assumed)
	writeRGB := func(dataReg uint16, r8, g8, b8 byte) {
		r5 := byte(uint16(r8) >> 3)
		g5 := byte(uint16(g8) >> 3)
		b5 := byte(uint16(b8) >> 3)
		v := uint16(r5) | (uint16(g5) << 5) | (uint16(b5) << 10)
		m.bus.Write(dataReg, byte(v&0xFF))
		m.bus.Write(dataReg, byte(v>>8))
	}
	// BG palette 0 at BCPS index 0 with auto-increment
	m.bus.Write(0xFF68, 0x80)
	for i := 0; i < 4; i++ {
		c := sel.bg[i]
		writeRGB(0xFF69, c.r, c.g, c.b)
	}
	// OBJ palettes 0 and 1: total 8 colors (we’ll write both palettes sequentially)
	m.bus.Write(0xFF6A, 0x80)
	for i := 0; i < 4; i++ { // OBJ0
		c := sel.obj0[i]
		writeRGB(0xFF6B, c.r, c.g, c.b)
	}
	for i := 0; i < 4; i++ { // OBJ1
		c := sel.obj1[i]
		writeRGB(0xFF6B, c.r, c.g, c.b)
	}
}

// hslToRGB converts H, S, L (0..360, 0..1, 0..1) to 8-bit RGB.
func hslToRGB(h, s, l float64) (byte, byte, byte) {
	h = math.Mod(h, 360)
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60.0, 2)-1))
	m := l - c/2
	var r1, g1, b1 float64
	switch {
	case 0 <= h && h < 60:
		r1, g1, b1 = c, x, 0
	case 60 <= h && h < 120:
		r1, g1, b1 = x, c, 0
	case 120 <= h && h < 180:
		r1, g1, b1 = 0, c, x
	case 180 <= h && h < 240:
		r1, g1, b1 = 0, x, c
	case 240 <= h && h < 300:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	r := byte(clamp01((r1 + m) * 255))
	g := byte(clamp01((g1 + m) * 255))
	b := byte(clamp01((b1 + m) * 255))
	return r, g, b
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// SetUseFetcherBG toggles the BG renderer between classic and fetcher-based path.
func (m *Machine) SetUseFetcherBG(on bool) { m.cfg.UseFetcherBG = on }

// SetUseCGBBG toggles the CGB BG/Window/Sprite rendering path using CGB attributes and palettes.
func (m *Machine) SetUseCGBBG(on bool) {
	m.cfg.UseCGBBG = on
	if m.bus != nil {
		// Expose CGB hardware when turning on; DMG ROMs will run in compatibility mode
		if on {
			m.bus.SetCGBMode(true)
			// Enter or exit compatibility mode depending on ROM capability
			m.cgbCompat = !m.cgbCapable
			if m.cgbCompat {
				// keep current chosen compat ID
				m.seedCGBCompatPalettes()
			}
		} else {
			// Disable CGB hardware exposure when turning off for dual carts; for DMG carts this
			// returns to classic behavior as well
			m.bus.SetCGBMode(false)
			m.cgbCompat = false
		}
	}
}

// UseCGBBG reports whether the CGB rendering path is enabled.
func (m *Machine) UseCGBBG() bool { return m.cfg.UseCGBBG && m.cgbCapable }

// WantCGBColors reports the user's intent to enable CGB colorization (even for DMG ROMs).
func (m *Machine) WantCGBColors() bool { return m.cfg.UseCGBBG }

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
	// When in DMG compat mode after load, try to compute a palette ID from header
	if m.cgbCompat {
		if id, ok := computeCompatPaletteIDFromROM(data); ok {
			m.cgbCompatID = id
			m.seedCGBCompatPalettesID(id)
		}
	}
	return nil
}

// ROMPath returns the currently loaded ROM file path, if any.
func (m *Machine) ROMPath() string {
	return m.romPath
}

// ROMTitle returns the title extracted from the ROM header, if available.
func (m *Machine) ROMTitle() string { return m.romTitle }

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
}

// SetCGBBootROM sets the CGB boot ROM image.
func (m *Machine) SetCGBBootROM(data []byte) {
	if len(data) >= 0x800 {
		m.cgbBootROM = make([]byte, 0x800)
		// accept either full 0x800 CGB boot or a larger blob containing it
		copy(m.cgbBootROM, data[len(data)-0x800:])
	} else {
		m.cgbBootROM = nil
	}
	if m.bus != nil && len(m.cgbBootROM) >= 0x800 {
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
	// Track compatibility mode for DMG ROMs under CGB
	m.cgbCompat = compat
	// Clear any boot mapping
	m.bus.EnableBoot(0)
	// CPU state like CGB after boot
	m.cpu.ResetNoBoot()
	m.cpu.SetPC(0x0100)
	m.cpu.A = 0x11 // indicate CGB hardware per Pan Docs
	// Set minimal IO similar to applyDMGPostBootIO
	m.applyDMGPostBootIO()
	// Seed default compatibility palettes when running a DMG ROM under CGB
	if compat {
		m.seedCGBCompatPalettes()
	}
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
	m.stepFrameCPU()
	// Render background, window, then sprites
	m.renderBG()
	m.renderWindow()
	m.renderSprites()
}

// StepFrameNoRender advances one frame of emulation without producing a new framebuffer.
func (m *Machine) StepFrameNoRender() { m.stepFrameCPU() }

// stepFrameCPU advances CPU for approximately one frame worth of cycles (~70224).
func (m *Machine) stepFrameCPU() {
	if m.cpu == nil {
		return
	}
	target := 70224
	acc := 0
	for acc < target {
		acc += m.cpu.Step()
	}
}

func (m *Machine) Framebuffer() []byte { return m.fb }

// IsCGBCompat reports if we're running a DMG ROM under CGB colorization.
func (m *Machine) IsCGBCompat() bool { return m != nil && m.cgbCompat }

// CurrentCompatPalette returns the current compat palette ID (0-based).
func (m *Machine) CurrentCompatPalette() int { return m.cgbCompatID }

// SetCompatPalette selects a compat palette ID and seeds CRAM accordingly.
func (m *Machine) SetCompatPalette(id int) {
	if !m.cgbCompat {
		return
	}
	if id < 0 {
		id = 0
	}
	m.cgbCompatID = id
	m.seedCGBCompatPalettesID(id)
}

// CycleCompatPalette increments the compat palette with wrap-around across available sets.
func (m *Machine) CycleCompatPalette(delta int) {
	if !m.cgbCompat {
		return
	}
	total := len(cgbCompatSetNames)
	id := m.cgbCompatID + delta
	for id < 0 {
		id += total
	}
	id = id % total
	m.cgbCompatID = id
	m.seedCGBCompatPalettesID(id)
}

// CompatPaletteName returns a friendly name for the given compat palette ID.
func (m *Machine) CompatPaletteName(id int) string {
	if id < 0 {
		id = 0
	}
	if len(cgbCompatSetNames) == 0 {
		return ""
	}
	if id >= len(cgbCompatSetNames) {
		id = id % len(cgbCompatSetNames)
	}
	return cgbCompatSetNames[id]
}

// CompatPaletteCount returns the number of available compat palettes.
func (m *Machine) CompatPaletteCount() int { return len(cgbCompatSetNames) }

// computeCompatPaletteIDFromROM chooses a DMG-on-CGB palette using header tables and heuristics.
func computeCompatPaletteIDFromROM(rom []byte) (int, bool) {
	if h, err := cart.ParseHeader(rom); err == nil {
		if id, ok := autoCompatPaletteFromHeader(h); ok {
			return id, true
		}
	}
	return 0, true
}

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
	Bus         []byte
	CPU         []byte
	CGBCompat   bool
	CGBCompatID int
}

func (m *Machine) SaveState() []byte {
	if m == nil || m.bus == nil || m.cpu == nil {
		return nil
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	_ = enc.Encode(machineState{
		Bus:         m.bus.SaveState(),
		CPU:         m.cpu.SaveState(),
		CGBCompat:   m.cgbCompat,
		CGBCompatID: m.cgbCompatID,
	})
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
	// Reconcile loaded state with current user color toggle and ROM capability.
	// Goal: If user has Colors OFF for a DMG ROM, do not force color back ON when loading a colored savestate.
	// For CGB-capable games, we leave the state as-is (don’t coerce DMG<->CGB at load).
	wantColors := m.cfg.UseCGBBG
	if !wantColors && !m.cgbCapable {
		// Colors disabled and DMG-only ROM: enforce DMG classic after load.
		m.cgbCompat = false
		m.cgbCompatID = 0
		m.bus.SetCGBMode(false)
	} else {
		// Respect the loaded state; enable compat only when user still wants colors.
		m.cgbCompat = s.CGBCompat && !m.cgbCapable && wantColors
		m.cgbCompatID = s.CGBCompatID
		if m.cgbCompat {
			m.bus.SetCGBMode(true)
			m.seedCGBCompatPalettesID(m.cgbCompatID)
		} else if m.cgbCapable && wantColors {
			// CGB-capable game with colors on: ensure CGB hardware exposed
			m.bus.SetCGBMode(true)
		}
	}
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
			// Shade and write out: if in CGB compatibility mode, map via CRAM BG palette 0; otherwise DMG grayscale
			bgp := lr.BGP
			useCompat := m.cgbCompat && m.bus.PPU().BGPalReady()
			shade := func(ci byte) (r, g, b byte) {
				if useCompat {
					// In compatibility mode, BGP indexes colors within CGB BG palette 0
					return m.bus.PPU().BGColorRGB(0, ci)
				}
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
				ci := line[x]
				r, g, b := shade(ci)
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = r, g, b, 0xFF
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
		// In DMG-on-CGB compatibility mode, if BG CGB palettes are populated, use CRAM BG palette 0
		useCompat := m.cgbCompat && m.bus.PPU().BGPalReady()
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
			// map BG color using the snapshot palette or compat CRAM palette 0
			if useCompat {
				r, g, b := m.bus.PPU().BGColorRGB(0, ci)
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = r, g, b, 0xFF
			} else {
				bgp := lr.BGP
				shift := ci * 2
				pal := (bgp >> shift) & 0x03
				var s byte
				switch pal {
				case 0:
					s = 0xFF
				case 1:
					s = 0xC0
				case 2:
					s = 0x60
				default:
					s = 0x00
				}
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = s, s, s, 0xFF
			}
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
		useCompat := m.cgbCompat && m.bus.PPU().BGPalReady()
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
			if useCompat {
				r, g, b := m.bus.PPU().BGColorRGB(0, ci)
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = r, g, b, 0xFF
			} else {
				shift := ci * 2
				pal := (bgp >> shift) & 0x03
				var s byte
				switch pal {
				case 0:
					s = 0xFF
				case 1:
					s = 0xC0
				case 2:
					s = 0x60
				default:
					s = 0x00
				}
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = s, s, s, 0xFF
			}
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
			// Shade sprites: in CGB compatibility mode, map via OBJ CRAM palettes 0/1; else DMG grayscale
			shadeP := func(palSelIdx byte, pal byte, ci byte) (r, g, b byte) {
				if m.cgbCompat && m.bus.PPU().OBJPalReady() {
					// palSelIdx 0->OBJ0, 1->OBJ1
					return m.bus.PPU().OBJColorRGB(palSelIdx, ci)
				}
				shift := ci * 2
				p := (pal >> shift) & 0x03
				switch p {
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
				ci := sline[x]
				if ci == 0 {
					continue
				}
				pal := obp0
				idx := byte(0)
				if palSel[x] == 1 {
					pal = obp1
					idx = 1
				}
				r, g, b := shadeP(idx, pal, ci)
				i := (y*m.w + x) * 4
				m.fb[i+0], m.fb[i+1], m.fb[i+2], m.fb[i+3] = r, g, b, 0xFF
			}
		}
		return
	}
	// We'll use per-line snapshots for LCDC and palettes
	shadeP := func(palSelIdx byte, pal byte, ci byte) (byte, byte, byte) {
		if m.cgbCompat && m.bus.PPU().OBJPalReady() {
			return m.bus.PPU().OBJColorRGB(palSelIdx, ci)
		}
		shift := ci * 2
		p := (pal >> shift) & 0x03
		switch p {
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
		if (lcdc&0x80) == 0 || (lcdc&0x02) == 0 {
			continue
		}
		sprite16 := (lcdc & 0x04) != 0
		obp0 := lr.OBP0
		obp1 := lr.OBP1
		if lr.LCDC == 0 { // fallback to live palettes if snapshot missing
			obp0 = m.bus.Read(0xFF48)
			obp1 = m.bus.Read(0xFF49)
		}
		candidates := make([]oamEntry, 0, 10)
		for i := 0; i < 40 && len(candidates) < 10; i++ {
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
				candidates = append(candidates, oamEntry{sy: sy, sx: sx, tile: tile, attr: attr, index: i})
			}
		}
		if len(candidates) == 0 {
			continue
		}
		// Stable sort by X then OAM index
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
			bestR, bestG, bestB := byte(0), byte(0), byte(0)
			for _, s := range candidates {
				if x < s.sx || x >= s.sx+8 {
					continue
				}
				// OBJ-to-BG priority: if bit7 set and BG/window pixel is non-zero, skip drawing
				if (s.attr&(1<<7)) != 0 && m.bgci[y*m.w+x] != 0 {
					continue
				}
				row := y - s.sy
				col := x - s.sx
				if (s.attr & (1 << 6)) != 0 { // Y flip
					if sprite16 {
						row = 15 - row
					} else {
						row = 7 - row
					}
				}
				if (s.attr & (1 << 5)) != 0 { // X flip
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
				tileAddr := uint16(0x8000) + uint16(tIndex)*16 + uint16(rowWithin)*2
				lo := m.bus.PPU().RawVRAM(tileAddr)
				hi := m.bus.PPU().RawVRAM(tileAddr + 1)
				bit := 7 - byte(col%8)
				ci := ((hi>>bit)&1)<<1 | ((lo >> bit) & 1)
				if ci == 0 {
					continue
				}
				pal := obp0
				idx := byte(0)
				if (s.attr & (1 << 4)) != 0 {
					pal = obp1
					idx = 1
				}
				r, g, b := shadeP(idx, pal, ci)
				if !bestFound || s.sx < bestX || (s.sx == bestX && s.index < bestIdx) {
					bestR, bestG, bestB = r, g, b
					bestX = s.sx
					bestIdx = s.index
					bestFound = true
				}
			}
			if bestFound {
				i := (y*m.w + x) * 4
				m.fb[i+0] = bestR
				m.fb[i+1] = bestG
				m.fb[i+2] = bestB
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
