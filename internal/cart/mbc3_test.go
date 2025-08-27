package cart

import "testing"

func TestMBC3_RTC_LatchAndRead(t *testing.T) {
	// Save and mock time
	prevNow := nowUnix
	nowUnix = func() int64 { return 100 }
	defer func() { nowUnix = prevNow }()

	rom := make([]byte, 0x8000)
	m := NewMBC3(rom, 0x2000)

	// Enable RAM/RTC access, set RTC values and latch
	m.Write(0x0000, 0x0A) // RAM enable
	m.rtcSec, m.rtcMin, m.rtcHour, m.rtcDay = 5, 6, 7, 0x101
	m.rtcHalt, m.rtcCarry = false, false
	m.Write(0x6000, 0x01) // latch (0->1)

	// Select RTC seconds
	m.Write(0x4000, 0x08)
	if got := m.Read(0xA000); got != 5 {
		t.Fatalf("latched sec got %d want 5", got)
	}
	// Change live sec; latched read should remain 5
	m.rtcSec = 30
	if got := m.Read(0xA000); got != 5 {
		t.Fatalf("latched sec changed unexpectedly: got %d", got)
	}

	// Read day low and day high/carry/halt
	m.Write(0x4000, 0x0B)
	if got := m.Read(0xA000); got != byte(0x101&0xFF) {
		t.Fatalf("latched day low got %02X want %02X", got, byte(0x01))
	}
	m.Write(0x4000, 0x0C)
	got := m.Read(0xA000)
	if (got & 0x01) == 0 {
		t.Fatalf("latched day high bit not set")
	}
	if (got & 0x40) != 0 {
		t.Fatalf("halt bit set unexpectedly")
	}
}

func TestMBC3_RTC_Advance_And_Persist(t *testing.T) {
	prevNow := nowUnix
	// Start at 100s
	nowVal := int64(100)
	nowUnix = func() int64 { return nowVal }
	defer func() { nowUnix = prevNow }()

	rom := make([]byte, 0x8000)
	m := NewMBC3(rom, 0x2000)
	// Choose sec=30 to avoid crossing minute on first 20s step
	m.rtcSec, m.rtcMin, m.rtcHour, m.rtcDay = 30, 59, 23, 0x1FF
	m.rtcHalt, m.rtcCarry = false, false
	m.lastRTCWallSec = nowVal

	// Advance 20s -> sec:50, min stays 59
	nowVal = 120
	_ = m.Read(0x0000) // trigger update
	if m.rtcSec != 50 || m.rtcMin != 59 {
		t.Fatalf("rtc advance 20s got sec=%d min=%d", m.rtcSec, m.rtcMin)
	}

	// Advance 60s -> min increments (59->0), hour/day rollover, carry set and day wraps to 0
	nowVal = 180
	_ = m.Read(0x0001)
	if m.rtcSec != 50 || m.rtcMin != 0 || m.rtcHour != 0 || m.rtcDay != 0 || !m.rtcCarry {
		t.Fatalf("rtc +60s rollover got %02d:%02d:%02d day=%03d carry=%v",
			m.rtcHour, m.rtcMin, m.rtcSec, m.rtcDay, m.rtcCarry)
	}

	// Save and load into a new cart and verify RTC persisted
	data := m.SaveRAM()
	n := NewMBC3(rom, 0x2000)
	n.LoadRAM(data)
	if n.rtcSec != m.rtcSec || n.rtcMin != m.rtcMin || n.rtcHour != m.rtcHour || n.rtcDay != m.rtcDay {
		t.Fatalf("rtc persist mismatch: got %02d:%02d:%02d day=%03d want %02d:%02d:%02d day=%03d",
			n.rtcHour, n.rtcMin, n.rtcSec, n.rtcDay, m.rtcHour, m.rtcMin, m.rtcSec, m.rtcDay)
	}
}
