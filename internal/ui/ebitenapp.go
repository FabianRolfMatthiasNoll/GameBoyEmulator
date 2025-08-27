package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"encoding/binary"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/emu"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

type App struct {
	cfg    Config
	m      *emu.Machine
	tex    *ebiten.Image
	paused bool
	fast   bool

	// audio
	audioCtx    *audio.Context
	audioPlayer *audio.Player

	// overlay/menu
	showMenu bool
	menuIdx  int    // selection index for current menu
	menuMode string // "main" | "rom" | "keys" | "settings"

	// rom picker state
	romList []string
	romSel  int
	romOff  int // scroll offset for ROM list

	// keybindings state
	keysOff int // scroll offset for keybindings

	// toast feedback
	toastMsg   string
	toastUntil time.Time
}

func NewApp(cfg Config, m *emu.Machine) *App {
	if cfg.Scale <= 0 {
		cfg.Scale = 3
	}
	ebiten.SetWindowTitle(cfg.Title)
	ebiten.SetWindowSize(160*cfg.Scale, 144*cfg.Scale)
	a := &App{cfg: cfg, m: m}
	// Init audio at 48kHz to match APU
	a.audioCtx = audio.NewContext(48000)
	// Create a streaming player that pulls from the emulator APU
	if p, err := a.audioCtx.NewPlayer(&apuStream{m: m}); err == nil {
		a.audioPlayer = p
		a.audioPlayer.Play()
	}
	return a
}

func (a *App) Run() error { return ebiten.RunGame(a) }

func (a *App) Update() error {
	// Keyboard → Game Boy buttons (disabled when menu is shown)
	if !a.showMenu {
		var btn emu.Buttons
		if ebiten.IsKeyPressed(ebiten.KeyRight) {
			btn.Right = true
		}
		if ebiten.IsKeyPressed(ebiten.KeyLeft) {
			btn.Left = true
		}
		if ebiten.IsKeyPressed(ebiten.KeyUp) {
			btn.Up = true
		}
		if ebiten.IsKeyPressed(ebiten.KeyDown) {
			btn.Down = true
		}
		if ebiten.IsKeyPressed(ebiten.KeyZ) {
			btn.A = true
		}
		if ebiten.IsKeyPressed(ebiten.KeyX) {
			btn.B = true
		}
		if ebiten.IsKeyPressed(ebiten.KeyEnter) {
			btn.Start = true
		}
		if ebiten.IsKeyPressed(ebiten.KeyShiftRight) {
			btn.Select = true
		}
		a.m.SetButtons(btn)
	} else {
		a.m.SetButtons(emu.Buttons{})
	}

	// Pause toggle (P)
	if inpututil.IsKeyJustPressed(ebiten.KeyP) {
		a.paused = !a.paused
	}

	// Fast-forward (Tab): while held, run multiple frames per Ebiten update
	a.fast = ebiten.IsKeyPressed(ebiten.KeyTab)

	// Reset shortcuts
	if inpututil.IsKeyJustPressed(ebiten.KeyR) { // post-boot reset
		a.m.ResetPostBoot()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyB) { // boot ROM reset
		a.m.ResetWithBoot()
	}

	// Frame-step when paused (N)
	if !a.showMenu && a.paused && inpututil.IsKeyJustPressed(ebiten.KeyN) {
		a.m.StepFrame()
	}

	// Toggle menu (Escape)
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		a.showMenu = !a.showMenu
		if a.showMenu {
			a.menuMode = "main"
			a.menuIdx = 0
		}
	}
	if a.showMenu {
		switch a.menuMode {
		case "main":
			// Navigate main menu (Save, Load, Switch ROM, Settings, Keybindings, Close)
			max := 5
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.menuIdx > 0 {
				a.menuIdx--
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) && a.menuIdx < max {
				a.menuIdx++
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
				switch a.menuIdx {
				case 0: // Save
					if err := a.m.SaveStateToFile("slot0.savestate"); err == nil {
						a.toast("Saved state to slot0.savestate")
					} else {
						a.toast("Save failed: " + err.Error())
					}
				case 1: // Load
					if err := a.m.LoadStateFromFile("slot0.savestate"); err == nil {
						a.toast("Loaded state from slot0.savestate")
					} else {
						a.toast("Load failed: " + err.Error())
					}
				case 2: // Switch ROM
					a.romList = a.findROMs()
					a.romSel = 0
					a.romOff = 0
					a.menuMode = "rom"
				case 3: // Settings
					a.menuMode = "settings"
					a.menuIdx = 0
				case 4: // Keybindings
					a.menuMode = "keys"
					a.keysOff = 0
				case 5: // Close
					a.showMenu = false
				}
			}
		case "rom":
			// ROM picker
			n := len(a.romList)
			if n == 0 {
				if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
					a.menuMode = "main"
				}
			} else {
				// visible rows
				baseY := 28
				maxRows := (144 - baseY) / 14
				if maxRows < 1 {
					maxRows = 1
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.romSel > 0 {
					a.romSel--
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) && a.romSel < n-1 {
					a.romSel++
				}
				// maintain scrolling window
				if a.romSel < a.romOff {
					a.romOff = a.romSel
				}
				if a.romSel >= a.romOff+maxRows {
					a.romOff = a.romSel - maxRows + 1
				}
				if a.romOff < 0 {
					a.romOff = 0
				}
				if a.romOff > n-1 {
					a.romOff = n - 1
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
					path := a.romList[a.romSel]
					if err := a.m.LoadROMFromFile(path); err == nil {
						a.toast("Loaded ROM: " + filepath.Base(path))
					} else {
						a.toast("ROM load failed: " + err.Error())
					}
					a.menuMode = "main"
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
					a.menuMode = "main"
				}
			}
		case "keys":
			// Scrollable keybindings list
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.keysOff > 0 {
				a.keysOff--
			}
			// compute how many rows fit to clamp scrolling down in draw; allow down here freely
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) {
				a.keysOff++
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
				a.menuMode = "main"
			}
		case "settings":
			// Select a setting with Up/Down; change with Left/Right.
			// Only one setting for now: Scale.
			items := 1
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.menuIdx > 0 {
				a.menuIdx--
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) && a.menuIdx < items-1 {
				a.menuIdx++
			}
			if a.menuIdx == 0 { // Scale
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
					if a.cfg.Scale > 1 {
						a.cfg.Scale--
						ebiten.SetWindowSize(160*a.cfg.Scale, 144*a.cfg.Scale)
					}
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
					if a.cfg.Scale < 10 {
						a.cfg.Scale++
						ebiten.SetWindowSize(160*a.cfg.Scale, 144*a.cfg.Scale)
					}
				}
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
				a.menuMode = "main"
			}
		}
	}

	// Screenshot (F12)
	if inpututil.IsKeyJustPressed(ebiten.KeyF12) {
		_ = a.saveScreenshot()
	}

	// Freeze emulation while menu is open
	if !a.showMenu {
		if !a.paused {
			if a.fast {
				for i := 0; i < 5; i++ {
					a.m.StepFrame()
				}
			} else {
				a.m.StepFrame()
			}
		}
	}

	return nil
}

