package cart

import "time"

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
	ramBank    byte // 0..3
	rtcSel     byte // 0x08..0x0C selects RTC register; 0xFF means none

	// RTC registers
	rtcSec   byte   // 0-59
	rtcMin   byte   // 0-59
	rtcHour  byte   // 0-23
	rtcDay   uint16 // 0-511 (9 bits)
	rtcHalt  bool
	rtcCarry bool

	// Latched snapshot for reads when latched
	latched bool
	lSec    byte
	lMin    byte
	lHour   byte
	lDay    uint16
	lHalt   bool
	lCarry  bool

	// Latch edge tracking and time update bookkeeping
	lastLatchWrite byte
	lastRTCWallSec int64
}

func NewMBC3(rom []byte, ramSize int) *MBC3 {
	m := &MBC3{rom: rom, rtcSel: 0xFF}
	if ramSize > 0 {
		m.ram = make([]byte, ramSize)
	}
	m.romBank = 1
	// initialize wall clock reference
	m.lastRTCWallSec = nowUnix()
	return m
}

func (m *MBC3) Read(addr uint16) byte {
	m.updateRTC()
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
		if !m.ramEnabled {
			return 0xFF
		}
		// If RTC selected, read RTC (latched if latched)
		if m.rtcSel >= 0x08 && m.rtcSel <= 0x0C {
			sec, min, hour, day, halt, carry := m.readLatchedRTC()
			switch m.rtcSel {
			case 0x08:
				return sec
			case 0x09:
				return min
			case 0x0A:
				return hour
			case 0x0B:
				return byte(day & 0xFF)
			case 0x0C:
				var v byte
				if (day>>8)&1 != 0 {
					v |= 0x01
				}
				if halt {
					v |= 0x40
				}
				if carry {
					v |= 0x80
				}
				return v
			}
			return 0xFF
		}
		// External RAM
		if len(m.ram) == 0 {
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
	m.updateRTC()
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
		// RAM bank 0..3 or RTC reg select 0x08..0x0C
		if value <= 0x03 {
			m.rtcSel = 0xFF
			m.ramBank = value & 0x03
		} else if value >= 0x08 && value <= 0x0C {
			m.rtcSel = value
		}
	case addr < 0x8000:
		// Latch clock: write 0 then 1 to latch
		if m.lastLatchWrite == 0 && (value&1) == 1 {
			m.latchRTC()
		}
		m.lastLatchWrite = value & 1
	case addr >= 0xA000 && addr <= 0xBFFF:
		if !m.ramEnabled {
			return
		}
		// If RTC selected, write to RTC registers
		if m.rtcSel >= 0x08 && m.rtcSel <= 0x0C {
			switch m.rtcSel {
			case 0x08:
				m.rtcSec = value % 60
			case 0x09:
				m.rtcMin = value % 60
			case 0x0A:
				m.rtcHour = value % 24
			case 0x0B:
				m.rtcDay = (m.rtcDay & 0x100) | uint16(value)
			case 0x0C:
				// bit0: day high, bit6: halt, bit7: carry
				if (value & 0x01) != 0 {
					m.rtcDay |= 0x100
				} else {
					m.rtcDay &^= 0x100
				}
				m.rtcHalt = (value & 0x40) != 0
				// carry is sticky until cleared by writing 0 to bit7
				if (value & 0x80) == 0 {
					m.rtcCarry = false
				}
			}
			return
		}
		// External RAM write
		if len(m.ram) == 0 {
			return
		}
		rb := int(m.ramBank & 0x03)
		off := rb*0x2000 + int(addr-0xA000)
		if off >= 0 && off < len(m.ram) {
			m.ram[off] = value
		}
	}
}

// --- RTC helpers ---
func (m *MBC3) readLatchedRTC() (sec, min, hour byte, day uint16, halt, carry bool) {
	if m.latched {
		return m.lSec, m.lMin, m.lHour, m.lDay, m.lHalt, m.lCarry
	}
	return m.rtcSec, m.rtcMin, m.rtcHour, m.rtcDay, m.rtcHalt, m.rtcCarry
}

