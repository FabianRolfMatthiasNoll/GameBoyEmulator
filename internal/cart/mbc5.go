package cart

import (
	"bytes"
	"encoding/gob"
)

// MBC5 supports up to 8MB ROM and 128KB RAM, simple banking.
type MBC5 struct {
	rom []byte
	ram []byte

	romBank    uint16 // 9 bits (0..511)
	ramBank    byte   // 0..15
	ramEnabled bool
}

func NewMBC5(rom []byte, ramSize int) *MBC5 {
	m := &MBC5{rom: rom}
	if ramSize > 0 {
		m.ram = make([]byte, ramSize)
	}
	m.romBank = 1 // default
	return m
}

func (m *MBC5) Read(addr uint16) byte {
	switch {
	case addr < 0x4000:
		// fixed bank 0
		if int(addr) < len(m.rom) {
			return m.rom[addr]
		}
		return 0xFF
	case addr < 0x8000:
		bank := int(m.romBank)
		off := bank*0x4000 + int(addr-0x4000)
		if off >= 0 && off < len(m.rom) {
			return m.rom[off]
		}
		return 0xFF
	case addr >= 0xA000 && addr <= 0xBFFF:
		if !m.ramEnabled || len(m.ram) == 0 {
			return 0xFF
		}
		rb := int(m.ramBank & 0x0F)
		off := rb*0x2000 + int(addr-0xA000)
		if off >= 0 && off < len(m.ram) {
			return m.ram[off]
		}
		return 0xFF
	default:
		return 0xFF
	}
}

func (m *MBC5) Write(addr uint16, value byte) {
	switch {
	case addr < 0x2000:
		m.ramEnabled = (value & 0x0F) == 0x0A
	case addr < 0x3000:
		// low 8 bits of ROM bank
		m.romBank = (m.romBank & 0x100) | uint16(value)
		if m.romBank == 0 {
			m.romBank = 1
		}
	case addr < 0x4000:
		// high bit of ROM bank (bit8)
		if value&0x01 != 0 {
			m.romBank = (m.romBank & 0x0FF) | 0x100
		} else {
			m.romBank &^= 0x100
		}
		if m.romBank == 0 {
			m.romBank = 1
		}
	case addr < 0x6000:
		// RAM bank number 0..15
		m.ramBank = value & 0x0F
	case addr >= 0xA000 && addr <= 0xBFFF:
		if !m.ramEnabled || len(m.ram) == 0 {
			return
		}
		rb := int(m.ramBank & 0x0F)
		off := rb*0x2000 + int(addr-0xA000)
		if off >= 0 && off < len(m.ram) {
			m.ram[off] = value
		}
	}
}

// BatteryBacked implementation
func (m *MBC5) SaveRAM() []byte {
	if len(m.ram) == 0 {
		return nil
	}
	out := make([]byte, len(m.ram))
	copy(out, m.ram)
	return out
}

func (m *MBC5) LoadRAM(data []byte) {
	if len(m.ram) == 0 || len(data) == 0 {
		return
	}
	copy(m.ram, data)
}

// SaveState/LoadState for save states
type mbc5State struct {
	RAM        []byte
	RomBank    uint16
	RamBank    byte
	RamEnabled bool
}

func (m *MBC5) SaveState() []byte {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	s := mbc5State{RAM: append([]byte(nil), m.ram...), RomBank: m.romBank, RamBank: m.ramBank, RamEnabled: m.ramEnabled}
	_ = enc.Encode(s)
	return buf.Bytes()
}

func (m *MBC5) LoadState(data []byte) {
	var s mbc5State
	dec := gob.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&s); err != nil {
		return
	}
	if len(m.ram) > 0 && len(s.RAM) > 0 {
		copy(m.ram, s.RAM)
	}
	m.romBank, m.ramBank, m.ramEnabled = s.RomBank, s.RamBank, s.RamEnabled
}
