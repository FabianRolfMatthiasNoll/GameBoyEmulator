package ui

import (
	"encoding/json"
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
	// timing
	lastTime   time.Time
	frameAcc   float64 // accumulated fractional frames
	audioMuted bool

	// audio
	audioCtx    *audio.Context
	audioPlayer *audio.Player
	audioSrc    *apuStream // for stats overlay

	// overlay/menu
	showMenu  bool
	menuIdx   int    // selection index for current menu
	menuMode  string // "main" | "rom" | "keys" | "settings"
	showStats bool   // debug: show audio buffer stats
	// adaptive audio buffering
	targetFrames int // desired stereo frames in buffer
	stableTicks  int // ticks since last underrun

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
	// Load settings from file if present, then apply defaults and merge
	cfg = loadSettings(cfg)
	cfg.Defaults()
	ebiten.SetWindowTitle(cfg.Title)
	ebiten.SetWindowSize(160*cfg.Scale, 144*cfg.Scale)
	a := &App{cfg: cfg, m: m}
	a.lastTime = time.Now()
	a.frameAcc = 0
	// Init audio at 48kHz to match APU
	a.audioCtx = audio.NewContext(48000)
	// Configure adaptive buffering and initial target
	if cfg.AudioBufferMs <= 0 {
		cfg.AudioBufferMs = 125
	}
	a.targetFrames = (cfg.AudioBufferMs * 48000) / 1000
	a.stableTicks = 0
	// Defer creating the player until Update runs to ensure window init isn't blocked
	// If no ROM is loaded yet, open the ROM picker automatically
	if m != nil && m.ROMPath() == "" {
		a.showMenu = true
		a.menuMode = "rom"
		a.menuIdx = 0
		a.romList = a.findROMs()
		a.romSel = 0
		a.romOff = 0
	}
	return a
}

func (a *App) Run() error { return ebiten.RunGame(a) }

// SaveSettings persists current settings to disk.
func (a *App) SaveSettings() { a.saveSettings() }

