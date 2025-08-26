package cart

// MBC3 implements ROM/RAM banking (RTC not implemented here).
// Banking behavior:
// - 0000-1FFF: RAM enable (0x0A in low nibble)
// - 2000-3FFF: ROM bank low 7 bits (0 maps to 1)
// - 4000-5FFF: RAM bank (0-3) or RTC reg select (08-0C) — we ignore RTC and treat >3 as 0
// - 6000-7FFF: Latch clock (ignored without RTC)
// - A000-BFFF: External RAM access when enabled and RAM present
// ROM: bank 0 fixed at 0000-3FFF; switchable 4000-7FFF uses bank (1..127)

type MBC3 struct {
	rom []byte
	ram []byte

	ramEnabled bool
	romBank    byte // 7 bits (1..127)
	ramBank    byte // 0..3 (others ignored to 0)
}

func NewMBC3(rom []byte, ramSize int) *MBC3 {
	m := &MBC3{rom: rom}
	if ramSize > 0 {
		m.ram = make([]byte, ramSize)
	}
	m.romBank = 1
	return m
}

func (m *MBC3) Read(addr uint16) byte {
	switch {
	case addr < 0x4000:
		if int(addr) < len(m.rom) {
			return m.rom[addr]
		}
		return 0xFF
	case addr < 0x8000:
		bank := int(m.romBank & 0x7F)
		if bank == 0 {
			bank = 1
		}
		off := bank*0x4000 + int(addr-0x4000)
		if off >= 0 && off < len(m.rom) {
			return m.rom[off]
		}
		return 0xFF
	case addr >= 0xA000 && addr <= 0xBFFF:
		if !m.ramEnabled || len(m.ram) == 0 {
			return 0xFF
		}
		rb := int(m.ramBank & 0x03)
		off := rb*0x2000 + int(addr-0xA000)
		if off >= 0 && off < len(m.ram) {
			return m.ram[off]
		}
		return 0xFF
	default:
		return 0xFF
	}
}

func (m *MBC3) Write(addr uint16, value byte) {
	switch {
	case addr < 0x2000:
		m.ramEnabled = (value & 0x0F) == 0x0A
	case addr < 0x4000:
		v := value & 0x7F
		if v == 0 {
			v = 1
		}
		m.romBank = v
	case addr < 0x6000:
		// If value in 0..3 select RAM bank; RTC regs (0x08..0x0C) ignored -> treat as RAM bank 0
		if value <= 0x03 {
			m.ramBank = value & 0x03
		} else {
			m.ramBank = 0
		}
	case addr < 0x8000:
		// Latch clock: ignored without RTC
		_ = value
	case addr >= 0xA000 && addr <= 0xBFFF:
		if !m.ramEnabled || len(m.ram) == 0 {
			return
		}
		rb := int(m.ramBank & 0x03)
		off := rb*0x2000 + int(addr-0xA000)
		if off >= 0 && off < len(m.ram) {
			m.ram[off] = value
		}
	}
}

// BatteryBacked implementation (RTC not persisted here)
func (m *MBC3) SaveRAM() []byte {
	if len(m.ram) == 0 {
		return nil
	}
	out := make([]byte, len(m.ram))
	copy(out, m.ram)
	return out
}

func (m *MBC3) LoadRAM(data []byte) {
	if len(m.ram) == 0 || len(data) == 0 {
		return
	}
	copy(m.ram, data)
}
