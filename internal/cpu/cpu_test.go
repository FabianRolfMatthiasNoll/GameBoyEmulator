package cpu

import (
	"testing"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/bus"
)

func newCPUWithROM(code []byte) *CPU {
	rom := make([]byte, 0x8000)
	copy(rom, code)
	b := bus.New(rom)
	c := New(b)
	return c
}

func TestCPU_NopAndPC(t *testing.T) {
	c := newCPUWithROM([]byte{0x00}) // NOP
	if cycles := c.Step(); cycles != 4 {
		t.Fatalf("NOP cycles got %d want 4", cycles)
	}
	if c.PC != 1 {
		t.Fatalf("PC after NOP got %#04x want 0x0001", c.PC)
	}
}

func TestCPU_LD_A_d8_And_XOR_A(t *testing.T) {
	c := newCPUWithROM([]byte{0x3E, 0x12, 0xAF}) // LD A,0x12; XOR A
	c.Step()                                     // LD
	if c.A != 0x12 {
		t.Fatalf("A after LD got %02x want 12", c.A)
	}
	c.Step() // XOR A
	if c.A != 0x00 {
		t.Fatalf("A after XOR got %02x want 00", c.A)
	}
	if (c.F & 0x80) == 0 { // Z flag
		t.Fatalf("Z flag not set after XOR A")
	}
}

func TestCPU_LD_a16_A_and_LD_A_a16(t *testing.T) {
	// Program: LD A,0x77; LD (0xC000),A; LD A,0x00; LD A,(0xC000)
	prog := []byte{0x3E, 0x77, 0xEA, 0x00, 0xC0, 0x3E, 0x00, 0xFA, 0x00, 0xC0}
	c := newCPUWithROM(prog)
	c.Step() // LD A,77
	c.Step() // LD (C000),A
	if a := c.bus.Read(0xC000); a != 0x77 {
		t.Fatalf("WRAM at C000 got %02x want 77", a)
	}
	c.Step() // LD A,00
	c.Step() // LD A,(C000)
	if c.A != 0x77 {
		t.Fatalf("A after LD A,(C000) got %02x want 77", c.A)
	}
}

func TestCPU_JP_and_JR(t *testing.T) {
	// JP to 0x0010 then JR -2 to loop
	prog := []byte{0xC3, 0x10, 0x00} // at 0x0000: JP 0x0010
	// Fill until 0x0010 with NOPs
	rom := make([]byte, 0x8000)
	copy(rom, prog)
	for i := 0x0003; i < 0x0010; i++ {
		rom[i] = 0x00
	}
	// at 0x0010: JR -2 (0xFE), which will hop back to 0x0010 itself (infinite)
	rom[0x0010] = 0x18
	rom[0x0011] = 0xFE
	b := bus.New(rom)
	c := New(b)
	cycles := c.Step() // JP
	if cycles != 16 || c.PC != 0x0010 {
		t.Fatalf("JP cycles=%d PC=%#04x want cycles=16 PC=0x0010", cycles, c.PC)
	}
	pcBefore := c.PC
	c.Step()              // JR -2
	if c.PC != pcBefore { // stays at 0x0010
		t.Fatalf("JR -2 PC got %#04x want %#04x", c.PC, pcBefore)
	}
}

func TestCPU_INC_B_Flags(t *testing.T) {
	c := newCPUWithROM([]byte{0x04, 0x04}) // INC B twice
	c.B = 0x0F
	c.F = 0x10 // carry set initially
	c.Step()
	if c.B != 0x10 {
		t.Fatalf("INC B result got %02x want 10", c.B)
	}
	if (c.F & 0x20) == 0 { // H set
		t.Fatalf("INC B should set H flag")
	}
	if (c.F & 0x10) == 0 { // C preserved
		t.Fatalf("INC B should preserve C flag")
	}
	c.B = 0xFF
	c.Step()
	if c.B != 0x00 || (c.F&0x80) == 0 { // Z set
		t.Fatalf("INC B to 0 should set Z flag, B=%02x, F=%02x", c.B, c.F)
	}
}

func TestCPU_LD_16bit_and_LDH(t *testing.T) {
	// Program:
	// LD HL,0xC000; LD (HL),0x5A; LD A,0x00; LD A,(0xFF00+0x00); LD (0xFF00+1),A
	prog := []byte{
		0x21, 0x00, 0xC0, // LD HL, C000
		0x36, 0x5A,       // LD (HL), 5A
		0x3E, 0x00,       // LD A, 00
		0xF0, 0x00,       // LD A, (FF00+0)
		0xE0, 0x01,       // LD (FF00+1), A
	}
	c := newCPUWithROM(prog)
	// Preload FF00 with 0xA7 via bus
	c.Bus().Write(0xFF00, 0x20) // select dpad so read is deterministic
	c.Bus().Write(0xFF00, 0x30) // select none to keep 0x0F
	c.Bus().Write(0xFF80, 0xA7) // HRAM base

	c.Step(); c.Step(); c.Step(); c.Step(); c.Step()
	if v := c.Bus().Read(0xC000); v != 0x5A {
		t.Fatalf("WRAM C000 got %02x want 5A", v)
	}
	if v := c.Bus().Read(0xFF01); v != c.A {
		t.Fatalf("LDH (FF00+1),A expected write to FF01 with A=%02x got %02x", c.A, v)
	}
}

func TestCPU_CALL_RET(t *testing.T) {
	// 0000: CALL 0005; NOP; NOP; NOP; NOP; RET
	rom := make([]byte, 0x8000)
	rom[0x0000] = 0xCD
	rom[0x0001] = 0x05
	rom[0x0002] = 0x00
	for i := 0x0003; i < 0x0005; i++ { rom[i] = 0x00 }
	rom[0x0005] = 0xC9 // RET
	b := bus.New(rom)
	c := New(b)
	c.Step() // CALL
	if c.PC != 0x0005 {
		t.Fatalf("PC after CALL got %04x want 0005", c.PC)
	}
	retCycles := c.Step()
	if c.PC != 0x0003 || retCycles != 16 {
		t.Fatalf("RET did not return to 0003; PC=%04x cyc=%d", c.PC, retCycles)
	}
}

