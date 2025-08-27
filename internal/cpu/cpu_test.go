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
		0x36, 0x5A, // LD (HL), 5A
		0x3E, 0x00, // LD A, 00
		0xF0, 0x00, // LD A, (FF00+0)
		0xE0, 0x01, // LD (FF00+1), A
	}
	c := newCPUWithROM(prog)
	// Preload FF00 with 0xA7 via bus
	c.Bus().Write(0xFF00, 0x20) // select dpad so read is deterministic
	c.Bus().Write(0xFF00, 0x30) // select none to keep 0x0F
	c.Bus().Write(0xFF80, 0xA7) // HRAM base

	c.Step()
	c.Step()
	c.Step()
	c.Step()
	c.Step()
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
	for i := 0x0003; i < 0x0005; i++ {
		rom[i] = 0x00
	}
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

func TestCPU_InterruptServiceAndHALT(t *testing.T) {
	// ROM with NOPs; we’ll manually set IF/IE and IME
	rom := make([]byte, 0x8000)
	b := bus.New(rom)
	c := New(b)
	c.SetPC(0x0100)

	// Set up IME and request VBlank (bit0) with IE enabled
	c.IME = true
	b.Write(0xFFFF, 0x01) // IE VBlank
	b.Write(0xFF0F, 0x01) // IF VBlank

	cycles := c.Step()
	if cycles != 20 {
		t.Fatalf("expected 20 cycles for interrupt service, got %d", cycles)
	}
	if c.PC != 0x0040 {
		t.Fatalf("expected PC at 0x0040 vector, got %04X", c.PC)
	}

	// Ensure IME cleared after service
	if c.IME {
		t.Fatal("IME should be cleared after interrupt service")
	}

	// Test HALT waking up without IME when IF&IE != 0
	c.halted = true
	b.Write(0xFFFF, 0x02) // enable LCD STAT
	b.Write(0xFF0F, 0x02) // request STAT
	cyc := c.Step()
	if cyc != 4 {
		t.Fatalf("halt step without servicing should take 4 cycles, got %d", cyc)
	}
	if c.halted {
		t.Fatal("HALT should wake (become unhalted) when IF&IE!=0 even with IME=0 (simplified HALT bug)")
	}
}

func TestCPU_DAA_AddAndSub(t *testing.T) {
	// Program: LD A,0x45; ADD A,0x38; DAA -> 0x83 becomes 0x83 with flags Z=0,N=0,H=0,C=0
	rom := make([]byte, 0x8000)
	rom[0x0000] = 0x3E // LD A, d8
	rom[0x0001] = 0x45
	rom[0x0002] = 0xC6 // ADD A, d8
	rom[0x0003] = 0x38
	rom[0x0004] = 0x27 // DAA
	b := bus.New(rom)
	c := New(b)
	c.Step() // LD
	c.Step() // ADD
	c.Step() // DAA
	if c.A != 0x83 {
		t.Fatalf("DAA after add got A=%02X want 83", c.A)
	}
	if (c.F&0x80) != 0 || (c.F&0x40) != 0 || (c.F&0x20) != 0 || (c.F&0x10) != 0 {
		t.Fatalf("DAA flags unexpected F=%02X", c.F)
	}

	// Subtraction case: 0x45 - 0x06 = 0x3F; DAA should adjust to 0x39 (subtract 0x06 due to H), N=1
	rom[0x0010] = 0x3E // LD A, d8
	rom[0x0011] = 0x45
	rom[0x0012] = 0xD6 // SUB d8
	rom[0x0013] = 0x06
	rom[0x0014] = 0x27 // DAA
	c.PC = 0x0010
	c.Step()
	c.Step()
	c.Step()
	if c.A != 0x39 || (c.F&0x40) == 0 { // N set
		t.Fatalf("DAA after sub got A=%02X F=%02X", c.A, c.F)
	}
}

func TestCPU_EI_DelayedEnable(t *testing.T) {
	// Program: EI; NOP; then an interrupt should be serviced on the NEXT instruction boundary
	rom := make([]byte, 0x8000)
	rom[0x0000] = 0xFB // EI
	rom[0x0001] = 0x00 // NOP
	b := bus.New(rom)
	c := New(b)
	c.SetPC(0x0000)
	// Set IE and IF for VBlank
	b.Write(0xFFFF, 0x01)
	b.Write(0xFF0F, 0x01)
	// Step EI: IME should not be enabled until next instruction start
	c.Step()
	if c.IME {
		t.Fatalf("IME should not be enabled immediately after EI")
	}
	// Step NOP: EI should take effect before executing NOP and interrupt serviced
	cyc := c.Step()
	if c.PC != 0x0040 || cyc != 20 {
		t.Fatalf("interrupt not serviced after EI delay; PC=%04X cyc=%d", c.PC, cyc)
	}
}

