package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"time"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/emu"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
)

type App struct {
	cfg    Config
	m      *emu.Machine
	tex    *ebiten.Image
	paused bool
	fast   bool

	// overlay/menu
	showMenu bool
	menuIdx  int // 0: Save, 1: Load, 2: Switch ROM, 3: Exit
}

func NewApp(cfg Config, m *emu.Machine) *App {
	if cfg.Scale <= 0 {
		cfg.Scale = 3
	}
	ebiten.SetWindowTitle(cfg.Title)
	ebiten.SetWindowSize(160*cfg.Scale, 144*cfg.Scale)
	return &App{cfg: cfg, m: m}
}

func (a *App) Run() error { return ebiten.RunGame(a) }

func (a *App) Update() error {
	// Keyboard → Game Boy buttons
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
	if a.paused && inpututil.IsKeyJustPressed(ebiten.KeyN) {
		a.m.StepFrame()
	}

	// Toggle menu (Escape)
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		a.showMenu = !a.showMenu
	}
	if a.showMenu {
		// Navigate menu
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) {
			if a.menuIdx > 0 { a.menuIdx-- }
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) {
			if a.menuIdx < 3 { a.menuIdx++ }
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
			switch a.menuIdx {
			case 0: // Save State (slot 0)
				a.m.SaveStateToFile("slot0.savestate")
			case 1: // Load State
				_ = a.m.LoadStateFromFile("slot0.savestate")
			case 2: // Switch ROM: quick load a known test ROM (stub)
				_ = a.m.LoadROMFromFile("testroms/pokemon_red.gb")
			case 3:
				a.showMenu = false
			}
		}
	}

	// Screenshot (F12)
	if inpututil.IsKeyJustPressed(ebiten.KeyF12) {
		_ = a.saveScreenshot()
	}

	if !a.paused {
		if a.fast {
			// Run a few frames to speed up
			for i := 0; i < 5; i++ { a.m.StepFrame() }
		} else {
			a.m.StepFrame()
		}
	}
	return nil
}

func (a *App) Draw(screen *ebiten.Image) {
	if a.tex == nil {
		a.tex = ebiten.NewImage(160, 144)
	}
	a.tex.WritePixels(a.m.Framebuffer())
	screen.DrawImage(a.tex, nil)

	if a.showMenu {
		// Simple overlay rect and text using ebiten debug text
		overlay := ebiten.NewImage(160, 144)
		overlay.Fill(color.RGBA{0,0,0,128})
		screen.DrawImage(overlay, nil)
		lines := []string{
			"Menu:",
			"  Save state (slot 0)",
			"  Load state (slot 0)",
			"  Switch ROM (todo)",
			"  Close",
		}
		for i, s := range lines {
			prefix := "  "
			if i == a.menuIdx+1 { prefix = "> " }
			ebitenutil.DebugPrintAt(screen, prefix+s, 10, 10+i*14)
		}
	}
}

func (a *App) Layout(outW, outH int) (int, int) { return 160, 144 }

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
