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
	cfg    Config
	m      *emu.Machine
	tex    *ebiten.Image
	shader *ebiten.Shader // active shader
	// track current preset to recompile when it changes
	shaderPreset string
	paused       bool
	fast         bool
	turbo        int  // turbo speed multiplier (1=off)
	skipOn       bool // whether to skip rendering frames
	skipN        int  // render 1 of (skipN+1) frames
	skipCtr      int  // counter for frame skip
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

	// shader resources
	ghostTex *ebiten.Image // previous frame for ghosting

	// jitter state
	drawCount int

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
	// Precompile shader if a preset is on
	a.ensureShader()
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
			a.updateMainMenu()
		case "slot":
			a.updateSlotMenu()
		case "rom":
			a.updateRomMenu()
		case "keys":
			a.updateKeysMenu()
		case "settings":
			a.updateSettingsMenu()
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
	dx := (oW - 160) / 2
	dy := (oH - 144) / 2
	// Optional subtle vertical jitter for retro feel
	jitterOffset := 0.0
	if a.cfg.Jitter {
		// Every ~2 seconds, nudge for one frame by 0.5px
		a.drawCount++
		if a.drawCount%120 == 0 { // assuming ~60fps
			jitterOffset = 0.5
		}
	}
	if dx < 0 {
		dx = 0
	}
	if dy < 0 {
		dy = 0
	}
	useShader := a.cfg.ShaderPreset != "off"
	if useShader {
		// Ensure shader is ready
		a.ensureShader()
		if a.shader != nil {
			// Draw with shader: pass the game image and optional previous frame
			op := &ebiten.DrawRectShaderOptions{}
			op.GeoM.Translate(float64(dx), float64(dy)+jitterOffset)
			op.Images[0] = a.tex
			if a.cfg.ShaderPreset == "ghost" && a.ghostTex != nil {
				op.Images[1] = a.ghostTex
			}
			// uniforms may be added later for tuning
			screen.DrawRectShader(160, 144, a.shader, op)
			// Update previous-frame buffer for ghosting
			if a.cfg.ShaderPreset == "ghost" {
				if a.ghostTex == nil {
					a.ghostTex = ebiten.NewImage(160, 144)
				}
				a.ghostTex.DrawImage(a.tex, nil)
			}
		} else {
			var op ebiten.DrawImageOptions
			op.GeoM.Translate(float64(dx), float64(dy)+jitterOffset)
			screen.DrawImage(a.tex, &op)
		}
	} else {
		var op ebiten.DrawImageOptions
		op.GeoM.Translate(float64(dx), float64(dy)+jitterOffset)
		screen.DrawImage(a.tex, &op)
	}

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

// Simple LCD-like shader with a subtle grid and subpixel mask.
// Unit is pixels so srcPos is in pixel space.
const lcdShaderSrc = `
//kage:unit pixels

package main

// imageSrc0 is the game texture

func mod(a, b float) float {
	return a - b*floor(a/b)
}

func Fragment(position vec4, srcPos vec2, color vec4) vec4 {
	// Base color from source
	c := imageSrc0At(srcPos)
	// Apply a light pixel grid: darker every pixel boundary
	gx := mod(srcPos.x, 1.0)
	gy := mod(srcPos.y, 1.0)
	line := 0.0
	if gx < 0.03 || gy < 0.03 { // thin lines
		line = 0.10
	}
	// Subpixel mask: RGB stripes across x
	stripe := mod(floor(srcPos.x), 3.0)
	mask := vec3(0.95,0.95,0.95)
	if stripe == 0.0 {
		mask = vec3(1.00,0.92,0.92)
	} else if stripe == 1.0 {
		mask = vec3(0.92,1.00,0.92)
	} else {
		mask = vec3(0.92,0.92,1.00)
	}
	rgb := c.rgb * mask * (1.0 - line)
	// Very subtle vignette to reduce hard edges
	sz := imageSrc0Size()
	uv := srcPos / sz
	d := distance(uv, vec2(0.5, 0.5))
	vignette := 1.0 - 0.04*pow(d/0.707, 1.5)
	rgb *= vignette
	return vec4(rgb, c.a)
}
`

