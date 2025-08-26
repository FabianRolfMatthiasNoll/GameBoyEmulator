package emu

import "github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cart"

type Buttons struct {
	A, B, Start, Select   bool
	Up, Down, Left, Right bool
}

type Machine struct {
	cfg  Config
	w, h int
	fb   []byte // RGBA 160x144*4
	// (later: cpu, ppu, apu, bus, cart, timers...)
}

func New(cfg Config) *Machine {
	return &Machine{
		cfg: cfg, w: 160, h: 144,
		fb: make([]byte, 160*144*4),
	}
}

func (m *Machine) LoadCartridge(rom []byte, boot []byte) error {
	// later: parse header, init cart/MBC, wire bus, reset CPU
	// for Milestone 0 just accept bytes.
	romHeader, err := cart.ParseHeader(rom)
	if err != nil {
		return err
	}
	_ = rom
	_ = boot
	_ = romHeader
	return nil
}

func (m *Machine) StepFrame() {
	// Milestone 0: draw a simple test pattern to prove the pipe
	for y := 0; y < m.h; y++ {
		for x := 0; x < m.w; x++ {
			i := (y*m.w + x) * 4
			m.fb[i+0] = byte(x * 255 / m.w)
			m.fb[i+1] = byte(y * 255 / m.h)
			m.fb[i+2] = 0
			m.fb[i+3] = 0xFF
		}
	}
}

func (m *Machine) Framebuffer() []byte  { return m.fb }
func (m *Machine) SetButtons(b Buttons) { /* store for next step */ }

// func (m *Machine) DrainAudio(max int) []float32 { ... later ... }
