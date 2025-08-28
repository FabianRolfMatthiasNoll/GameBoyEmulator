package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
)

func (a *App) drawMainMenu(screen *ebiten.Image) {
	lines := []string{
		"Menu:",
		fmt.Sprintf("  Save state (slot %d)", a.currentSlot+1),
		fmt.Sprintf("  Load state (slot %d)", a.currentSlot+1),
		"  Select Slot",
		"  Switch ROM",
		"  Settings",
		"  Keybindings",
		"  Close",
	}
	for i, s := range lines {
		prefix := "  "
		if i == a.menuIdx+1 {
			prefix = "> "
		}
		ebitenutil.DebugPrintAt(screen, prefix+s, 10, 10+i*14)
	}
	// quick hints, keep on-screen
	hint := "F5: Save  F9: Load  1-4: Slot  F11: Fullscreen  Backspace: Back"
	maxChars := a.maxCharsForText(10)
	if len(hint) > maxChars {
		hint = a.truncateText(hint, maxChars)
	}
	ebitenutil.DebugPrintAt(screen, hint, 10, 10+len(lines)*14)
}

func (a *App) drawSlotMenu(screen *ebiten.Image) {
	// Show 4 slots, gray out empty ones
	lines := []string{"Select Slot:"}
	for i := 0; i < 4; i++ {
		state := "(empty)"
		if _, err := os.Stat(a.statePath(i)); err == nil {
			state = ""
		}
		label := fmt.Sprintf("  %d %s", i+1, state)
		lines = append(lines, label)
	}
	for i, s := range lines {
		prefix := "  "
		if i == a.menuIdx+1 {
			prefix = "> "
		}
		text := prefix + strings.ReplaceAll(s, "(empty)", "[empty]")
		ebitenutil.DebugPrintAt(screen, text, 10, 10+i*14)
	}
}

func (a *App) drawRomMenu(screen *ebiten.Image) {
	ebitenutil.DebugPrintAt(screen, "Select ROM (Enter to load, Backspace/Esc to return)", 10, 10)
	// show configured ROMs directory
	d := a.cfg.ROMsDir
	d = a.truncateText("Dir: "+d, a.maxCharsForText(10))
	ebitenutil.DebugPrintAt(screen, d, 10, 24)
	if len(a.romList) == 0 {
		ebitenutil.DebugPrintAt(screen, "No ROMs found", 10, 40)
	}
	baseY := 40
	maxRows := (a.curH - baseY) / 14
	if maxRows < 1 {
		maxRows = 1
	}
	end := a.romOff + maxRows
	if end > len(a.romList) {
		end = len(a.romList)
	}
	visible := a.romList[a.romOff:end]
	maxChars := a.maxCharsForText(10) - 2 // account for "> " prefix
	if maxChars < 1 {
		maxChars = 1
	}
	for i, p := range visible {
		name := filepath.Base(p)
		name = a.truncateText(name, maxChars)
		prefix := "  "
		if a.romOff+i == a.romSel {
			prefix = "> "
		}
		ebitenutil.DebugPrintAt(screen, prefix+name, 10, baseY+i*14)
	}
	// scroll indicators
	if a.romOff > 0 {
		ebitenutil.DebugPrintAt(screen, "^", 2, baseY)
	}
	if end < len(a.romList) {
		ebitenutil.DebugPrintAt(screen, "v", 2, baseY+(maxRows-1)*14)
	}
}