// CRT-like shader: scanlines + slight curvature + subtle bloom
const crtShaderSrc = `
//kage:unit pixels
package main

func mod(a, b float) float {
	return a - b*floor(a/b)
}

func Fragment(position vec4, srcPos vec2, color vec4) vec4 {
	c := imageSrc0At(srcPos)
	// scanlines every other row
	if mod(floor(srcPos.y), 2.0) == 0.0 {
		c.rgb *= 0.80
	}
	// very subtle barrel distortion vignette
	sz := imageSrc0Size()
	uv := (srcPos / sz) * 2.0 - 1.0
	r2 := dot(uv, uv)
	vignette := 1.0 - 0.10*r2
	c.rgb *= vignette
	// gentle bloom by sampling 4 neighbors
	off := vec2(1.0, 1.0)
	s := imageSrc0At(srcPos + vec2(1.0,0.0)).rgb + imageSrc0At(srcPos + vec2(-1.0,0.0)).rgb +
		 imageSrc0At(srcPos + vec2(0.0,1.0)).rgb + imageSrc0At(srcPos + vec2(0.0,-1.0)).rgb
	c.rgb = mix(c.rgb, s/4.0, 0.15)
	return c
}
`

// Ghosting shader: blend current with previous frame to simulate LCD persistence/ghosting
const ghostShaderSrc = `
//kage:unit pixels
package main

func Fragment(position vec4, srcPos vec2, color vec4) vec4 {
	cur := imageSrc0At(srcPos)
	prev := imageSrc1At(srcPos)
	// Blend with previous frame
	rgb := mix(cur.rgb, prev.rgb, 0.45)
	return vec4(rgb, cur.a)
}
`

// Dot-matrix shader: emulate DMG dot matrix with circular mask and light neighbor bleed
const dotShaderSrc = `
//kage:unit pixels
package main

func mod(a, b float) float { return a - b*floor(a/b) }

func Fragment(position vec4, srcPos vec2, color vec4) vec4 {
	c := imageSrc0At(srcPos)
	// 2x2 Bayer-ish dither mask based on pixel indices
	ix := floor(srcPos.x)
	iy := floor(srcPos.y)
	parity := mod(ix+iy, 2.0)
	mask := 1.0
	if parity == 0.0 {
		mask = 0.85 // darken every other pixel to simulate dot pattern
	}
	// neighbor bleed for a bit of diffusion
	nb := (imageSrc0At(srcPos + vec2(1.0,0.0)).rgb + imageSrc0At(srcPos + vec2(-1.0,0.0)).rgb +
		   imageSrc0At(srcPos + vec2(0.0,1.0)).rgb + imageSrc0At(srcPos + vec2(0.0,-1.0)).rgb) / 4.0
	rgb := mix(c.rgb, nb, 0.10) * mask
	return vec4(rgb, c.a)
}
`

// ensureShader compiles/selects the active shader based on preset.
func (a *App) ensureShader() {
	preset := strings.ToLower(a.cfg.ShaderPreset)
	// Back-compat: if legacy flag set, prefer lcd
	if preset == "" {
		if a.cfg.LCDShader {
			preset = "lcd"
		} else {
			preset = "off"
		}
	}
	// If off, release shader
	if preset == "off" {
		a.shader = nil
		a.shaderPreset = "off"
		return
	}
	// Determine current shader source by preset
	var src string
	switch preset {
	case "lcd":
		src = lcdShaderSrc
	case "crt":
		src = crtShaderSrc
	case "ghost":
		src = ghostShaderSrc
	case "dot":
		src = dotShaderSrc
	default:
		src = lcdShaderSrc
	}
	// If no shader or switching preset, (re)compile
	if a.shader == nil || a.shaderPreset != preset {
		if s, err := ebiten.NewShader([]byte(src)); err == nil {
			a.shader = s
			a.shaderPreset = preset
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