func TestCPU_STOP_ConsumesPadding(t *testing.T) {
	// Program: STOP 00; NOP -> after STOP, PC should advance by 2 then execute NOP
	rom := make([]byte, 0x8000)
	rom[0x0000] = 0x10 // STOP
	rom[0x0001] = 0x00 // padding
	rom[0x0002] = 0x00 // NOP
	b := bus.New(rom)
	c := New(b)
	cycles := c.Step() // STOP
	if cycles != 4 {
		t.Fatalf("STOP cycles got %d want 4", cycles)
	}
	if c.PC != 0x0002 {
		t.Fatalf("PC after STOP got %04X want 0002", c.PC)
	}
	c.Step() // NOP
	if c.PC != 0x0003 {
		t.Fatalf("PC after NOP got %04X want 0003", c.PC)
	}
}

func TestCPU_HALT_Bug_DoubleFetch(t *testing.T) {
	// Arrange a pending interrupt with IME=0, execute HALT, then ensure next opcode byte is double-read
	rom := make([]byte, 0x8000)
	// Program: HALT; NOP; (the HALT bug will cause the NOP (0x00) to be fetched twice)
	rom[0x0000] = 0x76 // HALT
	rom[0x0001] = 0x00 // NOP
	b := bus.New(rom)
	c := New(b)
	c.IME = false
	// Set IF&IE pending (e.g., VBlank)
	b.Write(0xFFFF, 0x01)
	b.Write(0xFF0F, 0x01)

	// Execute HALT: should set haltBug and continue running
	cyc := c.Step()
	if cyc != 4 || c.halted {
		t.Fatalf("HALT bug: step after HALT got cyc=%d halted=%v", cyc, c.halted)
	}
	// Next Step: fetch NOP but do not advance PC due to haltBug, then execute it
	pcBefore := c.PC
	c.Step()
	if c.PC != pcBefore+1 {
		t.Fatalf("HALT bug: expected PC to advance by 1 after double-fetch NOP, got %04X->%04X", pcBefore, c.PC)
	}
}

func TestCPU_CB_Prefix_CyclesAndBehavior(t *testing.T) {
	// Program writes HL=C000, sets (HL)=0x80, then runs CB opcodes in sequence
	rom := make([]byte, 0x8000)
	i := 0
	emit := func(b ...byte) { copy(rom[i:], b); i += len(b) }
	// Setup
	emit(0x21, 0x00, 0xC0) // LD HL,C000
	emit(0x36, 0x80)       // LD (HL),80
	// Tests
	emit(0xCB, 0x7E) // BIT 7,(HL)
	emit(0xCB, 0xBE) // RES 7,(HL)
	emit(0xCB, 0xC6) // SET 0,(HL)
	emit(0xCB, 0x00) // RLC B

	b := bus.New(rom)
	c := New(b)
	// Execute setup
	c.Step() // LD HL, C000
	c.Step() // LD (HL), 80
	// BIT 7,(HL) should be Z=0 and take 12 cycles
	cyc := c.Step()
	if cyc != 12 || (c.F&0x80) != 0 { // Z should be 0 because bit7 is 1
		t.Fatalf("BIT 7,(HL) cycles/Z got cyc=%d F=%02X", cyc, c.F)
	}
	// RES 7,(HL) should clear bit7 and take 16 cycles
	cyc = c.Step()
	if cyc != 16 || b.Read(0xC000) != 0x00 {
		t.Fatalf("RES 7,(HL) got cyc=%d mem=%02X", cyc, b.Read(0xC000))
	}
	// SET 0,(HL) should set bit0 and take 16 cycles
	cyc = c.Step()
	if cyc != 16 || b.Read(0xC000) != 0x01 {
		t.Fatalf("SET 0,(HL) got cyc=%d mem=%02X", cyc, b.Read(0xC000))
	}
	// RLC B should take 8 cycles and set C from old bit7
	c.B = 0x80
	cyc = c.Step()
	if cyc != 8 || c.B != 0x01 || (c.F&0x10) == 0 { // C flag set
		t.Fatalf("RLC B got cyc=%d B=%02X F=%02X", cyc, c.B, c.F)
	}
}