func (m *MBC3) latchRTC() {
	// Capture current RTC into latched fields
	m.updateRTC()
	m.lSec, m.lMin, m.lHour = m.rtcSec, m.rtcMin, m.rtcHour
	m.lDay, m.lHalt, m.lCarry = m.rtcDay, m.rtcHalt, m.rtcCarry
	m.latched = true
}

func (m *MBC3) updateRTC() {
	if m.rtcHalt {
		return
	}
	now := nowUnix()
	if m.lastRTCWallSec == 0 {
		m.lastRTCWallSec = now
		return
	}
	if now <= m.lastRTCWallSec {
		return
	}
	delta := now - m.lastRTCWallSec
	m.lastRTCWallSec = now
	if delta <= 0 {
		return
	}
	// advance seconds by delta
	total := int(m.rtcSec) + int(delta)
	m.rtcSec = byte(total % 60)
	carryMin := total / 60
	if carryMin > 0 {
		totalMin := int(m.rtcMin) + carryMin
		m.rtcMin = byte(totalMin % 60)
		carryHour := totalMin / 60
		if carryHour > 0 {
			totalHour := int(m.rtcHour) + carryHour
			m.rtcHour = byte(totalHour % 24)
			carryDay := totalHour / 24
			if carryDay > 0 {
				days := int(m.rtcDay) + carryDay
				if days > 511 {
					m.rtcCarry = true
					days %= 512
				}
				m.rtcDay = uint16(days)
			}
		}
	}
}

// Dependency injection for time to ease tests
var nowUnix = func() int64 { return time.Now().Unix() }

// BatteryBacked implementation (RTC not persisted here)
// BatteryBacked implementation with RTC footer persistence
func (m *MBC3) SaveRAM() []byte {
	// RAM may be zero-length; still persist RTC if needed by returning only footer
	base := len(m.ram)
	footerLen := 4 + 6 + 8 // magic + (ver,sec,min,hour,dayLow,dayHighFlags) + wallsecs
	out := make([]byte, base+footerLen)
	copy(out, m.ram)
	// Footer
	copy(out[base:base+4], []byte{'G', 'B', 'M', '3'})
	i := base + 4
	out[i] = 1
	i++ // version
	out[i] = m.rtcSec
	i++
	out[i] = m.rtcMin
	i++
	out[i] = m.rtcHour
	i++
	out[i] = byte(m.rtcDay & 0xFF)
	i++
	hi := byte(0)
	if (m.rtcDay>>8)&1 != 0 {
		hi |= 0x01
	}
	if m.rtcHalt {
		hi |= 0x40
	}
	if m.rtcCarry {
		hi |= 0x80
	}
	out[i] = hi
	i++
	ws := m.lastRTCWallSec
	// write int64 little-endian
	for b := 0; b < 8; b++ {
		out[i+b] = byte(ws >> (8 * b))
	}
	return out
}

func (m *MBC3) LoadRAM(data []byte) {
	if len(data) == 0 {
		return
	}
	// Copy RAM portion if present
	if len(m.ram) > 0 {
		n := len(data)
		if n > len(m.ram) {
			n = len(m.ram)
		}
		copy(m.ram, data[:n])
	}
	// Parse footer if present
	if len(data) < len(m.ram)+4+6+8 {
		return
	}
	base := len(m.ram)
	if string(data[base:base+4]) != "GBM3" {
		return
	}
	i := base + 4
	ver := data[i]
	i++
	if ver != 1 {
		return
	}
	m.rtcSec = data[i]
	i++
	m.rtcMin = data[i]
	i++
	m.rtcHour = data[i]
	i++
	low := data[i]
	i++
	hi := data[i]
	i++
	m.rtcDay = (uint16(hi&0x01) << 8) | uint16(low)
	m.rtcHalt = (hi & 0x40) != 0
	m.rtcCarry = (hi & 0x80) != 0
	// read int64 little-endian
	var ws int64
	for b := 0; b < 8; b++ {
		ws |= int64(data[i+b]) << (8 * b)
	}
	m.lastRTCWallSec = ws
}