// apuStream implements io.Reader by pulling PCM samples from the emulator APU and
// converting them to 16-bit little-endian stereo frames.
type apuStream struct{ m *emu.Machine }

func (s *apuStream) Read(p []byte) (int, error) {
	if len(p) == 0 || s == nil || s.m == nil {
		return 0, nil
	}
	// Each frame is 4 bytes (stereo int16). Try to fill entire buffer; avoid output underruns.
	want := len(p) / 4
	filled := 0
	// Keep a simple last-sample for graceful underflows
	var last int16
	for filled < want {
		frames := s.m.APUPullStereo(want - filled)
		// Convert pulled frames (interleaved) to mono and write both channels
		i := filled * 4
		for j := 0; j+1 < len(frames) && i+3 < len(p); j += 2 {
			l := int32(frames[j])
			r := int32(frames[j+1])
			m := int16((l + r) / 2)
			last = m
			binary.LittleEndian.PutUint16(p[i:], uint16(m))
			binary.LittleEndian.PutUint16(p[i+2:], uint16(m))
			i += 4
			filled++
		}
		if filled >= want {
			break
		}
		// If not enough frames available yet, fill a tiny chunk with last to prevent hard gaps.
		// This smooths over jitter without adding latency.
		padFrames := want - filled
		if padFrames > 8 {
			padFrames = 8
		}
		for k := 0; k < padFrames; k++ {
			idx := (filled + k) * 4
			binary.LittleEndian.PutUint16(p[idx:], uint16(last))
			binary.LittleEndian.PutUint16(p[idx+2:], uint16(last))
		}
		filled += padFrames
	}
	return filled * 4, nil
}