func TestCPU_ADD_HL_FlagsAndCarry(t *testing.T) {
	// Program: LD HL,0x0FFF; LD BC,0x0001; ADD HL,BC; then LD HL,0xFFFF; LD BC,0x0001; ADD HL,BC
	rom := make([]byte, 0x8000)
	i := 0
	emit := func(b ...byte) { copy(rom[i:], b); i += len(b) }
	emit(0x21, 0xFF, 0x0F) // LD HL,0x0FFF
	emit(0x01, 0x01, 0x00) // LD BC,0x0001
	emit(0x09)             // ADD HL,BC
	emit(0x21, 0xFF, 0xFF) // LD HL,0xFFFF
	emit(0x01, 0x01, 0x00) // LD BC,0x0001
	emit(0x09)             // ADD HL,BC

	b := bus.New(rom)
	c := New(b)
	// First add: set Z beforehand to check preservation
	c.F = 0x80
	c.Step() // LD HL
	c.Step() // LD BC
	c.F = 0x80
	c.Step() // ADD HL,BC -> 0x0FFF + 1 = 0x1000, H=1, C=0, N=0, Z preserved
	if (c.F&0x80) == 0 || (c.F&0x40) != 0 || (c.F&0x20) == 0 || (c.F&0x10) != 0 {
		t.Fatalf("ADD HL,BC flags #1 F=%02X (expect Z=1 N=0 H=1 C=0)", c.F)
	}
	// Second add: 0xFFFF + 1 = 0x0000, H=1, C=1, Z preserved
	c.Step() // LD HL
	c.Step() // LD BC
	c.F = 0x00 // Z cleared should remain cleared
	c.Step()   // ADD HL,BC
	if (c.F&0x80) != 0 || (c.F&0x40) != 0 || (c.F&0x20) == 0 || (c.F&0x10) == 0 {
		t.Fatalf("ADD HL,BC flags #2 F=%02X (expect Z=0 N=0 H=1 C=1)", c.F)
	}
}

func TestCPU_16bit_INC_DEC_DoNotAffectFlags(t *testing.T) {
	rom := []byte{
		0x03, // INC BC
		0x0B, // DEC BC
		0x23, // INC HL
		0x2B, // DEC HL
		0x13, // INC DE
		0x1B, // DEC DE
		0x33, // INC SP
		0x3B, // DEC SP
	}
	b := bus.New(rom)
	c := New(b)
	c.F = 0xF0
	for range rom {
		c.Step()
		if c.F != 0xF0 {
			t.Fatalf("16-bit INC/DEC should not change flags; F=%02X", c.F)
		}
	}
}

func TestCPU_Conditional_Cycles(t *testing.T) {
	// JR NZ,+2; NOP; NOP
	rom := make([]byte, 0x8000)
	rom[0x0000] = 0x20
	rom[0x0001] = 0x02
	rom[0x0002] = 0x00
	rom[0x0003] = 0x00
	b := bus.New(rom)
	c := New(b)
	// Taken when Z=0 => 12 cycles
	c.F = 0x00
	cyc := c.Step()
	if cyc != 12 || c.PC != 0x0004 {
		t.Fatalf("JR NZ taken cycles/PC: cyc=%d PC=%04X", cyc, c.PC)
	}
	// Not taken when Z=1 => 8 cycles
	c.PC = 0x0000
	c.F = 0x80
	cyc = c.Step()
	if cyc != 8 || c.PC != 0x0002 {
		t.Fatalf("JR NZ not-taken cycles/PC: cyc=%d PC=%04X", cyc, c.PC)
	}

	// JP NC,a16
	rom[0x0010] = 0xD2
	rom[0x0011] = 0x34
	rom[0x0012] = 0x12
	c.PC = 0x0010
	c.F = 0x00 // C=0, taken => 16
	cyc = c.Step()
	if cyc != 16 || c.PC != 0x1234 {
		t.Fatalf("JP NC taken cycles/PC: cyc=%d PC=%04X", cyc, c.PC)
	}
	// Not taken
	c.PC = 0x0010
	c.F = 0x10 // C=1
	cyc = c.Step()
	if cyc != 12 || c.PC != 0x0013 {
		t.Fatalf("JP NC not-taken cycles/PC: cyc=%d PC=%04X", cyc, c.PC)
	}

	// CALL NZ,a16 and RET C
	rom[0x0020] = 0xC4
	rom[0x0021] = 0x00
	rom[0x0022] = 0x40
	c.PC = 0x0020
	c.F = 0x00 // Z=0 => taken
	cyc = c.Step()
	if cyc != 24 || c.PC != 0x4000 {
		t.Fatalf("CALL NZ taken cycles/PC: cyc=%d PC=%04X", cyc, c.PC)
	}
	// Place RET C at 0x4000
	rom[0x4000] = 0xD8 // RET C
	c.F = 0x10         // C=1 => taken
	cyc = c.Step()
	if cyc != 20 {
		t.Fatalf("RET C taken cycles=%d", cyc)
	}
}

