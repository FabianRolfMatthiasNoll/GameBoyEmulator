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

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/emu"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

type App struct {
	cfg     Config
	m       *emu.Machine
	tex     *ebiten.Image
	paused  bool
	fast    bool
	turbo   int  // turbo speed multiplier (1=off)
	skipOn  bool // whether to skip rendering frames
	skipN   int  // render 1 of (skipN+1) frames
	skipCtr int  // counter for frame skip
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

	// save-state slot management
	currentSlot int // 0..9

	// rom picker state
	romList []string
	romSel  int
	romOff  int // scroll offset for ROM list

	// keybindings state
	keysOff int // scroll offset for keybindings

	// settings edit state
	editingROMDir bool
	romDirInput   string
	settingsOff   int // scroll offset for settings list

	// toast feedback
	toastMsg   string
	toastUntil time.Time

	// overlay skin
	shellImg  *ebiten.Image
	shellList []string
	shellIdx  int

	// current logical screen size
	curW int
	curH int
}

func NewApp(cfg Config, m *emu.Machine) *App {
	// Load settings from file if present, then apply defaults and merge
	cfg = loadSettings(cfg)
	cfg.Defaults()
	ebiten.SetWindowTitle(cfg.Title)
	ebiten.SetWindowSize(160*cfg.Scale, 144*cfg.Scale)
	a := &App{cfg: cfg, m: m}
	a.curW, a.curH = 160, 144
	a.lastTime = time.Now()
	a.frameAcc = 0
	a.turbo = 1
	a.skipOn = false
	a.skipN = 0
	a.skipCtr = 0
	// Init audio at 48kHz to match APU
	a.audioCtx = audio.NewContext(48000)
	// Configure adaptive buffering and initial target
	if cfg.AudioBufferMs <= 0 {
		cfg.AudioBufferMs = 125
	}
	a.targetFrames = (cfg.AudioBufferMs * 48000) / 1000
	a.stableTicks = 0
	// Defer creating the player until Update runs to ensure window init isn't blocked
	// If no ROM is loaded yet by the machine, open the ROM picker automatically
	if m != nil && m.ROMPath() == "" {
		a.showMenu = true
		a.menuMode = "rom"
		a.menuIdx = 0
		a.romList = a.findROMs()
		a.romSel = 0
		a.romOff = 0
	}
	// If a ROM is already loaded, include its title in the window title
	if m != nil && m.ROMPath() != "" {
		title := cfg.Title
		if t := m.ROMTitle(); t != "" {
			title = cfg.Title + " - [" + t + "]"
		}
		ebiten.SetWindowTitle(title)
	}
	// default save-state slot
	a.currentSlot = 0
	// init ROM dir input for editing
	a.romDirInput = cfg.ROMsDir
	a.settingsOff = 0
	// Propagate rendering config into emulator
	if m != nil {
		m.SetUseFetcherBG(a.cfg.UseFetcherBG)
	}
	// discover available skins and align selected index
	a.shellList = a.findSkins()
	a.shellIdx = 0
	for i, p := range a.shellList {
		if filepath.Clean(p) == filepath.Clean(a.cfg.ShellImage) {
			a.shellIdx = i
			break
		}
	}
	if a.cfg.ShellOverlay {
		a.loadShell()
		a.applyWindowSize()
	}
	return a
}

func (a *App) Run() error { return ebiten.RunGame(a) }

// SaveSettings persists current settings to disk.
func (a *App) SaveSettings() { a.saveSettings() }