func (a *App) Draw(screen *ebiten.Image) {
	if a.tex == nil {
		a.tex = ebiten.NewImage(160, 144)
	}
	a.tex.WritePixels(a.m.Framebuffer())
	screen.DrawImage(a.tex, nil)

	// Toast message
	if a.toastMsg != "" && time.Now().Before(a.toastUntil) {
		msg := a.toastMsg
		msg = a.truncateText(msg, a.maxCharsForText(6))
		ebitenutil.DebugPrintAt(screen, msg, 6, 4)
	}

	if a.showMenu {
		overlay := ebiten.NewImage(160, 144)
		overlay.Fill(color.RGBA{0, 0, 0, 140})
		screen.DrawImage(overlay, nil)
		switch a.menuMode {
		case "main":
			lines := []string{
				"Menu:",
				"  Save state (slot 0)",
				"  Load state (slot 0)",
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
		case "rom":
			ebitenutil.DebugPrintAt(screen, "Select ROM (Enter to load, Esc back)", 10, 10)
			if len(a.romList) == 0 {
				ebitenutil.DebugPrintAt(screen, "No .gb files found in testroms/", 10, 28)
			}
			baseY := 28
			maxRows := (144 - baseY) / 14
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
		case "keys":
			title := "Keybindings (Up/Down to scroll, Enter/Esc to return)"
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
			maxRows := (144 - baseY) / 14
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
		case "settings":
			title := "Settings (Use Up/Down to select; Left/Right to change; Enter/Esc to return)"
			cursorY := 10
			for _, w := range a.wrapText(title, a.maxCharsForText(10)) {
				ebitenutil.DebugPrintAt(screen, w, 10, cursorY)
				cursorY += 14
			}
			// items
			items := []string{
				fmt.Sprintf("Scale: %dx", a.cfg.Scale),
			}
			for i, it := range items {
				prefix := "  "
				if i == a.menuIdx {
					prefix = "> "
				}
				line := prefix + it
				line = a.truncateText(line, a.maxCharsForText(10))
				ebitenutil.DebugPrintAt(screen, line, 10, cursorY+i*14)
			}
		}
	}
}

// toast displays a short message at the top-left
func (a *App) toast(msg string) {
	a.toastMsg = msg
	a.toastUntil = time.Now().Add(2 * time.Second)
}

// findROMs returns a sorted list of ROM file paths from testroms/ (and current dir if .gb files found)
func (a *App) findROMs() []string {
	var files []string
	addFrom := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(strings.ToLower(name), ".gb") {
				files = append(files, filepath.Join(dir, name))
			}
		}
	}
	addFrom("testroms")
	addFrom(".")
	sort.Strings(files)
	// de-dup
	uniq := files[:0]
	seen := map[string]bool{}
	for _, p := range files {
		if seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, p)
	}
	return uniq
}

func (a *App) Layout(outW, outH int) (int, int) { return 160, 144 }

// maxCharsForText estimates how many characters fit on a line starting at left margin x.
// This uses a conservative ~6px per character for the debug font.
func (a *App) maxCharsForText(left int) int {
	w := 160 - left - 4
	if w < 6 {
		return 1
	}
	return w / 6
}

// truncateText trims s to fit within max characters, appending "..." when truncated.
func (a *App) truncateText(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// wrapText wraps a string into lines no longer than max characters, breaking at spaces when possible.
func (a *App) wrapText(s string, max int) []string {
	if max <= 0 {
		return []string{""}
	}
	var lines []string
	for len(s) > 0 {
		if len(s) <= max {
			lines = append(lines, s)
			break
		}
		// find last space within max
		cut := -1
		for i := max; i >= 0 && i < len(s); i-- {
			if s[i] == ' ' {
				cut = i
				break
			}
			if i == 0 {
				break
			}
		}
		if cut == -1 || cut == 0 {
			// no space to break, hard wrap
			lines = append(lines, s[:max])
			s = s[max:]
			continue
		}
		lines = append(lines, strings.TrimRight(s[:cut], " "))
		s = strings.TrimLeft(s[cut+1:], " ")
	}
	return lines
}

func (a *App) saveScreenshot() error {
	fb := a.m.Framebuffer()
	img := &image.RGBA{
		Pix:    make([]byte, len(fb)),
		Stride: 4 * 160,
		Rect:   image.Rect(0, 0, 160, 144),
	}
	copy(img.Pix, fb)
	ts := time.Now().Format("20060102_150405")
	name := fmt.Sprintf("screenshot_%s.png", ts)
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