func TestCPU_ADC_SBC_HalfCarry(t *testing.T) {
	// ADC: A=0x0F + 0x00 + C=1 => 0x10, H=1, C=0
	rom := []byte{0x3E, 0x0F, 0xCE, 0x00} // LD A,0F; ADC A,00
	b := bus.New(rom)
	c := New(b)
	c.F = 0x10 // set carry in
	c.Step()
	c.Step()
	if c.A != 0x10 || (c.F&0x20) == 0 || (c.F&0x10) != 0 {
		t.Fatalf("ADC half-carry failed: A=%02X F=%02X", c.A, c.F)
	}
	// SBC: A=0x10 - 0x01 - C=0 => 0x0F, H=1, C=0
	rom2 := []byte{0x3E, 0x10, 0xDE, 0x01} // LD A,10; SBC A,01
	b2 := bus.New(rom2)
	c2 := New(b2)
	c2.F = 0x00 // C=0
	c2.Step()
	c2.Step()
	if c2.A != 0x0F || (c2.F&0x20) == 0 || (c2.F&0x10) != 0 {
		t.Fatalf("SBC half-borrow failed: A=%02X F=%02X", c2.A, c2.F)
	}
	// SBC borrow case: A=0x00 - 0x01 => 0xFF, H=1, C=1
	rom3 := []byte{0x3E, 0x00, 0xDE, 0x01}
	b3 := bus.New(rom3)
	c3 := New(b3)
	c3.Step()
	c3.Step()
	if c3.A != 0xFF || (c3.F&0x20) == 0 || (c3.F&0x10) == 0 {
		t.Fatalf("SBC borrow flags failed: A=%02X F=%02X", c3.A, c3.F)
	}
}

func TestCPU_LD_HL_SP_plus_r8_and_ADD_SP_r8_Flags(t *testing.T) {
	// Sequence: LD SP,0xFF0F; LD HL,SP+(-1); ADD SP,+1; ADD SP,-2
	rom := []byte{
		0x31, 0x0F, 0xFF, // LD SP,FF0F
		0xF8, 0xFF, // LD HL,SP-1 => FF0E, expect H=1,C=1 (borrow across low nibble and byte)
		0xE8, 0x01, // ADD SP,+1 => FF10, expect H=1,C=0
		0xE8, 0xFE, // ADD SP,-2 => FF0E, expect H=1,C=1
	}
	b := bus.New(rom)
	c := New(b)
	c.Step() // LD SP
	c.Step() // LD HL,SP-1
	if c.getHL() != 0xFF0E || (c.F&0x20) == 0 || (c.F&0x10) == 0 { // H=1,C=1
		t.Fatalf("LD HL,SP-1 flags/HL wrong: HL=%04X F=%02X", c.getHL(), c.F)
	}
	c.Step() // ADD SP,+1
	if c.SP != 0xFF10 || (c.F&0x20) == 0 || (c.F&0x10) != 0 { // H=1,C=0
		t.Fatalf("ADD SP,+1 flags/SP wrong: SP=%04X F=%02X", c.SP, c.F)
	}
	c.Step() // ADD SP,-2
	if c.SP != 0xFF0E || (c.F&0x20) != 0 || (c.F&0x10) == 0 { // H=0,C=1
		t.Fatalf("ADD SP,-2 flags/SP wrong: SP=%04X F=%02X", c.SP, c.F)
	}
}

