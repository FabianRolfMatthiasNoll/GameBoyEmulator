package emu

import (
	"strings"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/cart"
)

// compatTitleExact maps exact, normalized titles to a preferred palette ID.
// Note: IDs index into cgbCompatSetNames/cgbCompatSets in emu.go.
var compatTitleExact = map[string]int{
	"TETRIS":              2, // Blue
	"TETRIS DX":           2,
	"SUPER MARIO LAND":    3, // Red
	"SUPER MARIO LAND 2":  3,
	"DR. MARIO":           4, // Pastel
	"DONKEY KONG":         1, // Sepia
	"THE LEGEND OF ZELDA": 0, // Green
	"ZELDA":               0,
	"METROID II":          3, // Red accent
	"KIRBY'S DREAM LAND":  4, // Pastel/soft
	"MEGA MAN":            2, // Blue
	"MEGAMAN":             2,
	"WARIO LAND":          1, // Sepia
	"POKEMON YELLOW":      4, // Pastel
	"POKEMON RED":         4,
	"POKEMON BLUE":        4,
	"POCKET MONSTERS":     4,
}

type containsRule struct {
	substr string
	id     int
}

// compatTitleContains applies broader substring heuristics for families.
var compatTitleContains = []containsRule{
	{"TETRIS", 2},
	{"MARIO", 3},
	{"ZELDA", 0},
	{"KIRBY", 4},
	{"DONKEY KONG", 1},
	{"METROID", 3},
	{"MEGA MAN", 2},
	{"MEGAMAN", 2},
	{"WARIO", 1},
	{"POKEMON", 4},
	{"POCKET MONSTERS", 4},
}

// autoCompatPaletteFromHeader tries to pick a good default palette using a small title table
// and then a stable fallback based on licensee/checksum. Returns (id, true) on success.
func autoCompatPaletteFromHeader(h *cart.Header) (int, bool) {
	if h == nil {
		return 0, false
	}
	title := strings.TrimSpace(strings.TrimRight(h.Title, "\x00"))
	t := strings.ToUpper(title)
	if id, ok := compatTitleExact[t]; ok {
		return id, true
	}
	for _, r := range compatTitleContains {
		if strings.Contains(t, r.substr) {
			return r.id, true
		}
	}
	// Fallback: for Nintendo-published titles, vary palette by header checksum; others use default.
	nintendo := false
	if h.OldLicensee == 0x33 {
		nintendo = (strings.ToUpper(h.NewLicensee) == "01")
	} else {
		nintendo = (h.OldLicensee == 0x01)
	}
	if nintendo {
		// Use header checksum to pick a stable palette across sessions.
		// Keep it within available set count (len(cgbCompatSetNames)).
		// We mod by 6 to align with our curated set length.
		return int(h.HeaderChecksum) % 6, true
	}
	return 0, true
}
