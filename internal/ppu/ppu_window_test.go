package ppu

import "testing"

// advanceLines ticks the PPU forward by n full visible lines (456 dots each).
func advanceLines(p *PPU, n int) { p.Tick(456 * n) }

func TestWindowActivationAndCounter(t *testing.T) {
	p := New(nil)
	// Enable LCD, BG and Window
	p.CPUWrite(0xFF40, 0x80)           // LCD on
	p.CPUWrite(0xFF40, 0x80|0x01)      // BG on
	p.CPUWrite(0xFF40, 0x80|0x01|0x20) // Window on
	// Set WY and WX
	p.CPUWrite(0xFF4A, 10) // WY = 10
	p.CPUWrite(0xFF4B, 7)  // WX = 7 -> winXStart=0

	// After turning LCD on, we start at LY=0 mode 2
	// Advance to line 10 (WY)
	advanceLines(p, 10)
	if ly := p.CPURead(0xFF44); ly != 10 {
		t.Fatalf("expected LY=10, got %d", ly)
	}
	// Enter mode 3 on line 10 so that capture occurs
	p.Tick(80)
	// During line WY, WinLine should be 0
	lr := p.LineRegs(10)
	if lr.WinLine != 0 {
		t.Fatalf("expected WinLine=0 at WY, got %d", lr.WinLine)
	}
	// Next line should increment WinLine to 1; enter mode 3 for line 11 before reading
	advanceLines(p, 1)
	p.Tick(80)
	lr2 := p.LineRegs(11)
	if lr2.WinLine != 1 {
		t.Fatalf("expected WinLine=1 at WY+1, got %d", lr2.WinLine)
	}
}

func TestWindowNotVisibleWhenWXTooLarge(t *testing.T) {
	p := New(nil)
	// Enable LCD, BG and Window
	p.CPUWrite(0xFF40, 0x80|0x01|0x20)
	// Set WY=5 and WX>166 so window should not be visible
	p.CPUWrite(0xFF4A, 5)
	p.CPUWrite(0xFF4B, 200)
	// Advance to several lines beyond WY
	advanceLines(p, 8)
	// WinLine should remain 0 on captured regs since window not visible
	for y := 5; y <= 12; y++ {
		if p.LineRegs(y).WinLine != 0 {
			t.Fatalf("expected WinLine=0 at y=%d when WX>=166", y)
		}
	}
}
