package ui

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"time"

	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/emu"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

type App struct {
	cfg    Config
	m      *emu.Machine
	tex    *ebiten.Image
	paused bool
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

	// Screenshot (F12)
	if inpututil.IsKeyJustPressed(ebiten.KeyF12) {
		_ = a.saveScreenshot()
	}

	if !a.paused {
		a.m.StepFrame()
	}
	return nil
}

func (a *App) Draw(screen *ebiten.Image) {
	if a.tex == nil {
		a.tex = ebiten.NewImage(160, 144)
	}
	a.tex.WritePixels(a.m.Framebuffer())
	screen.DrawImage(a.tex, nil)
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