func (a *App) Update() error {
	// Lazy-create audio player on first update to avoid startup blocking before the window appears
	if a.audioPlayer == nil {
		// Safe init: create audio player but start muted initially to avoid first-frame stalls
		a.audioMuted = true
		a.m.APUClearAudioLatency()
		a.audioSrc = &apuStream{m: a.m, mono: !a.cfg.AudioStereo, muted: &a.audioMuted, lowLatency: a.cfg.AudioLowLatency}
		if p, err := a.audioCtx.NewPlayer(a.audioSrc); err == nil {
			a.audioPlayer = p
			a.applyPlayerBufferSize()
			a.audioPlayer.Play()
		}
		// Unmute after a few update ticks once frames are flowing
		// (we toggle below when target buffer has some frames)
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
	// Fast-forward (Tab)
	prevFast := a.fast
	a.fast = ebiten.IsKeyPressed(ebiten.KeyTab)
	// Turbo controls: F6/F7 adjust multiplier; F4 toggles frame-skip
	if inpututil.IsKeyJustPressed(ebiten.KeyF6) {
		if a.turbo > 1 {
			a.turbo--
		}
		a.toast(fmt.Sprintf("Turbo: x%d", a.turbo))
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF7) {
		if a.turbo < 10 {
			a.turbo++
		}
		a.toast(fmt.Sprintf("Turbo: x%d", a.turbo))
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF4) {
		a.skipOn = !a.skipOn
		a.toast(fmt.Sprintf("Frame skip: %v", map[bool]string{true: "On", false: "Off"}[a.skipOn]))
	}
	// Resets
	if inpututil.IsKeyJustPressed(ebiten.KeyR) {
		a.m.ResetPostBoot()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyB) {
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
	// Fullscreen toggle (F11)
	if inpututil.IsKeyJustPressed(ebiten.KeyF11) {
		ebiten.SetFullscreen(!ebiten.IsFullscreen())
	}
	// Quick toggle shell overlay (F10)
	if inpututil.IsKeyJustPressed(ebiten.KeyF10) {
		a.cfg.ShellOverlay = !a.cfg.ShellOverlay
		if a.cfg.ShellOverlay {
			a.loadShell()
		}
		a.applyWindowSize()
		a.saveSettings()
		state := map[bool]string{true: "On", false: "Off"}[a.cfg.ShellOverlay]
		a.toast("Shell Overlay: " + state)
	}
	// Quick slots (1..4) and quick save/load (F5/F9)
	// number keys 1..4 map to slots 1..4
	if inpututil.IsKeyJustPressed(ebiten.Key1) {
		a.currentSlot = 0
		a.toast("Slot set to 1")
	}
	if inpututil.IsKeyJustPressed(ebiten.Key2) {
		a.currentSlot = 1
		a.toast("Slot set to 2")
	}
	if inpututil.IsKeyJustPressed(ebiten.Key3) {
		a.currentSlot = 2
		a.toast("Slot set to 3")
	}
	if inpututil.IsKeyJustPressed(ebiten.Key4) {
		a.currentSlot = 3
		a.toast("Slot set to 4")
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF5) {
		if err := a.saveSlot(a.currentSlot); err == nil {
			a.toast(fmt.Sprintf("Saved slot %d", a.currentSlot+1))
		} else {
			a.toast("Save failed: " + err.Error())
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF9) {
		if _, err := os.Stat(a.statePath(a.currentSlot)); err != nil {
			a.toast("Slot is empty")
		} else {
			if err := a.loadSlot(a.currentSlot); err == nil {
				a.toast(fmt.Sprintf("Loaded slot %d", a.currentSlot+1))
			} else {
				a.toast("Load failed: " + err.Error())
			}
		}
	}

	// Apply mute when paused or menu shown; reset pacing on transitions
	muted := a.paused || a.showMenu
	if muted != a.audioMuted {
		a.audioMuted = muted
		a.lastTime = time.Now()
		a.frameAcc = 0
		// When (un)muting, drop buffered audio to avoid stale playback
		if a.m != nil {
			a.m.APUClearAudioLatency()
		}
	}

	// If entering fast-forward, cap audio buffer so it doesn't lag; on exit, clear to resync
	if a.m != nil && prevFast != a.fast {
		if a.fast {
			// Trim to ~40ms at 48kHz (~1920 frames)
			a.m.APUCapBufferedStereo(1920)
			// tighten player buffer during fast-forward
			a.applyPlayerBufferSize()
		} else {
			// Leaving fast-forward: drop buffered audio to resync with video/input
			a.m.APUClearAudioLatency()
			a.applyPlayerBufferSize()
		}
	}

	if a.showMenu {
		switch a.menuMode {
		case "main":
			max := 6
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.menuIdx > 0 {
				a.menuIdx--
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) && a.menuIdx < max {
				a.menuIdx++
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
				switch a.menuIdx {
				case 0:
					if err := a.saveSlot(a.currentSlot); err == nil {
						a.toast(fmt.Sprintf("Saved slot %d", a.currentSlot+1))
					} else {
						a.toast("Save failed: " + err.Error())
					}
				case 1:
					if _, err := os.Stat(a.statePath(a.currentSlot)); err != nil {
						a.toast("Slot is empty")
					} else {
						if err := a.loadSlot(a.currentSlot); err == nil {
							a.toast(fmt.Sprintf("Loaded slot %d", a.currentSlot+1))
						} else {
							a.toast("Load failed: " + err.Error())
						}
					}
				case 2:
					a.menuMode = "slot"
					a.menuIdx = a.currentSlot
				case 3:
					a.romList = a.findROMs()
					a.romSel = 0
					a.romOff = 0
					a.menuMode = "rom"
				case 4:
					a.menuMode = "settings"
					a.menuIdx = 0
					a.editingROMDir = false
				case 5:
					a.menuMode = "keys"
					a.keysOff = 0
				case 6:
					a.showMenu = false
				}
			}
			// Back with Backspace
			if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
				a.showMenu = false
			}
		case "slot":
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.menuIdx > 0 {
				a.menuIdx--
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) && a.menuIdx < 3 {
				a.menuIdx++
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
				a.currentSlot = a.menuIdx
				a.toast(fmt.Sprintf("Slot set to %d", a.currentSlot+1))
				a.menuMode = "main"
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
				a.menuMode = "main"
			}
		case "rom":
			n := len(a.romList)
			if n == 0 {
				if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
					a.menuMode = "main"
				}
			} else {
				// compute window
				baseY := 28
				_ = baseY // draw uses same baseline; keep logic consistent
				maxRows := (a.curH - baseY) / 14
				if maxRows < 1 {
					maxRows = 1
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.romSel > 0 {
					a.romSel--
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) && a.romSel < n-1 {
					a.romSel++
				}
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
						if strings.HasSuffix(strings.ToLower(path), ".gb") {
							sav := strings.TrimSuffix(path, ".gb") + ".sav"
							if data, err := os.ReadFile(sav); err == nil {
								_ = a.m.LoadBattery(data)
							}
						}
						// If user has CGB Colors toggled for a DMG ROM, restart into CGB compat now
						if a.m.WantCGBColors() && !a.m.UseCGBBG() {
							a.m.ResetCGBPostBoot(true)
						}
						// Update window title with game title
						title := a.cfg.Title
						if t := a.m.ROMTitle(); t != "" {
							title = a.cfg.Title + " - [" + t + "]"
						}
						ebiten.SetWindowTitle(title)
						// Apply saved per-ROM palette preference, if any
						if a.m.IsCGBCompat() && a.cfg.PerROMCompatPalette != nil {
							if pid, ok := a.cfg.PerROMCompatPalette[path]; ok {
								a.m.SetCompatPalette(pid)
							}
						}
					} else {
						a.toast("ROM load failed: " + err.Error())
					}
					a.menuMode = "main"
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
					a.menuMode = "main"
				}
			}
		case "keys":
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.keysOff > 0 {
				a.keysOff--
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) {
				a.keysOff++
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
				a.menuMode = "main"
			}
		case "settings":
			// Items order:
			// 0 Scale
			// 1 Audio
			// 2 Audio Adaptive
			// 3 Low-Latency
			// 4 BG Renderer
			// 5 ROMs Dir
			// 6 CGB Colors
			// 7 Compat Palette (if compat)
			// 8 Shell Overlay
			// 9 Shell Skin
			hasCompat := a.m != nil && a.m.IsCGBCompat()
			items := 9
			if hasCompat {
				items = 10
			}
			if !a.editingROMDir { // normal navigation when not editing
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.menuIdx > 0 {
					a.menuIdx--
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) && a.menuIdx < items-1 {
					a.menuIdx++
				}
				// maintain scroll window
				title := "Settings (Up/Down select; Left/Right change; Enter: edit/apply; Backspace/Esc: back)"
				baseY := 10 + 14*len(a.wrapText(title, a.maxCharsForText(10))) + 14
				maxRows := (a.curH - baseY) / 14
				if maxRows < 1 {
					maxRows = 1
				}
				if a.menuIdx < a.settingsOff {
					a.settingsOff = a.menuIdx
				}
				if a.menuIdx >= a.settingsOff+maxRows {
					a.settingsOff = a.menuIdx - maxRows + 1
				}
			}
			if a.menuIdx == 0 && !a.editingROMDir { // Scale
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
					if a.cfg.Scale > 1 {
						a.cfg.Scale--
						a.applyWindowSize()
					}
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
					if a.cfg.Scale < 10 {
						a.cfg.Scale++
						a.applyWindowSize()
					}
				}
			} else if a.menuIdx == 1 && !a.editingROMDir { // Audio Output
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
					a.cfg.AudioStereo = !a.cfg.AudioStereo
					if a.audioPlayer != nil {
						a.audioPlayer.Close()
						a.audioPlayer = nil
					}
					for i := 0; i < 12; i++ {
						a.m.StepFrame()
					}
					a.audioSrc = &apuStream{m: a.m, mono: !a.cfg.AudioStereo, muted: &a.audioMuted, lowLatency: a.cfg.AudioLowLatency}
					if p, err := a.audioCtx.NewPlayer(a.audioSrc); err == nil {
						a.audioPlayer = p
						a.applyPlayerBufferSize()
						a.audioPlayer.Play()
					}
				}
			} else if a.menuIdx == 2 && !a.editingROMDir { // Audio Adaptive
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
					a.cfg.AudioAdaptive = !a.cfg.AudioAdaptive
				}
			} else if a.menuIdx == 3 && !a.editingROMDir { // Low-Latency Audio
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) || inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
					a.cfg.AudioLowLatency = !a.cfg.AudioLowLatency
					a.saveSettings()
					// When turning on low-latency, immediately trim buffered audio
					if a.m != nil && a.cfg.AudioLowLatency {
						a.m.APUCapBufferedStereo(1440) // ~30ms
					}
					if a.audioSrc != nil {
						a.audioSrc.lowLatency = a.cfg.AudioLowLatency
					}
					a.applyPlayerBufferSize()
				}
			} else if a.menuIdx == 4 && !a.editingROMDir { // BG Renderer
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) || inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
					a.cfg.UseFetcherBG = !a.cfg.UseFetcherBG
					if a.m != nil {
						a.m.SetUseFetcherBG(a.cfg.UseFetcherBG)
					}
					a.saveSettings()
				}
			} else if a.menuIdx == 5 { // ROMs Dir edit mode
				if !a.editingROMDir {
					if inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
						a.editingROMDir = true
						a.romDirInput = a.cfg.ROMsDir
					}
					if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
						a.menuMode = "main"
					}
				} else {
					// editing: collect typed characters
					for _, r := range ebiten.InputChars() {
						if r != '\n' && r != '\r' {
							a.romDirInput += string(r)
						}
					}
					if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) && len(a.romDirInput) > 0 {
						a.romDirInput = a.romDirInput[:len(a.romDirInput)-1]
					}
					if inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
						val := strings.TrimSpace(a.romDirInput)
						if val != "" {
							a.cfg.ROMsDir = val
							a.saveSettings()
							a.romList = a.findROMs()
							a.toast("ROMs dir set")
						}
						a.editingROMDir = false
					}
					if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
						a.editingROMDir = false
						a.romDirInput = a.cfg.ROMsDir
					}
				}
			} else if a.menuIdx == 6 && !a.editingROMDir { // CGB Colors toggle
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) || inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
					if a.m != nil {
						turnOn := !a.m.WantCGBColors()
						if turnOn {
							// Enable CGB colors. If the ROM is DMG-only, enter CGB compatibility mode with a clean reset.
							a.m.SetUseCGBBG(true)
							if a.m.IsCGBCompat() {
								a.m.ResetCGBPostBoot(true)
							}
						} else {
							// Turn off: leave compat mode and return to DMG post-boot.
							a.m.SetUseCGBBG(false)
							a.m.ResetPostBoot()
						}
					}
				}
			} else if a.menuIdx == 7 && hasCompat && !a.editingROMDir { // Compat Palette row
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
					a.m.CycleCompatPalette(-1)
					pid := a.m.CurrentCompatPalette()
					a.toast(fmt.Sprintf("Compat palette: %d - %s", pid, a.m.CompatPaletteName(pid)))
					// persist per-ROM palette
					if a.m.ROMPath() != "" {
						a.cfg.PerROMCompatPalette[a.m.ROMPath()] = pid
						a.saveSettings()
					}
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) || inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
					a.m.CycleCompatPalette(+1)
					pid := a.m.CurrentCompatPalette()
					a.toast(fmt.Sprintf("Compat palette: %d - %s", pid, a.m.CompatPaletteName(pid)))
					// persist per-ROM palette
					if a.m.ROMPath() != "" {
						a.cfg.PerROMCompatPalette[a.m.ROMPath()] = pid
						a.saveSettings()
					}
				}
			} else if a.menuIdx == 8 && !a.editingROMDir { // Shell Overlay toggle
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) || inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
					a.cfg.ShellOverlay = !a.cfg.ShellOverlay
					if a.cfg.ShellOverlay {
						a.loadShell()
					}
					a.applyWindowSize()
					a.saveSettings()
				}
			} else if a.menuIdx == 9 && !a.editingROMDir { // Shell Skin select
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
					if len(a.shellList) > 0 {
						a.shellIdx = (a.shellIdx - 1 + len(a.shellList)) % len(a.shellList)
						a.cfg.ShellImage = a.shellList[a.shellIdx]
						a.shellImg = nil // force reload
						a.loadShell()
						a.applyWindowSize()
						a.saveSettings()
						a.toast("Skin: " + filepath.Base(a.cfg.ShellImage))
					}
				}
				if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) || inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
					if len(a.shellList) > 0 {
						a.shellIdx = (a.shellIdx + 1) % len(a.shellList)
						a.cfg.ShellImage = a.shellList[a.shellIdx]
						a.shellImg = nil
						a.loadShell()
						a.applyWindowSize()
						a.saveSettings()
						a.toast("Skin: " + filepath.Base(a.cfg.ShellImage))
					}
				}
			}
			// back to main from settings when not editing
			if !a.editingROMDir && (inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyBackspace)) {
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

	// In DMG-on-CGB compatibility mode, allow quick palette cycling with [ and ]
	if a.m != nil && a.m.IsCGBCompat() {
		if inpututil.IsKeyJustPressed(ebiten.KeyBracketLeft) {
			a.m.CycleCompatPalette(-1)
			pid := a.m.CurrentCompatPalette()
			a.toast(fmt.Sprintf("Compat palette: %d - %s", pid, a.m.CompatPaletteName(pid)))
			if a.m.ROMPath() != "" {
				a.cfg.PerROMCompatPalette[a.m.ROMPath()] = pid
				a.saveSettings()
			}
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyBracketRight) {
			a.m.CycleCompatPalette(+1)
			pid := a.m.CurrentCompatPalette()
			a.toast(fmt.Sprintf("Compat palette: %d - %s", pid, a.m.CompatPaletteName(pid)))
			if a.m.ROMPath() != "" {
				a.cfg.PerROMCompatPalette[a.m.ROMPath()] = pid
				a.saveSettings()
			}
		}
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
			speed = float64(max(2, a.turbo))
		}
		a.frameAcc += dt * gbFps * speed
		// Step whole frames
		steps := 0
		for a.frameAcc >= 1.0 && steps < 10 { // cap to avoid spiral of death
			// Optional frame skip: advance emulation without rendering for skipped frames
			doRender := true
			if a.skipOn {
				if a.skipCtr < a.skipN {
					doRender = false
					a.skipCtr++
				} else {
					a.skipCtr = 0
				}
			}
			if doRender {
				a.m.StepFrame()
			} else {
				a.m.StepFrameNoRender()
			}
			a.frameAcc -= 1.0
			steps++
		}
		// Adaptive audio buffer target: raise on underruns, decay slowly when stable
		if a.cfg.AudioAdaptive && a.audioSrc != nil && !a.cfg.AudioLowLatency {
			// Clamp upper bound to ~200ms to avoid large inherent lag
			maxFrames := 48000 * 200 / 1000 // ~9600
			if a.targetFrames > maxFrames {
				a.targetFrames = maxFrames
			}
			if a.audioSrc.underruns > 0 {
				a.stableTicks = 0
				if a.targetFrames < maxFrames {
					a.targetFrames += 800
					if a.targetFrames > maxFrames {
						a.targetFrames = maxFrames
					}
				}
				a.audioSrc.underruns = 0
			} else {
				a.stableTicks++
				if a.stableTicks > 90 { // decay a bit faster
					minFrames := 48000 * 40 / 1000 // ~40ms
					if a.targetFrames > minFrames {
						a.targetFrames -= 400
						if a.targetFrames < minFrames {
							a.targetFrames = minFrames
						}
					}
					a.stableTicks = 0
				}
			}
		}
		// Audio buffer target and trimming
		target := a.targetFrames
		// Low-latency mode enforces a small, fixed target and trims any excess
		if a.cfg.AudioLowLatency {
			target = 48000 * 35 / 1000 // ~35ms
		}
		// When fast-forwarding, keep latency small by lowering target further
		if a.fast {
			ffTarget := 48000 * 30 / 1000 // ~30ms while fast-forwarding
			if target > ffTarget {
				target = ffTarget
			}
		}
		buffered := a.m.APUBufferedStereo()
		// If we started muted and we have some audio buffered, unmute now
		if a.audioMuted && buffered > 1024 { // ~20ms
			a.audioMuted = false
		}
		// Trim if buffer runs away while in low-latency mode
		if a.cfg.AudioLowLatency {
			ceiling := target + 48000*10/1000 // target +10ms
			if buffered > ceiling {
				a.m.APUCapBufferedStereo(ceiling)
			}
		}
	}

	return nil
}