func (a *App) drawKeysMenu(screen *ebiten.Image) {
	title := "Keybindings (Up/Down to scroll, Backspace/Esc to return)"
	cursorY := 10
	for _, w := range a.wrapText(title, a.maxCharsForText(10)) {
		ebitenutil.DebugPrintAt(screen, w, 10, cursorY)
		cursorY += 14
	}
	rows := []string{
		"Z: A",
		"X: B",
		"Enter: Start",
		"RightShift: Select",
		"Arrows: D-Pad",
		"P: Pause",
		"N: Step (when paused)",
		"Tab: Fast-forward",
		"R: Reset",
		"B: Reset with Boot ROM",
		"Esc: Open/Close Menu",
	}
	baseY := cursorY + 4
	maxRows := (a.curH - baseY) / 14
	if maxRows < 1 {
		maxRows = 1
	}
	if a.keysOff < 0 {
		a.keysOff = 0
	}
	if a.keysOff > len(rows)-1 {
		a.keysOff = len(rows) - 1
	}
	end := a.keysOff + maxRows
	if end > len(rows) {
		end = len(rows)
	}
	maxChars := a.maxCharsForText(10)
	for i := a.keysOff; i < end; i++ {
		line := a.truncateText(rows[i], maxChars)
		ebitenutil.DebugPrintAt(screen, line, 10, baseY+(i-a.keysOff)*14)
	}
	// scroll indicators
	if a.keysOff > 0 {
		ebitenutil.DebugPrintAt(screen, "^", 2, baseY)
	}
	if end < len(rows) {
		ebitenutil.DebugPrintAt(screen, "v", 2, baseY+(maxRows-1)*14)
	}
}

func (a *App) drawSettingsMenu(screen *ebiten.Image) {
	title := "Settings (Up/Down select; Left/Right change; Enter: edit/apply; Backspace/Esc: back)"
	cursorY := 10
	for _, w := range a.wrapText(title, a.maxCharsForText(10)) {
		ebitenutil.DebugPrintAt(screen, w, 10, cursorY)
		cursorY += 14
	}
	// items
	romDir := a.cfg.ROMsDir
	if a.editingROMDir {
		romDir = a.romDirInput + "_"
	}
	items := []string{
		fmt.Sprintf("Scale: %dx", a.cfg.Scale),
		fmt.Sprintf("Audio: %s", map[bool]string{true: "Stereo", false: "Mono"}[a.cfg.AudioStereo]),
		fmt.Sprintf("Audio Adaptive: %s", map[bool]string{true: "On", false: "Off"}[a.cfg.AudioAdaptive]),
		fmt.Sprintf("Low-Latency Audio: %s", map[bool]string{true: "On", false: "Off"}[a.cfg.AudioLowLatency]),
		fmt.Sprintf("BG Renderer: %s", map[bool]string{true: "Fetcher", false: "Classic"}[a.cfg.UseFetcherBG]),
		fmt.Sprintf("ROMs Dir: %s", a.truncateText(romDir, a.maxCharsForText(10)-11)),
		fmt.Sprintf("CGB Colors: %s", map[bool]string{true: "On", false: "Off"}[a.m != nil && a.m.WantCGBColors()]),
	}
	// If in CGB compatibility mode, show current palette hint with name
	if a.m != nil && a.m.IsCGBCompat() {
		pid := a.m.CurrentCompatPalette()
		items = append(items, fmt.Sprintf("Compat Palette: %d - %s  ([/]): cycle", pid, a.m.CompatPaletteName(pid)))
		items = append(items, fmt.Sprintf("Shell Overlay: %s  (F10 toggles)", map[bool]string{true: "On", false: "Off"}[a.cfg.ShellOverlay]))
	}
	baseY := cursorY
	maxRows := (a.curH - baseY) / 14
	if maxRows < 1 {
		maxRows = 1
	}
	end := a.settingsOff + maxRows
	if end > len(items) {
		end = len(items)
	}
	for i := a.settingsOff; i < end; i++ {
		prefix := "  "
		if i == a.menuIdx {
			prefix = "> "
		}
		line := prefix + items[i]
		line = a.truncateText(line, a.maxCharsForText(10))
		ebitenutil.DebugPrintAt(screen, line, 10, baseY+(i-a.settingsOff)*14)
	}
	// scroll indicators
	if a.settingsOff > 0 {
		ebitenutil.DebugPrintAt(screen, "^", 2, baseY)
	}
	if end < len(items) {
		ebitenutil.DebugPrintAt(screen, "v", 2, baseY+(maxRows-1)*14)
	}
}
