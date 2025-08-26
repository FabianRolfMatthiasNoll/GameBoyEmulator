package cart

// MBC1 implements basic MBC1 ROM/RAM banking.
// Supports ROM banking up to 2MB and RAM up to 32KB. Battery/RTC not handled here.
type MBC1 struct {
	rom []byte
	ram []byte

	romBankLow5       byte // lower 5 bits of ROM bank number (0->1 remapped)
	ramBankOrRomHigh2 byte // either RAM bank (mode1) or ROM bank high bits (mode0)
	ramEnabled        bool
	modeSelect        byte // 0: ROM banking (default), 1: RAM banking
}

func NewMBC1(rom []byte, ramSize int) *MBC1 {
	m := &MBC1{rom: rom}
	if ramSize > 0 {
		m.ram = make([]byte, ramSize)
	}
	// default to bank 1 for switchable area
	m.romBankLow5 = 1
	return m
}

func (m *MBC1) Read(addr uint16) byte {
	switch {
	case addr < 0x4000:
		// Bank 0 or high bits applied in mode1
		if m.modeSelect == 0 {
			// ROM bank 0 fixed
			if int(addr) < len(m.rom) {
				return m.rom[addr]
			}
			return 0xFF
		}
		// mode 1: apply high bits to bank 0 region
		bank := int((m.ramBankOrRomHigh2 & 0x03) << 5)
		off := bank*0x4000 + int(addr)
		if off < len(m.rom) {
			return m.rom[off]
		}
		return 0xFF
	case addr < 0x8000:
		// Switchable ROM bank
		bank := int(m.effectiveROMBank())
		off := bank*0x4000 + int(addr-0x4000)
		if off < len(m.rom) {
			return m.rom[off]
		}
		return 0xFF
	case addr >= 0xA000 && addr <= 0xBFFF:
		if !m.ramEnabled || len(m.ram) == 0 {
			return 0xFF
		}
		ramBank := 0
		if m.modeSelect == 1 {
			ramBank = int(m.ramBankOrRomHigh2 & 0x03)
		}
		off := ramBank*0x2000 + int(addr-0xA000)
		if off >= 0 && off < len(m.ram) {
			return m.ram[off]
		}
		return 0xFF
	default:
		return 0xFF
	}
}

func (m *MBC1) Write(addr uint16, value byte) {
	switch {
	case addr < 0x2000:
		// RAM enable: low 4 bits must be 0x0A
		m.ramEnabled = (value & 0x0F) == 0x0A
	case addr < 0x4000:
		// ROM bank low 5 bits (0 maps to 1)
		m.romBankLow5 = value & 0x1F
		if m.romBankLow5 == 0 {
			m.romBankLow5 = 1
		}
	case addr < 0x6000:
		// RAM bank or ROM high bits (2 bits)
		m.ramBankOrRomHigh2 = value & 0x03
	case addr < 0x8000:
		// Mode select: 0 ROM banking, 1 RAM banking
		m.modeSelect = value & 0x01
	case addr >= 0xA000 && addr <= 0xBFFF:
		if !m.ramEnabled || len(m.ram) == 0 {
			return
		}
		ramBank := 0
		if m.modeSelect == 1 {
			ramBank = int(m.ramBankOrRomHigh2 & 0x03)
		}
		off := ramBank*0x2000 + int(addr-0xA000)
		if off >= 0 && off < len(m.ram) {
			m.ram[off] = value
		}
	}
}

func (m *MBC1) effectiveROMBank() byte {
	// Combine high 2 bits (when in ROM banking mode) with low5; ensure bank != 0, 0x20, 0x40, 0x60 remapped per spec
	high := m.ramBankOrRomHigh2 & 0x03
	bank := m.romBankLow5 | (high << 5)
	// Avoid forbidden banks when low5 is zero (already remapped to 1). Additional remap not strictly necessary here.
	return bank
}