func (a *App) Draw(screen *ebiten.Image) {
	// Optional shell overlay drawn first as background
	if a.cfg.ShellOverlay {
		if a.shellImg == nil {
			a.loadShell()
		}
		if a.shellImg != nil {
			screen.DrawImage(a.shellImg, nil)
		}
	}

	// Draw the 160x144 game framebuffer; center it within current logical screen
	oW, oH := screen.Bounds().Dx(), screen.Bounds().Dy()
	if a.tex == nil {
		a.tex = ebiten.NewImage(160, 144)
	}
	a.tex.WritePixels(a.m.Framebuffer())
	var op ebiten.DrawImageOptions
	dx := (oW - 160) / 2
	dy := (oH - 144) / 2
	if dx < 0 {
		dx = 0
	}
	if dy < 0 {
		dy = 0
	}
	op.GeoM.Translate(float64(dx), float64(dy))
	screen.DrawImage(a.tex, &op)

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
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Turbo: x%d  Skip: %v", a.turbo, a.skipOn), 4, 32)
	}

	// Toast message
	if a.toastMsg != "" && time.Now().Before(a.toastUntil) {
		msg := a.toastMsg
		msg = a.truncateText(msg, a.maxCharsForText(6))
		ebitenutil.DebugPrintAt(screen, msg, 6, 4)
	}

	if a.showMenu {
		overlay := ebiten.NewImage(oW, oH)
		overlay.Fill(color.RGBA{0, 0, 0, 140})
		screen.DrawImage(overlay, nil)
		switch a.menuMode {
		case "main":
			a.drawMainMenu(screen)
		case "slot":
			a.drawSlotMenu(screen)
		case "rom":
			a.drawRomMenu(screen)
		case "keys":
			a.drawKeysMenu(screen)
		case "settings":
			a.drawSettingsMenu(screen)
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
			ln := strings.ToLower(name)
			if strings.HasSuffix(ln, ".gb") || strings.HasSuffix(ln, ".gbc") {
				files = append(files, filepath.Join(dir, name))
			}
		}
	}
	// Resolve ROMsDir: if absolute, use it; if relative, try both exe-relative and CWD-relative
	exe, _ := os.Executable()
	exedir := filepath.Dir(exe)
	roms := a.cfg.ROMsDir
	if filepath.IsAbs(roms) {
		addFrom(roms)
	} else {
		addFrom(filepath.Join(exedir, roms))
		addFrom(roms) // relative to current working directory
	}
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
	// Prefer user config dir (e.g., %AppData%/gbemu) for persistence
	if dir, err := os.UserConfigDir(); err == nil {
		d := filepath.Join(dir, "gbemu")
		_ = os.MkdirAll(d, 0755)
		return filepath.Join(d, "settings.json")
	}
	// Fallback to executable directory
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "gbemu_settings.json")
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
	cfg.AudioLowLatency = override.AudioLowLatency || cfg.AudioLowLatency
	// boolean override for UseFetcherBG if explicitly set true via code or defaults file
	if override.UseFetcherBG {
		cfg.UseFetcherBG = true
	}
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

