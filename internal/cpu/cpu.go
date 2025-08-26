package cpu

import (
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/bus"
)

// CPU implements a minimal SM83 core scaffold with a handful of opcodes for testing.
type CPU struct {
	// 8-bit registers
	A, F byte
	B, C byte
	D, E byte
	H, L byte

	SP uint16
	PC uint16

	IME    bool
	halted bool
	// EI enables IME after the following instruction
	eiPending bool

	bus *bus.Bus
}

// New creates a CPU with default post-boot-like state (simplified).
func New(b *bus.Bus) *CPU {
	return &CPU{bus: b, SP: 0xFFFE, PC: 0x0000}
}

// SetPC allows tests or a boot stub to set the program counter.
func (c *CPU) SetPC(pc uint16) { c.PC = pc }

// Bus exposes the underlying bus for tests/tools.
func (c *CPU) Bus() *bus.Bus { return c.bus }

// ResetNoBoot sets registers to typical DMG post-boot state.
// Useful when running without a boot ROM.
func (c *CPU) ResetNoBoot() {
	c.A, c.F = 0x01, 0xB0
	c.B, c.C = 0x00, 0x13
	c.D, c.E = 0x00, 0xD8
	c.H, c.L = 0x01, 0x4D
	c.SP = 0xFFFE
	c.IME = false
	c.halted = false
	c.eiPending = false
}

// Flags helpers
const (
	flagZ byte = 1 << 7
	flagN byte = 1 << 6
	flagH byte = 1 << 5
	flagC byte = 1 << 4
)

func (c *CPU) setZNHC(z, n, h, carry bool) {
	var f byte
	if z {
		f |= flagZ
	}
	if n {
		f |= flagN
	}
	if h {
		f |= flagH
	}
	if carry {
		f |= flagC
	}
	c.F = f
}

func (c *CPU) add8(a, b byte) (res byte, z, n, h, cy bool) {
	r := uint16(a) + uint16(b)
	res = byte(r)
	z = res == 0
	n = false
	h = ((a & 0x0F) + (b & 0x0F)) > 0x0F
	cy = r > 0xFF
	return
}

func (c *CPU) adc8(a, b byte, carryIn bool) (res byte, z, n, h, cy bool) {
	ci := byte(0)
	if carryIn {
		ci = 1
	}
	r := uint16(a) + uint16(b) + uint16(ci)
	res = byte(r)
	z = res == 0
	n = false
	h = ((a & 0x0F) + (b & 0x0F) + ci) > 0x0F
	cy = r > 0xFF
	return
}

func (c *CPU) sub8(a, b byte) (res byte, z, n, h, cy bool) {
	r := int16(a) - int16(b)
	res = byte(r)
	z = res == 0
	n = true
	h = (a & 0x0F) < (b & 0x0F)
	cy = int16(a) < int16(b)
	return
}

func (c *CPU) sbc8(a, b byte, carryIn bool) (res byte, z, n, h, cy bool) {
	ci := byte(0)
	if carryIn {
		ci = 1
	}
	r := int16(a) - int16(b) - int16(ci)
	res = byte(r)
	z = res == 0
	n = true
	h = (a & 0x0F) < ((b & 0x0F) + ci)
	cy = int16(a) < int16(b)+int16(ci)
	return
}

func (c *CPU) and8(a, b byte) (res byte, z, n, h, cy bool) {
	res = a & b
	z = res == 0
	n = false
	h = true
	cy = false
	return
}

func (c *CPU) xor8(a, b byte) (res byte, z, n, h, cy bool) {
	res = a ^ b
	z = res == 0
	n = false
	h = false
	cy = false
	return
}

func (c *CPU) or8(a, b byte) (res byte, z, n, h, cy bool) {
	res = a | b
	z = res == 0
	n = false
	h = false
	cy = false
	return
}

func (c *CPU) cp8(a, b byte) (z, n, h, cy bool) {
	_, z, n, h, cy = c.sub8(a, b)
	return
}

func (c *CPU) read8(addr uint16) byte     { return c.bus.Read(addr) }
func (c *CPU) write8(addr uint16, v byte) { c.bus.Write(addr, v) }

func (c *CPU) fetch8() byte {
	b := c.read8(c.PC)
	c.PC++
	return b
}

func (c *CPU) fetch16() uint16 {
	lo := uint16(c.fetch8())
	hi := uint16(c.fetch8())
	return lo | (hi << 8)
}