func (a *App) Update() error {
	// Lazy-create audio player on first update to avoid startup blocking before the window appears
	if a.audioPlayer == nil {
		// Prefill a bit to prevent initial underruns BEFORE starting the player
		for i := 0; i < 12; i++ {
			a.m.StepFrame()
		}
		a.audioSrc = &apuStream{m: a.m, mono: !a.cfg.AudioStereo, muted: &a.audioMuted}
		if p, err := a.audioCtx.NewPlayer(a.audioSrc); err == nil {
			a.audioPlayer = p
			a.audioPlayer.Play()
		}
	}
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
	// Apply mute when paused or menu shown; reset pacing on transitions
	muted := a.paused || a.showMenu
	if muted != a.audioMuted {
		a.audioMuted = muted
		a.lastTime = time.Now()
		a.frameAcc = 0
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
				case 0: // Save (slot 0)
					if err := a.saveSlot(0); err == nil {
						a.toast("Saved slot 0")
					} else {
						a.toast("Save failed: " + err.Error())
					}
				case 1: // Load (slot 0)
					if err := a.loadSlot(0); err == nil {
						a.toast("Loaded slot 0")
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
						// Try to load battery save next to ROM
						if strings.HasSuffix(strings.ToLower(path), ".gb") {
							sav := strings.TrimSuffix(path, ".gb") + ".sav"
							if data, err := os.ReadFile(sav); err == nil {
								_ = a.m.LoadBattery(data)
							}
						}
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
			// Items: Scale, Audio Output, Audio Adaptive
			items := 3
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
			} else if a.menuIdx == 1 { // Audio Output
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
					a.cfg.AudioStereo = !a.cfg.AudioStereo
					// Recreate player with new mono/stereo mode
					if a.audioPlayer != nil {
						a.audioPlayer.Close()
						a.audioPlayer = nil
					}
					// Prefill some frames by stepping emulation to reduce initial underruns
					for i := 0; i < 12; i++ {
						a.m.StepFrame()
					}
					// Create a streaming player that pulls from the emulator APU (after prefill)
					a.audioSrc = &apuStream{m: a.m, mono: !a.cfg.AudioStereo, muted: &a.audioMuted}
					if p, err := a.audioCtx.NewPlayer(a.audioSrc); err == nil {
						a.audioPlayer = p
						a.audioPlayer.Play()
					}
				} else if a.menuIdx == 2 { // Audio Adaptive
					if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
						a.cfg.AudioAdaptive = !a.cfg.AudioAdaptive
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
	// Toggle stats overlay (F8)
	if inpututil.IsKeyJustPressed(ebiten.KeyF8) {
		a.showStats = !a.showStats
	}

	// Emulation pacing: run at ~59.7275 FPS using a time accumulator, decoupled from Ebiten's ~60Hz
	if !a.showMenu && !a.paused {
		now := time.Now()
		dt := now.Sub(a.lastTime).Seconds()
		if dt < 0 {
			dt = 0
		}
		a.lastTime = now
		gbFps := 4194304.0 / 70224.0 // ~59.7275
		speed := 1.0
		if a.fast {
			speed = 5.0
		}
		a.frameAcc += dt * gbFps * speed
		// Step whole frames
		steps := 0
		for a.frameAcc >= 1.0 && steps < 10 { // cap to avoid spiral of death
			a.m.StepFrame()
			a.frameAcc -= 1.0
			steps++
		}
		// Adaptive audio buffer target: raise on underruns, decay slowly when stable
		if a.cfg.AudioAdaptive && a.audioSrc != nil {
			if a.audioSrc.underruns > 0 {
				a.stableTicks = 0
				if a.targetFrames < 10500 {
					a.targetFrames += 600
				}
				a.audioSrc.underruns = 0
			} else {
				a.stableTicks++
				if a.stableTicks > 120 {
					if a.targetFrames > 5400 {
						a.targetFrames -= 300
					}
					a.stableTicks = 0
				}
			}
		}
		// Audio buffer top-up toward current target
		target := a.targetFrames
		buffered := a.m.APUBufferedStereo()
		if buffered < target {
			deficit := target - buffered
			// ~samples per GB frame at 48kHz: ~803
			const spf = 803
			framesNeeded := (deficit + spf - 1) / spf
			if framesNeeded > 8 {
				framesNeeded = 8
			} // small cap per tick
			for i := 0; i < framesNeeded; i++ {
				a.m.StepFrame()
			}
		}
	}

	return nil
}

// apuStream implements io.Reader by pulling PCM samples from the emulator APU and
// converting them to 16-bit little-endian stereo frames.
type apuStream struct {
	m     *emu.Machine
	mono  bool
	muted *bool
	// stats
	underruns  int
	lastWant   int
	lastPulled int
}

func (s *apuStream) Read(p []byte) (int, error) {
	if len(p) == 0 || s == nil || s.m == nil {
		return 0, nil
	}
	// If buffer is smaller than a full stereo frame (4 bytes), fill with silence to avoid returning 0 bytes.
	if len(p) < 4 {
		for i := range p {
			p[i] = 0
		}
		return len(p), nil
	}
	if s.muted != nil && *s.muted {
		for i := range p {
			p[i] = 0
		}
		time.Sleep(5 * time.Millisecond)
		return len(p), nil
	}
	// Each frame is 4 bytes (stereo int16). Block until the buffer is filled.
	want := len(p) / 4
	filled := 0
	// Short, responsive wait loop: return what we have instead of padding
	deadline := time.Now().Add(15 * time.Millisecond)
	for filled < want {
		frames := s.m.APUPullStereo(want - filled)
		if len(frames) == 0 {
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(1 * time.Millisecond)
			continue
		}
		// Convert pulled frames (interleaved) depending on mode
		i := filled * 4
		for j := 0; j+1 < len(frames) && i+3 < len(p); j += 2 {
			l := int16(frames[j])
			r := int16(frames[j+1])
			if s.mono {
				m := int16((int32(l) + int32(r)) / 2)
				binary.LittleEndian.PutUint16(p[i:], uint16(m))
				binary.LittleEndian.PutUint16(p[i+2:], uint16(m))
			} else {
				binary.LittleEndian.PutUint16(p[i:], uint16(l))
				binary.LittleEndian.PutUint16(p[i+2:], uint16(r))
			}
			i += 4
			filled++
		}
	}
	if filled < want {
		s.underruns++
		// pad remaining with silence to avoid stalling
		i := filled * 4
		for ; filled < want && i+3 < len(p); filled++ {
			binary.LittleEndian.PutUint16(p[i:], 0)
			binary.LittleEndian.PutUint16(p[i+2:], 0)
			i += 4
		}
		s.lastWant = want
		s.lastPulled = filled
		return want * 4, nil
	}
	s.lastWant = want
	s.lastPulled = filled
	return want * 4, nil
}

func (a *App) Draw(screen *ebiten.Image) {
	if a.tex == nil {
		a.tex = ebiten.NewImage(160, 144)
	}
	a.tex.WritePixels(a.m.Framebuffer())
	screen.DrawImage(a.tex, nil)

	// Stats overlay
	if a.showStats {
		bf := a.m.APUBufferedStereo()
		ms := (bf * 1000) / 48000 // ~ms of audio buffered at 48kHz
		und, lp, lw := 0, 0, 0
		if a.audioSrc != nil {
			und = a.audioSrc.underruns
			lp = a.audioSrc.lastPulled
			lw = a.audioSrc.lastWant
		}
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Buf: %d (~%dms)", bf, ms), 4, 4)
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Under: %d  Read: %d/%d", und, lp, lw), 4, 18)
	}

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
				fmt.Sprintf("Audio: %s", map[bool]string{true: "Stereo", false: "Mono"}[a.cfg.AudioStereo]),
				fmt.Sprintf("Audio Adaptive: %s", map[bool]string{true: "On", false: "Off"}[a.cfg.AudioAdaptive]),
				fmt.Sprintf("ROMs Dir: %s", a.truncateText(a.cfg.ROMsDir, a.maxCharsForText(10)-11)),
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
	// Resolve ROMsDir relative to the executable directory
	exe, _ := os.Executable()
	exedir := filepath.Dir(exe)
	roms := a.cfg.ROMsDir
	if !filepath.IsAbs(roms) {
		roms = filepath.Join(exedir, roms)
	}
	addFrom(roms)
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

// --- Settings persistence ---
func settingsPath() string {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	return filepath.Join(dir, "gbemu_settings.json")
}

func loadSettings(override Config) Config {
	var cfg Config
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	// override non-zero fields from param
	if override.Title != "" {
		cfg.Title = override.Title
	}
	if override.Scale != 0 {
		cfg.Scale = override.Scale
	}
	if override.AudioBufferMs != 0 {
		cfg.AudioBufferMs = override.AudioBufferMs
	}
	if override.ROMsDir != "" {
		cfg.ROMsDir = override.ROMsDir
	}
	cfg.AudioStereo = override.AudioStereo || cfg.AudioStereo
	cfg.AudioAdaptive = override.AudioAdaptive || cfg.AudioAdaptive
	if cfg.Title == "" && override.Title == "" {
		cfg.Title = "gbemu"
	}
	return cfg
}

func (a *App) saveSettings() {
	if a == nil {
		return
	}
	b, _ := json.MarshalIndent(a.cfg, "", "  ")
	_ = os.WriteFile(settingsPath(), b, 0644)
}

// --- Save states (per-ROM, per-slot) ---
func (a *App) statePath(slot int) string {
	// State file: <ROMName>.slot<slot>.savestate in same dir as ROM
	base := "unknown"
	if a.m != nil && a.m.ROMPath() != "" {
		base = a.m.ROMPath()
	}
	dir := filepath.Dir(base)
	name := filepath.Base(base)
	return filepath.Join(dir, fmt.Sprintf("%s.slot%d.savestate", name, slot))
}

func (a *App) saveSlot(slot int) error {
	path := a.statePath(slot)
	return a.m.SaveStateToFile(path)
}

func (a *App) loadSlot(slot int) error {
	path := a.statePath(slot)
	return a.m.LoadStateFromFile(path)
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
