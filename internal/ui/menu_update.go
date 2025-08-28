package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

func (a *App) updateMainMenu() {
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
}

func (a *App) updateSlotMenu() {
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
}

func (a *App) updateRomMenu() {
	n := len(a.romList)
	if n == 0 {
		if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
			a.menuMode = "main"
		}
		return
	}
	// compute window to maintain selection visibility
	baseY := 28
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

func (a *App) updateKeysMenu() {
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && a.keysOff > 0 {
		a.keysOff--
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) {
		a.keysOff++
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
		a.menuMode = "main"
	}
}

func (a *App) updateSettingsMenu() {
	// Items order:
	// 0 Scale
	// 1 Audio
	// 2 Audio Adaptive
	// 3 Low-Latency
	// 4 BG Renderer
	// 5 Shader Preset
	// 6 ROMs Dir
	// 7 CGB Colors
	// 8 Compat Palette (if compat)
	// 9 Shell Overlay
	// 10 Shell Skin
	hasCompat := a.m != nil && a.m.IsCGBCompat()
	items := 10
	if hasCompat {
		items = 11
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
	} else if a.menuIdx == 5 && !a.editingROMDir { // Shader preset cycle
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) || inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
			presets := []string{"off","lcd","crt","ghost"}
			// find current index
			idx := 0
			for i, p := range presets {
				if strings.ToLower(a.cfg.ShaderPreset) == p {
					idx = i
					break
				}
			}
			if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
				idx = (idx - 1 + len(presets)) % len(presets)
			} else {
				idx = (idx + 1) % len(presets)
			}
			a.cfg.ShaderPreset = presets[idx]
			// reset/compile shader accordingly
			a.shader = nil
			a.ensureShader()
			a.saveSettings()
		}
	} else if a.menuIdx == 6 { // ROMs Dir edit mode
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
	} else if a.menuIdx == 7 && !a.editingROMDir { // CGB Colors toggle
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
	} else if a.menuIdx == 8 && hasCompat && !a.editingROMDir { // Compat Palette row
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
	} else if a.menuIdx == 9 && !a.editingROMDir { // Shell Overlay toggle
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) || inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) || inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
			a.cfg.ShellOverlay = !a.cfg.ShellOverlay
			if a.cfg.ShellOverlay {
				a.loadShell()
			}
			a.applyWindowSize()
			a.saveSettings()
		}
	} else if a.menuIdx == 10 && !a.editingROMDir { // Shell Skin select
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