func (c *CPU) read16(addr uint16) uint16 {
	lo := uint16(c.read8(addr))
	hi := uint16(c.read8(addr + 1))
	return lo | (hi << 8)
}

func (c *CPU) write16(addr uint16, v uint16) {
	c.write8(addr, byte(v&0x00FF))
	c.write8(addr+1, byte(v>>8))
}

func (c *CPU) getAF() uint16  { return uint16(c.A)<<8 | uint16(c.F&0xF0) }
func (c *CPU) setAF(v uint16) { c.A = byte(v >> 8); c.F = byte(v) & 0xF0 }
func (c *CPU) getBC() uint16  { return uint16(c.B)<<8 | uint16(c.C) }
func (c *CPU) setBC(v uint16) { c.B = byte(v >> 8); c.C = byte(v) }
func (c *CPU) getDE() uint16  { return uint16(c.D)<<8 | uint16(c.E) }
func (c *CPU) setDE(v uint16) { c.D = byte(v >> 8); c.E = byte(v) }
func (c *CPU) getHL() uint16  { return uint16(c.H)<<8 | uint16(c.L) }
func (c *CPU) setHL(v uint16) { c.H = byte(v >> 8); c.L = byte(v) }

func (c *CPU) push16(v uint16) {
	c.SP -= 2
	c.write16(c.SP, v)
}

func (c *CPU) pop16() uint16 {
	v := c.read16(c.SP)
	c.SP += 2
	return v
}