func (a *App) Layout(outW, outH int) (int, int) {
	if a != nil && a.cfg.ShellOverlay {
		if a.shellImg == nil {
			a.loadShell()
		}
		if a.shellImg != nil {
			if w, h := a.shellImg.Size(); w > 0 && h > 0 {
				a.curW, a.curH = w, h
				return w, h
			}
		}
	}
	a.curW, a.curH = 160, 144
	return 160, 144
}

// maxCharsForText estimates how many characters fit on a line starting at left margin x.
// This uses a conservative ~6px per character for the debug font.
func (a *App) maxCharsForText(left int) int {
	w := a.curW - left - 4
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

// applyWindowSize recalculates the window size depending on overlay presence.
// When overlay is enabled and larger than 160x144, the window scales the overlay by cfg.Scale; otherwise scales the game area.
func (a *App) applyWindowSize() {
	if a == nil {
		return
	}
	baseW, baseH := 160, 144
	if a.cfg.ShellOverlay && a.shellImg != nil {
		w, h := a.shellImg.Size()
		if w > 0 && h > 0 {
			baseW, baseH = w, h
		}
	}
	ebiten.SetWindowSize(baseW*a.cfg.Scale, baseH*a.cfg.Scale)
}

// findSkins searches common skin folders for .png files and returns absolute paths.
func (a *App) findSkins() []string {
	var out []string
	addPngs := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			if strings.HasSuffix(name, ".png") {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	exe, _ := os.Executable()
	exedir := filepath.Dir(exe)
	// default skins location in repo
	addPngs(filepath.Join(exedir, "assets", "skins"))
	// also try CWD-relative
	addPngs(filepath.Join("assets", "skins"))
	// De-dup
	sort.Strings(out)
	uniq := out[:0]
	seen := map[string]bool{}
	for _, p := range out {
		cp := filepath.Clean(p)
		if seen[cp] {
			continue
		}
		seen[cp] = true
		uniq = append(uniq, cp)
	}
	return uniq
}

// loadShell loads the configured shell image into memory and adjusts window size.
func (a *App) loadShell() {
	a.shellImg = nil
	openAndDecode := func(path string) (*ebiten.Image, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		img, err := png.Decode(f)
		if err != nil {
			return nil, err
		}
		return ebiten.NewImageFromImage(img), nil
	}
	// Try as-is (relative to CWD or absolute)
	if img, err := openAndDecode(a.cfg.ShellImage); err == nil {
		a.shellImg = img
		return
	}
	// Fallback to exe-relative
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), a.cfg.ShellImage)
		if img, err2 := openAndDecode(p); err2 == nil {
			a.shellImg = img
			return
		}
	}
}