func TestCPU_POP_AF_MasksFlagsLowNibble(t *testing.T) {
	// Program: PUSH AF; then overwrite stack to value with low nibble set; POP AF; verify F low nibble cleared
	rom := make([]byte, 0x8000)
	// Minimal program: PUSH AF; POP AF; NOP to end
	rom[0x0000] = 0xF5 // PUSH AF
	rom[0x0001] = 0xF1 // POP AF
	rom[0x0002] = 0x00 // NOP
	b := bus.New(rom)
	c := New(b)
	c.A = 0x12
	c.F = 0xF0
	// Execute PUSH AF
	c.Step()
	// Overwrite stack memory directly to simulate flags with low nibble set
	sp := c.SP
	// AF was pushed at [SP..SP+1]; write 0x34 for A and 0xFF for F to test masking
	b.Write(sp, 0x34)     // low byte (F)
	b.Write(sp+1, 0x12)   // high byte (A)
	// POP AF should load A=0x12 and F masked to 0xF0 (from 0x34 -> 0x30)
	c.Step()
	if c.A != 0x12 {
		t.Fatalf("POP AF A got %02X want 12", c.A)
	}
	if c.F&0x0F != 0x00 {
		t.Fatalf("POP AF should clear low nibble of F, got F=%02X", c.F)
	}
}

func TestCPU_UnprefixedRotates_ClearZ(t *testing.T) {
	// RLCA, RRCA, RLA, RRA must always clear Z, even if result is 0
	rom := []byte{
		0x07, // RLCA
		0x0F, // RRCA
		0x17, // RLA
		0x1F, // RRA
	}
	b := bus.New(rom)
	c := New(b)
	c.A = 0x00
	c.F = 0x80 // Z set initially
	c.Step()
	if (c.F&0x80) != 0 {
		t.Fatalf("RLCA should clear Z, F=%02X", c.F)
	}
	c.F = 0x80
	c.Step()
	if (c.F&0x80) != 0 {
		t.Fatalf("RRCA should clear Z, F=%02X", c.F)
	}
	c.F = 0x90 // carry set
	c.Step()
	if (c.F&0x80) != 0 {
		t.Fatalf("RLA should clear Z, F=%02X", c.F)
	}
	c.F = 0x10 // carry set, Z clear already
	c.Step()
	if (c.F&0x80) != 0 {
		t.Fatalf("RRA should clear Z, F=%02X", c.F)
	}
}

func TestCPU_CCF_SCF_CPL_Flags(t *testing.T) {
	// Verify flag behavior for CCF/SCF/CPL
	rom := []byte{
		0x3E, 0x00, // LD A,00
		0x37,       // SCF: C=1, Z preserved, N=H=0
		0x3F,       // CCF: toggle C
		0x2F,       // CPL: A=~A, N=H=1, Z unchanged, C unchanged
	}
	b := bus.New(rom)
	c := New(b)
	c.F = 0x80 // Z set initially
	c.Step()   // LD A,00
	c.Step()   // SCF
	if (c.F&0x10) == 0 || (c.F&0x80) == 0 || (c.F&(0x60)) != 0 {
		t.Fatalf("SCF flags unexpected F=%02X", c.F)
	}
	c.Step() // CCF -> toggle C, Z preserved, N/H cleared
	if (c.F&0x10) != 0 || (c.F&0x80) == 0 || (c.F&(0x60)) != 0 {
		t.Fatalf("CCF flags unexpected F=%02X", c.F)
	}
	prevC := c.F & 0x10
	prevZ := c.F & 0x80
	c.Step() // CPL
	if c.A != 0xFF {
		t.Fatalf("CPL A got %02X want FF", c.A)
	}
	// N and H set, Z and C unchanged
	if (c.F&0x60) != 0x60 || (c.F&0x10) != prevC || (c.F&0x80) != prevZ {
		t.Fatalf("CPL flags unexpected F=%02X", c.F)
	}
}

func TestCPU_RETI_EnablesIME_AndCycles(t *testing.T) {
	// Arrange an interrupt that jumps to 0x0040, where handler executes RETI.
	rom := make([]byte, 0x8000)
	// At vector 0x0040: RETI
	rom[0x0040] = 0xD9
	b := bus.New(rom)
	c := New(b)
	c.SetPC(0x0100)
	// Request VBlank and enable IME
	c.IME = true
	b.Write(0xFFFF, 0x01) // IE VBlank
	b.Write(0xFF0F, 0x01) // IF VBlank
	// Step: should service interrupt (20 cycles) and PC=0x0040
	cyc := c.Step()
	if cyc != 20 || c.PC != 0x0040 {
		t.Fatalf("Interrupt service failed: cyc=%d PC=%04X", cyc, c.PC)
	}
	if c.IME {
		t.Fatalf("IME should be cleared during ISR, got IME=true")
	}
	// Execute RETI: enable IME immediately and return to 0x0100
	// Simulate the stack as if the interrupt push occurred
	// Note: push16 happens in service; pop16 in RETI.
	cyc = c.Step()
	if cyc != 16 {
		t.Fatalf("RETI cycles got %d want 16", cyc)
	}
	if !c.IME {
		t.Fatalf("RETI should enable IME immediately")
	}
}

