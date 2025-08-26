package bus

import "testing"

func TestBus_ROMAndRAM(t *testing.T) {
	rom := make([]byte, 0x8000)
	rom[0x0100] = 0x42
	b := New(rom)

	if got := b.Read(0x0100); got != 0x42 {
		t.Fatalf("ROM read got %02x, want 42", got)
	}

	// RAM write+read
	b.Write(0xC000, 0x99)
	if got := b.Read(0xC000); got != 0x99 {
		t.Fatalf("RAM read got %02x, want 99", got)
	}

	// Echo RAM mirrors C000–DDFF
	b.Write(0xE000, 0x55)
	if got := b.Read(0xC000); got != 0x55 {
		t.Fatalf("Echo write did not mirror to WRAM: got %02x", got)
	}

	// HRAM read/write
	b.Write(0xFF80, 0xAB)
	if got := b.Read(0xFF80); got != 0xAB {
		t.Fatalf("HRAM read got %02x, want AB", got)
	}

	// ROM-only cart should return 0xFF for A000–BFFF
	if got := b.Read(0xA123); got != 0xFF {
		t.Fatalf("Ext RAM (ROM-only) got %02x, want FF", got)
	}
}

func TestBus_VRAM_OAM_InterruptRegs(t *testing.T) {
	b := New(make([]byte, 0x8000))

	// VRAM
	b.Write(0x8000, 0x11)
	if got := b.Read(0x8000); got != 0x11 {
		t.Fatalf("VRAM read got %02x, want 11", got)
	}

	// OAM
	b.Write(0xFE00, 0x22)
	if got := b.Read(0xFE00); got != 0x22 {
		t.Fatalf("OAM read got %02x, want 22", got)
	}

	// IF register at 0xFF0F (lower 5 bits)
	b.Write(0xFF0F, 0x3F) // bits 5-7 ignored on read
	if got := b.Read(0xFF0F); got != 0xE0|0x1F {
		t.Fatalf("IF read got %02x, want FF (E0|1F)", got)
	}

	// IE at 0xFFFF
	b.Write(0xFFFF, 0x1B)
	if got := b.Read(0xFFFF); got != 0x1B {
		t.Fatalf("IE read got %02x, want 1B", got)
	}
}

func TestBus_JOYP_And_Timers(t *testing.T) {
	b := New(make([]byte, 0x8000))

	// Default JOYP read (no selection set -> both groups unselected => 1s in lower 4 bits)
	if got := b.Read(0xFF00); got&0x0F != 0x0F {
		t.Fatalf("JOYP default lower bits got %02x want 0x0F", got)
	}

	// Select D-Pad (P14=0), press Right+Up
	b.Write(0xFF00, 0x20) // bit5=1, bit4=0
	b.SetJoypadState(JoypRight | JoypUp)
	got := b.Read(0xFF00)
	if got&0x0F != 0x0A { // 1010b: Right and Up cleared
		t.Fatalf("JOYP D-Pad got %02x want 0x0A", got&0x0F)
	}

	// Select Buttons (P15=0), press A+Start
	b.Write(0xFF00, 0x10) // bit5=0, bit4=1
	b.SetJoypadState(JoypA | JoypStart)
	got = b.Read(0xFF00)
	if got&0x0F != 0x06 { // 0110b: A and Start cleared
		t.Fatalf("JOYP Buttons got %02x want 0x06", got&0x0F)
	}

	// Timers basic RW
	b.Write(0xFF04, 0x12) // DIV write resets to 0
	if got := b.Read(0xFF04); got != 0x00 {
		t.Fatalf("DIV got %02x want 00", got)
	}
	b.Write(0xFF05, 0x77)
	if got := b.Read(0xFF05); got != 0x77 {
		t.Fatalf("TIMA got %02x want 77", got)
	}
	b.Write(0xFF06, 0x88)
	if got := b.Read(0xFF06); got != 0x88 {
		t.Fatalf("TMA got %02x want 88", got)
	}
	b.Write(0xFF07, 0xFD)
	if got := b.Read(0xFF07); got != (0xF8 | (0xFD & 0x07)) {
		t.Fatalf("TAC got %02x want %02x", got, 0xF8|(0xFD&0x07))
	}
}

func TestBus_SerialImmediate(t *testing.T) {
	b := New(make([]byte, 0x8000))
	var out []byte
	b.SetSerialWriter(writerFunc(func(p []byte) (int, error) {
		out = append(out, p...)
		return len(p), nil
	}))

	b.Write(0xFF01, 0x41) // 'A'
	b.Write(0xFF02, 0x81) // start, external clock
	if len(out) != 1 || out[0] != 0x41 {
		t.Fatalf("serial out got %v want [0x41]", out)
	}
	if got := b.Read(0xFF02); (got & 0x80) != 0 { // transfer done => bit7 cleared
		t.Fatalf("serial control bit7 not cleared: %02x", got)
	}
	if (b.Read(0xFF0F) & (1 << 3)) == 0 { // IF bit3 set
		t.Fatalf("serial IF bit not set after transfer")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