// Step executes one instruction and returns an approximate cycle count for the implemented subset.
func (c *CPU) Step() (cycles int) {
	// Advance timers on return with the cycles consumed in this step
	defer func() {
		if c.bus != nil && cycles > 0 {
			c.bus.Tick(cycles)
		}
		// Apply EI delayed enable after completing this instruction
		if c.eiPending {
			c.IME = true
			c.eiPending = false
		}
	}()

	// Interrupt servicing helper
	serviceInterrupt := func() int {
		ie := c.bus.Read(0xFFFF)
		ifReg := c.bus.Read(0xFF0F) & 0x1F
		pending := ie & ifReg
		if pending == 0 {
			return 0
		}
		// priority order VBlank(0), LCD STAT(1), Timer(2), Serial(3), Joypad(4)
		var bit uint
		for bit = 0; bit < 5; bit++ {
			if (pending & (1 << bit)) != 0 {
				break
			}
		}
		// acknowledge: clear IF bit
		c.bus.Write(0xFF0F, (ifReg&^(1<<bit))&0x1F)
		// push PC and jump
		c.halted = false
		c.IME = false
		c.push16(c.PC)
		c.PC = 0x40 + uint16(bit)*8
		return 20
	}

	// HALT behavior: if IME and an interrupt is pending, service it; else sleep
	if c.halted {
		if c.IME {
			if cyc := serviceInterrupt(); cyc != 0 {
				return cyc
			}
		} else {
			// wake on pending interrupt without servicing (HALT bug simplified)
			ifReg := c.bus.Read(0xFF0F) & 0x1F
			ie := c.bus.Read(0xFFFF)
			if (ifReg & ie) != 0 {
				c.halted = false
			} else {
				return 4
			}
		}
	}

	// If IME and an interrupt is pending, service before executing opcode
	if c.IME {
		if cyc := serviceInterrupt(); cyc != 0 {
			return cyc
		}
	}

	op := c.fetch8()
	switch op {
	case 0x00: // NOP
		return 4

	// LD r, d8
	case 0x06:
		c.B = c.fetch8()
		return 8
	case 0x0E:
		c.C = c.fetch8()
		return 8
	case 0x16:
		c.D = c.fetch8()
		return 8
	case 0x1E:
		c.E = c.fetch8()
		return 8
	case 0x26:
		c.H = c.fetch8()
		return 8
	case 0x2E:
		c.L = c.fetch8()
		return 8
	case 0x3E:
		c.A = c.fetch8()
		return 8

	// LD r,r' and LD (HL),r / LD r,(HL)
	case 0x40, 0x41, 0x42, 0x43, 0x44, 0x45, 0x47,
		0x48, 0x49, 0x4A, 0x4B, 0x4C, 0x4D, 0x4F,
		0x50, 0x51, 0x52, 0x53, 0x54, 0x55, 0x57,
		0x58, 0x59, 0x5A, 0x5B, 0x5C, 0x5D, 0x5F,
		0x60, 0x61, 0x62, 0x63, 0x64, 0x65, 0x67,
		0x68, 0x69, 0x6A, 0x6B, 0x6C, 0x6D, 0x6F,
		0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x77,
		0x78, 0x79, 0x7A, 0x7B, 0x7C, 0x7D, 0x7F:
		if op == 0x76 { // HALT handled elsewhere
			c.halted = true
			return 4
		}
		d := (op >> 3) & 7
		s := op & 7
		// Map reg index to value pointer; 6 means (HL)
		get := func(idx byte) byte {
			switch idx {
			case 0:
				return c.B
			case 1:
				return c.C
			case 2:
				return c.D
			case 3:
				return c.E
			case 4:
				return c.H
			case 5:
				return c.L
			case 6:
				return c.read8(c.getHL())
			case 7:
				return c.A
			}
			return 0
		}
		set := func(idx byte, val byte) {
			switch idx {
			case 0:
				c.B = val
			case 1:
				c.C = val
			case 2:
				c.D = val
			case 3:
				c.E = val
			case 4:
				c.H = val
			case 5:
				c.L = val
			case 6:
				c.write8(c.getHL(), val)
			case 7:
				c.A = val
			}
		}
		val := get(byte(s))
		set(byte(d), val)
		if d == 6 || s == 6 {
			return 8
		}
		return 4

	// 16-bit loads
	case 0x01: // LD BC,d16
		c.setBC(c.fetch16())
		return 12
	case 0x11: // LD DE,d16
		c.setDE(c.fetch16())
		return 12
	case 0x21: // LD HL,d16
		c.setHL(c.fetch16())
		return 12
	case 0x31: // LD SP,d16
		c.SP = c.fetch16()
		return 12
	case 0x08: // LD (a16),SP
		addr := c.fetch16()
		c.write16(addr, c.SP)
		return 20

	// LD (HL), d8
	case 0x36:
		v := c.fetch8()
		c.write8(c.getHL(), v)
		return 12

	// LD (BC),A / (DE),A and A,(BC)/(DE)
	case 0x02:
		c.write8(c.getBC(), c.A)
		return 8
	case 0x12:
		c.write8(c.getDE(), c.A)
		return 8
	case 0x0A:
		c.A = c.read8(c.getBC())
		return 8
	case 0x1A:
		c.A = c.read8(c.getDE())
		return 8

	// LDI/LDD via HL
	case 0x22: // LD (HL+),A
		hl := c.getHL()
		c.write8(hl, c.A)
		c.setHL(hl + 1)
		return 8
	case 0x2A: // LD A,(HL+)
		hl := c.getHL()
		c.A = c.read8(hl)
		c.setHL(hl + 1)
		return 8
	case 0x32: // LD (HL-),A
		hl := c.getHL()
		c.write8(hl, c.A)
		c.setHL(hl - 1)
		return 8
	case 0x3A: // LD A,(HL-)
		hl := c.getHL()
		c.A = c.read8(hl)
		c.setHL(hl - 1)
		return 8

	// LDH (FF00+n),A and A,(FF00+n)
	case 0xE0:
		n := uint16(c.fetch8())
		c.write8(0xFF00+n, c.A)
		return 12
	case 0xF0:
		n := uint16(c.fetch8())
		c.A = c.read8(0xFF00 + n)
		return 12
	// LD (FF00+C),A and A,(FF00+C)
	// Rotates and flag ops
	case 0x07: // RLCA
		cval := (c.A >> 7) & 1
		c.A = (c.A << 1) | byte(cval)
		c.setZNHC(false, false, false, cval == 1)
		return 4
	case 0x0F: // RRCA
		cval := c.A & 1
		c.A = (c.A >> 1) | (cval << 7)
		c.setZNHC(false, false, false, cval == 1)
		return 4
	case 0x17: // RLA
		cval := (c.A >> 7) & 1
		carry := byte(0)
		if (c.F & flagC) != 0 {
			carry = 1
		}
		c.A = (c.A << 1) | carry
		c.setZNHC(false, false, false, cval == 1)
		return 4
	case 0x1F: // RRA
		cval := c.A & 1
		carry := byte(0)
		if (c.F & flagC) != 0 {
			carry = 1
		}
		c.A = (c.A >> 1) | (carry << 7)
		c.setZNHC(false, false, false, cval == 1)
		return 4
	case 0x27: // DAA
		a := c.A
		cf := (c.F & flagC) != 0
		if (c.F & flagN) == 0 { // after addition
			if cf || a > 0x99 {
				a += 0x60
				cf = true
			}
			if (c.F&flagH) != 0 || (a&0x0F) > 9 {
				a += 0x06
			}
		} else { // after subtraction
			if cf {
				a -= 0x60
			}
			if (c.F & flagH) != 0 {
				a -= 0x06
			}
		}
		c.A = a
		c.setZNHC(c.A == 0, (c.F&flagN) != 0, false, cf)
		return 4
	case 0x2F: // CPL
		c.A = ^c.A
		// N and H set, C unchanged, Z unchanged
		c.F = (c.F & (flagZ | flagC)) | flagN | flagH
		return 4
	case 0x37: // SCF
		c.F = (c.F & flagZ) | flagC
		return 4
	case 0x3F: // CCF
		if (c.F & flagC) != 0 {
			c.F = c.F &^ flagC
		} else {
			c.F |= flagC
		}
		c.F &^= (flagN | flagH)
		c.F &= (flagZ | flagC)
		return 4

	case 0xE2:
		c.write8(0xFF00+uint16(c.C), c.A)
		return 8
	case 0xF2:
		c.A = c.read8(0xFF00 + uint16(c.C))
		return 8

	case 0x04: // INC B
		old := c.B
		c.B++
		z := c.B == 0
		h := (old & 0x0F) == 0x0F
		c.setZNHC(z, false, h, (c.F&flagC) != 0)
		return 4

	// INC r / DEC r for all regs and (HL)
	case 0x0C: // INC C
		old := c.C
		c.C++
		c.setZNHC(c.C == 0, false, (old&0x0F) == 0x0F, (c.F&flagC) != 0)
		return 4
	case 0x14:
		old := c.D
		c.D++
		c.setZNHC(c.D == 0, false, (old&0x0F) == 0x0F, (c.F&flagC) != 0)
		return 4
	case 0x1C:
		old := c.E
		c.E++
		c.setZNHC(c.E == 0, false, (old&0x0F) == 0x0F, (c.F&flagC) != 0)
		return 4
	case 0x24:
		old := c.H
		c.H++
		c.setZNHC(c.H == 0, false, (old&0x0F) == 0x0F, (c.F&flagC) != 0)
		return 4
	case 0x2C:
		old := c.L
		c.L++
		c.setZNHC(c.L == 0, false, (old&0x0F) == 0x0F, (c.F&flagC) != 0)
		return 4
	case 0x3C:
		old := c.A
		c.A++
		c.setZNHC(c.A == 0, false, (old&0x0F) == 0x0F, (c.F&flagC) != 0)
		return 4
	case 0x34: // INC (HL)
		addr := c.getHL()
		v := c.read8(addr)
		old := v
		v++
		c.write8(addr, v)
		z := v == 0
		h := (old & 0x0F) == 0x0F
		c.setZNHC(z, false, h, (c.F&flagC) != 0)
		return 12

	case 0x05: // DEC B
		old := c.B
		c.B--
		c.setZNHC(c.B == 0, true, (old&0x0F) == 0x00, (c.F&flagC) != 0)
		return 4
	case 0x0D:
		old := c.C
		c.C--
		c.setZNHC(c.C == 0, true, (old&0x0F) == 0x00, (c.F&flagC) != 0)
		return 4
	case 0x15:
		old := c.D
		c.D--
		c.setZNHC(c.D == 0, true, (old&0x0F) == 0x00, (c.F&flagC) != 0)
		return 4
	case 0x1D:
		old := c.E
		c.E--
		c.setZNHC(c.E == 0, true, (old&0x0F) == 0x00, (c.F&flagC) != 0)
		return 4
	case 0x25:
		old := c.H
		c.H--
		c.setZNHC(c.H == 0, true, (old&0x0F) == 0x00, (c.F&flagC) != 0)
		return 4
	case 0x2D:
		old := c.L
		c.L--
		c.setZNHC(c.L == 0, true, (old&0x0F) == 0x00, (c.F&flagC) != 0)
		return 4
	case 0x3D:
		old := c.A
		c.A--
		c.setZNHC(c.A == 0, true, (old&0x0F) == 0x00, (c.F&flagC) != 0)
		return 4
	case 0x35: // DEC (HL)
		addr := c.getHL()
		v := c.read8(addr)
		old := v
		v--
		c.write8(addr, v)
		z := v == 0
		h := (old & 0x0F) == 0x00
		c.setZNHC(z, true, h, (c.F&flagC) != 0)
		return 12

	// 0xAF handled in XOR group below

	// ADD/ADC/SUB/SBC/AND/XOR/OR/CP with registers
	case 0x80, 0x81, 0x82, 0x83, 0x84, 0x85, 0x87:
		var src byte
		switch op & 7 {
		case 0:
			src = c.B
		case 1:
			src = c.C
		case 2:
			src = c.D
		case 3:
			src = c.E
		case 4:
			src = c.H
		case 5:
			src = c.L
		case 7:
			src = c.A
		}
		r, z, n, h, cy := c.add8(c.A, src)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 4
	case 0x88, 0x89, 0x8A, 0x8B, 0x8C, 0x8D, 0x8F:
		var src byte
		switch op & 7 {
		case 0:
			src = c.B
		case 1:
			src = c.C
		case 2:
			src = c.D
		case 3:
			src = c.E
		case 4:
			src = c.H
		case 5:
			src = c.L
		case 7:
			src = c.A
		}
		r, z, n, h, cy := c.adc8(c.A, src, (c.F&flagC) != 0)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 4
	case 0x90, 0x91, 0x92, 0x93, 0x94, 0x95, 0x97:
		var src byte
		switch op & 7 {
		case 0:
			src = c.B
		case 1:
			src = c.C
		case 2:
			src = c.D
		case 3:
			src = c.E
		case 4:
			src = c.H
		case 5:
			src = c.L
		case 7:
			src = c.A
		}
		r, z, n, h, cy := c.sub8(c.A, src)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 4
	case 0x98, 0x99, 0x9A, 0x9B, 0x9C, 0x9D, 0x9F:
		var src byte
		switch op & 7 {
		case 0:
			src = c.B
		case 1:
			src = c.C
		case 2:
			src = c.D
		case 3:
			src = c.E
		case 4:
			src = c.H
		case 5:
			src = c.L
		case 7:
			src = c.A
		}
		r, z, n, h, cy := c.sbc8(c.A, src, (c.F&flagC) != 0)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 4
	case 0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA7:
		var src byte
		switch op & 7 {
		case 0:
			src = c.B
		case 1:
			src = c.C
		case 2:
			src = c.D
		case 3:
			src = c.E
		case 4:
			src = c.H
		case 5:
			src = c.L
		case 7:
			src = c.A
		}
		r, z, n, h, cy := c.and8(c.A, src)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 4
	case 0xA8, 0xA9, 0xAA, 0xAB, 0xAC, 0xAD, 0xAF:
		var src byte
		switch op & 7 {
		case 0:
			src = c.B
		case 1:
			src = c.C
		case 2:
			src = c.D
		case 3:
			src = c.E
		case 4:
			src = c.H
		case 5:
			src = c.L
		case 7:
			src = c.A
		}
		r, z, n, h, cy := c.xor8(c.A, src)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 4
	case 0xB0, 0xB1, 0xB2, 0xB3, 0xB4, 0xB5, 0xB7:
		var src byte
		switch op & 7 {
		case 0:
			src = c.B
		case 1:
			src = c.C
		case 2:
			src = c.D
		case 3:
			src = c.E
		case 4:
			src = c.H
		case 5:
			src = c.L
		case 7:
			src = c.A
		}
		r, z, n, h, cy := c.or8(c.A, src)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 4
	case 0xB8, 0xB9, 0xBA, 0xBB, 0xBC, 0xBD, 0xBF:
		var src byte
		switch op & 7 {
		case 0:
			src = c.B
		case 1:
			src = c.C
		case 2:
			src = c.D
		case 3:
			src = c.E
		case 4:
			src = c.H
		case 5:
			src = c.L
		case 7:
			src = c.A
		}
		z, n, h, cy := c.cp8(c.A, src)
		c.setZNHC(z, n, h, cy)
		return 4

	// ALU with (HL)
	case 0x86:
		r, z, n, h, cy := c.add8(c.A, c.read8(c.getHL()))
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0x8E:
		r, z, n, h, cy := c.adc8(c.A, c.read8(c.getHL()), (c.F&flagC) != 0)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0x96:
		r, z, n, h, cy := c.sub8(c.A, c.read8(c.getHL()))
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0x9E:
		r, z, n, h, cy := c.sbc8(c.A, c.read8(c.getHL()), (c.F&flagC) != 0)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xA6:
		r, z, n, h, cy := c.and8(c.A, c.read8(c.getHL()))
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xAE:
		r, z, n, h, cy := c.xor8(c.A, c.read8(c.getHL()))
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xB6:
		r, z, n, h, cy := c.or8(c.A, c.read8(c.getHL()))
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xBE:
		z, n, h, cy := c.cp8(c.A, c.read8(c.getHL()))
		c.setZNHC(z, n, h, cy)
		return 8

	// ALU immediate
	case 0xC6:
		r, z, n, h, cy := c.add8(c.A, c.fetch8())
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xCE:
		r, z, n, h, cy := c.adc8(c.A, c.fetch8(), (c.F&flagC) != 0)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xD6:
		r, z, n, h, cy := c.sub8(c.A, c.fetch8())
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xDE:
		r, z, n, h, cy := c.sbc8(c.A, c.fetch8(), (c.F&flagC) != 0)
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xE6:
		r, z, n, h, cy := c.and8(c.A, c.fetch8())
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xEE:
		r, z, n, h, cy := c.xor8(c.A, c.fetch8())
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xF6:
		r, z, n, h, cy := c.or8(c.A, c.fetch8())
		c.A = r
		c.setZNHC(z, n, h, cy)
		return 8
	case 0xFE:
		z, n, h, cy := c.cp8(c.A, c.fetch8())
		c.setZNHC(z, n, h, cy)
		return 8

	case 0xEA: // LD (a16),A
		addr := c.fetch16()
		c.write8(addr, c.A)
		return 16
	case 0xFA: // LD A,(a16)
		addr := c.fetch16()
		c.A = c.read8(addr)
		return 16

	case 0xC3: // JP a16
		addr := c.fetch16()
		c.PC = addr
		return 16
	case 0xE9: // JP (HL)
		c.PC = c.getHL()
		return 4
	case 0x18: // JR r8
		off := int8(c.fetch8())
		c.PC = uint16(int32(c.PC) + int32(off))
		return 12

	// JR cc,r8
	case 0x20: // JR NZ
		off := int8(c.fetch8())
		if (c.F & flagZ) == 0 {
			c.PC = uint16(int32(c.PC) + int32(off))
			return 12
		}
		return 8
	case 0x28: // JR Z
		off := int8(c.fetch8())
		if (c.F & flagZ) != 0 {
			c.PC = uint16(int32(c.PC) + int32(off))
			return 12
		}
		return 8
	case 0x30: // JR NC
		off := int8(c.fetch8())
		if (c.F & flagC) == 0 {
			c.PC = uint16(int32(c.PC) + int32(off))
			return 12
		}
		return 8
	case 0x38: // JR C
		off := int8(c.fetch8())
		if (c.F & flagC) != 0 {
			c.PC = uint16(int32(c.PC) + int32(off))
			return 12
		}
		return 8

	// CALL/RET
	case 0xCD: // CALL a16
		addr := c.fetch16()
		c.push16(c.PC)
		c.PC = addr
		return 24
	case 0xC9: // RET
		c.PC = c.pop16()
		return 16
	case 0xD9: // RETI
		c.PC = c.pop16()
		c.IME = true
		return 16

	// RST t
	case 0xC7:
		c.push16(c.PC)
		c.PC = 0x00
		return 16
	case 0xCF:
		c.push16(c.PC)
		c.PC = 0x08
		return 16
	case 0xD7:
		c.push16(c.PC)
		c.PC = 0x10
		return 16
	case 0xDF:
		c.push16(c.PC)
		c.PC = 0x18
		return 16
	case 0xE7:
		c.push16(c.PC)
		c.PC = 0x20
		return 16
	case 0xEF:
		c.push16(c.PC)
		c.PC = 0x28
		return 16
	case 0xF7:
		c.push16(c.PC)
		c.PC = 0x30
		return 16
	case 0xFF:
		c.push16(c.PC)
		c.PC = 0x38
		return 16

	// CALL cc
	case 0xC4: // NZ
		addr := c.fetch16()
		if (c.F & flagZ) == 0 {
			c.push16(c.PC)
			c.PC = addr
			return 24
		}
		return 12
	case 0xCC: // Z
		addr := c.fetch16()
		if (c.F & flagZ) != 0 {
			c.push16(c.PC)
			c.PC = addr
			return 24
		}
		return 12
	case 0xD4: // NC
		addr := c.fetch16()
		if (c.F & flagC) == 0 {
			c.push16(c.PC)
			c.PC = addr
			return 24
		}
		return 12
	case 0xDC: // C
		addr := c.fetch16()
		if (c.F & flagC) != 0 {
			c.push16(c.PC)
			c.PC = addr
			return 24
		}
		return 12

	// RET cc
	case 0xC0:
		if (c.F & flagZ) == 0 {
			c.PC = c.pop16()
			return 20
		}
		return 8
	case 0xC8:
		if (c.F & flagZ) != 0 {
			c.PC = c.pop16()
			return 20
		}
		return 8
	case 0xD0:
		if (c.F & flagC) == 0 {
			c.PC = c.pop16()
			return 20
		}
		return 8
	case 0xD8:
		if (c.F & flagC) != 0 {
			c.PC = c.pop16()
			return 20
		}
		return 8

	// JP cc,a16
	case 0xC2:
		addr := c.fetch16()
		if (c.F & flagZ) == 0 {
			c.PC = addr
			return 16
		}
		return 12
	case 0xCA:
		addr := c.fetch16()
		if (c.F & flagZ) != 0 {
			c.PC = addr
			return 16
		}
		return 12
	case 0xD2:
		addr := c.fetch16()
		if (c.F & flagC) == 0 {
			c.PC = addr
			return 16
		}
		return 12
	case 0xDA:
		addr := c.fetch16()
		if (c.F & flagC) != 0 {
			c.PC = addr
			return 16
		}
		return 12

	// 16-bit INC/DEC and ADD HL,rr
	case 0x03:
		c.setBC(c.getBC() + 1)
		return 8
	case 0x13:
		c.setDE(c.getDE() + 1)
		return 8
	case 0x23:
		c.setHL(c.getHL() + 1)
		return 8
	case 0x33:
		c.SP++
		return 8
	case 0x0B:
		c.setBC(c.getBC() - 1)
		return 8
	case 0x1B:
		c.setDE(c.getDE() - 1)
		return 8
	case 0x2B:
		c.setHL(c.getHL() - 1)
		return 8
	case 0x3B:
		c.SP--
		return 8
	case 0x09: // ADD HL,BC
		hl := c.getHL()
		bc := c.getBC()
		r := uint32(hl) + uint32(bc)
		h := ((hl & 0x0FFF) + (bc & 0x0FFF)) > 0x0FFF
		c.setHL(uint16(r))
		c.setZNHC((c.F&flagZ) != 0, false, h, r > 0xFFFF)
		return 8
	case 0x19:
		hl := c.getHL()
		de := c.getDE()
		r := uint32(hl) + uint32(de)
		h := ((hl & 0x0FFF) + (de & 0x0FFF)) > 0x0FFF
		c.setHL(uint16(r))
		c.setZNHC((c.F&flagZ) != 0, false, h, r > 0xFFFF)
		return 8
	case 0x29:
		hl := c.getHL()
		hl2 := hl
		r := uint32(hl) + uint32(hl2)
		h := ((hl & 0x0FFF) + (hl2 & 0x0FFF)) > 0x0FFF
		c.setHL(uint16(r))
		c.setZNHC((c.F&flagZ) != 0, false, h, r > 0xFFFF)
		return 8
	case 0x39:
		hl := c.getHL()
		sp := c.SP
		r := uint32(hl) + uint32(sp)
		h := ((hl & 0x0FFF) + (sp & 0x0FFF)) > 0x0FFF
		c.setHL(uint16(r))
		c.setZNHC((c.F&flagZ) != 0, false, h, r > 0xFFFF)
		return 8

	// Stack/SP ops
	case 0xF8: // LD HL,SP+r8
		off := int8(c.fetch8())
		res := uint16(int32(int16(c.SP)) + int32(off))
		// Flags: Z=0,N=0,H,C set from lower byte carry
		low := byte(c.SP & 0xFF)
		_, _, _, h, cy := c.add8(low, byte(off))
		c.setHL(res)
		c.setZNHC(false, false, h, cy)
		return 12
	case 0xF9: // LD SP,HL
		c.SP = c.getHL()
		return 8
	case 0xE8: // ADD SP,r8
		off := int8(c.fetch8())
		low := byte(c.SP & 0xFF)
		_, _, _, h, cy := c.add8(low, byte(off))
		res := uint16(int32(int16(c.SP)) + int32(off))
		c.SP = res
		c.setZNHC(false, false, h, cy)
		return 16

	// EI/DI
	case 0xF3: // DI
		c.IME = false
		c.eiPending = false
		return 4
	case 0xFB: // EI (enable after following instruction)
		c.eiPending = true
		return 4

	// CB prefix
	case 0xCB:
		cb := c.fetch8()
		reg := cb & 7
		opg := (cb >> 6) & 3
		y := (cb >> 3) & 7
		// helpers
		get := func(idx byte) byte {
			switch idx {
			case 0:
				return c.B
			case 1:
				return c.C
			case 2:
				return c.D
			case 3:
				return c.E
			case 4:
				return c.H
			case 5:
				return c.L
			case 6:
				return c.read8(c.getHL())
			case 7:
				return c.A
			}
			return 0
		}
		set := func(idx byte, v byte) {
			switch idx {
			case 0:
				c.B = v
			case 1:
				c.C = v
			case 2:
				c.D = v
			case 3:
				c.E = v
			case 4:
				c.H = v
			case 5:
				c.L = v
			case 6:
				c.write8(c.getHL(), v)
			case 7:
				c.A = v
			}
		}
		cycles := 8
		if reg == 6 {
			cycles = 16
		}
		switch opg {
		case 0: // rotate/shift/swap
			v := get(reg)
			var cflag byte
			switch y {
			case 0: // RLC
				cflag = (v >> 7) & 1
				v = (v << 1) | cflag
				c.setZNHC(v == 0, false, false, cflag == 1)
			case 1: // RRC
				cflag = v & 1
				v = (v >> 1) | (cflag << 7)
				c.setZNHC(v == 0, false, false, cflag == 1)
			case 2: // RL
				cflag = (v >> 7) & 1
				cin := byte(0)
				if (c.F & flagC) != 0 {
					cin = 1
				}
				v = (v << 1) | cin
				c.setZNHC(v == 0, false, false, cflag == 1)
			case 3: // RR
				cflag = v & 1
				cin := byte(0)
				if (c.F & flagC) != 0 {
					cin = 1
				}
				v = (v >> 1) | (cin << 7)
				c.setZNHC(v == 0, false, false, cflag == 1)
			case 4: // SLA
				cflag = (v >> 7) & 1
				v <<= 1
				c.setZNHC(v == 0, false, false, cflag == 1)
			case 5: // SRA
				cflag = v & 1
				v = (v >> 1) | (v & 0x80)
				c.setZNHC(v == 0, false, false, cflag == 1)
			case 6: // SWAP
				v = (v << 4) | (v >> 4)
				c.setZNHC(v == 0, false, false, false)
			case 7: // SRL
				cflag = v & 1
				v >>= 1
				c.setZNHC(v == 0, false, false, cflag == 1)
			}
			set(reg, v)
		case 1: // BIT y, r
			v := get(reg)
			bit := (v >> y) & 1
			z := bit == 0
			// Z set if bit=0, N=0, H=1, C unchanged
			c.F = (c.F & flagC) | flagH
			if z {
				c.F |= flagZ
			}
		case 2: // RES y, r
			v := get(reg)
			v &^= (1 << y)
			set(reg, v)
		case 3: // SET y, r
			v := get(reg)
			v |= (1 << y)
			set(reg, v)
		}
		return cycles

	// PUSH/POP
	case 0xF5: // PUSH AF
		c.push16(c.getAF())
		return 16
	case 0xC5: // PUSH BC
		c.push16(c.getBC())
		return 16
	case 0xD5: // PUSH DE
		c.push16(c.getDE())
		return 16
	case 0xE5: // PUSH HL
		c.push16(c.getHL())
		return 16
	case 0xF1: // POP AF
		c.setAF(c.pop16())
		return 12
	case 0xC1: // POP BC
		c.setBC(c.pop16())
		return 12
	case 0xD1: // POP DE
		c.setDE(c.pop16())
		return 12
	case 0xE1: // POP HL
		c.setHL(c.pop16())
		return 12

	case 0x76: // HALT
		c.halted = true
		return 4

	default:
		// Unimplemented opcodes: act as NOP for now (to keep tests simple)
		return 4
	}
}

// LD r,r' table and LD via (HL) handling
// Implemented inline in switch via specific cases for now to keep it simple.