func TestCPU_LD_r_from_HL_CyclesAndBehavior(t *testing.T) {
	// Build program with resets of HL between each LD r,(HL):
	// [0]: LD HL,C000; LD B,(HL)
	// [4]: LD HL,C000; LD C,(HL)
	// [8]: LD HL,C000; LD D,(HL)
	// [12]: LD HL,C000; LD E,(HL)
	// [16]: LD HL,C000; LD H,(HL)
	// [20]: LD HL,C000; LD L,(HL)
	// [24]: LD HL,C000; LD A,(HL)
	rom := make([]byte, 0x8000)
	i := 0
	emit := func(bts ...byte) { copy(rom[i:], bts); i += len(bts) }
	// Emit pairs
	emit(0x21, 0x00, 0xC0, 0x46) // LD HL,C000; LD B,(HL)
	emit(0x21, 0x00, 0xC0, 0x4E) // LD C,(HL)
	emit(0x21, 0x00, 0xC0, 0x56) // LD D,(HL)
	emit(0x21, 0x00, 0xC0, 0x5E) // LD E,(HL)
	emit(0x21, 0x00, 0xC0, 0x66) // LD H,(HL)
	emit(0x21, 0x00, 0xC0, 0x6E) // LD L,(HL)
	emit(0x21, 0x00, 0xC0, 0x7E) // LD A,(HL)

	b := bus.New(rom)
	c := New(b)
	// Preload C000 with a value
	b.Write(0xC000, 0x5A)

	// LD HL; LD B,(HL)
	if cyc := c.Step(); cyc != 12 || c.getHL() != 0xC000 { t.Fatalf("LD HL,d16 failed: cyc=%d HL=%04X", cyc, c.getHL()) }
	if cyc := c.Step(); cyc != 8 || c.B != 0x5A { t.Fatalf("LD B,(HL) cyc=%d B=%02X", cyc, c.B) }

	// LD HL; LD C,(HL)
	if cyc := c.Step(); cyc != 12 || c.getHL() != 0xC000 { t.Fatalf("LD HL,d16 failed: cyc=%d HL=%04X", cyc, c.getHL()) }
	if cyc := c.Step(); cyc != 8 || c.C != 0x5A { t.Fatalf("LD C,(HL) cyc=%d C=%02X", cyc, c.C) }

	// LD HL; LD D,(HL)
	if cyc := c.Step(); cyc != 12 || c.getHL() != 0xC000 { t.Fatalf("LD HL,d16 failed: cyc=%d HL=%04X", cyc, c.getHL()) }
	if cyc := c.Step(); cyc != 8 || c.D != 0x5A { t.Fatalf("LD D,(HL) cyc=%d D=%02X", cyc, c.D) }

	// LD HL; LD E,(HL)
	if cyc := c.Step(); cyc != 12 || c.getHL() != 0xC000 { t.Fatalf("LD HL,d16 failed: cyc=%d HL=%04X", cyc, c.getHL()) }
	if cyc := c.Step(); cyc != 8 || c.E != 0x5A { t.Fatalf("LD E,(HL) cyc=%d E=%02X", cyc, c.E) }

	// LD HL; LD H,(HL)
	if cyc := c.Step(); cyc != 12 || c.getHL() != 0xC000 { t.Fatalf("LD HL,d16 failed: cyc=%d HL=%04X", cyc, c.getHL()) }
	if cyc := c.Step(); cyc != 8 || c.H != 0x5A { t.Fatalf("LD H,(HL) cyc=%d H=%02X", cyc, c.H) }

	// LD HL; LD L,(HL)
	if cyc := c.Step(); cyc != 12 || c.getHL() != 0xC000 { t.Fatalf("LD HL,d16 failed: cyc=%d HL=%04X", cyc, c.getHL()) }
	if cyc := c.Step(); cyc != 8 || c.L != 0x5A { t.Fatalf("LD L,(HL) cyc=%d L=%02X", cyc, c.L) }

	// LD HL; LD A,(HL)
	if cyc := c.Step(); cyc != 12 || c.getHL() != 0xC000 { t.Fatalf("LD HL,d16 failed: cyc=%d HL=%04X", cyc, c.getHL()) }
	if cyc := c.Step(); cyc != 8 || c.A != 0x5A { t.Fatalf("LD A,(HL) cyc=%d A=%02X", cyc, c.A) }
}
