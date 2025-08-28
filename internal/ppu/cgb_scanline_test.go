package ppu

// Tests for CGB BG/window scanline helpers: attributes: palette, flips, bank, priority.
import "testing"

type fakeVRAM struct{ v0, v1 [0x2000]byte }

func (f *fakeVRAM) Read(addr uint16) byte { return 0 }
func (f *fakeVRAM) ReadBank(bank int, addr uint16) byte {
	if addr < 0x8000 || addr >= 0xA000 {
		return 0
	}
	off := addr - 0x8000
	if bank == 0 {
		return f.v0[off]
	}
	return f.v1[off]
}

func TestCGB_BG_Attrs_Flips_Bank_Palette(t *testing.T) {
	var v fakeVRAM
	// Put a tile in bank0 and a different tile in bank1
	// Tile index 1 pattern: left half 01, right half 10
	// Write row 0 in bank0 (unused by this test but kept for completeness)
	v.v0[0x0010+0] = 0xF0 // lo
	v.v0[0x0010+1] = 0x00 // hi
	// Since attr sets yflip, row=7 will be selected: write row 7 bytes in bank1
	v.v1[0x0010+14] = 0x0F // lo at row 7 (7*2)
	v.v1[0x0010+15] = 0x00 // hi
	// Map at 0x9800: place tile 1 at first entry
	v.v0[0x1800+0] = 0x01
	// Attrs at 0x9C00 mirror (we pass base explicitly): bank=1, xflip=1, yflip=1, pal=5, prio=1
	v.v1[0x1C00+0] = 0x80 | 0x40 | 0x20 | 0x10 | 0x05

	ci, pal, pri := RenderBGScanlineCGB(&v, 0x9800, 0x9C00, true, 0, 0, 0)
	if !pri[0] {
		t.Fatalf("priority not set")
	}
	if pal[0] != 5 {
		t.Fatalf("palette got %d want 5", pal[0])
	}
	// With xflip+yflip and bank1 data, first pixel should reflect flipped bits
	if ci[0] == 0 {
		t.Fatalf("unexpected ci 0 at first pixel")
	}
}

func TestCGB_Window_Basic(t *testing.T) {
	var v fakeVRAM
	// tile 2 pattern simple
	v.v0[0x0020+0] = 0xFF
	v.v0[0x0020+1] = 0x00
	v.v0[0x1800+0] = 0x02
	v.v1[0x1C00+0] = 0x00 // bank0, pal0
	ci, pal, pri := RenderWindowScanlineCGB(&v, 0x9800, 0x9C00, true, 0, 0)
	if pal[0] != 0 || pri[0] {
		t.Fatalf("unexpected pal/pri %d/%v", pal[0], pri[0])
	}
	if ci[0] == 0 {
		t.Fatalf("ci should be nonzero")
	}
}
