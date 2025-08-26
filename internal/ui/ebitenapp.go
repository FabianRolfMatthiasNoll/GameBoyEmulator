package ui

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/FabianRolfMatthiasNoll/GameBoyEmulator/internal/emu"
)

type App struct {
	cfg Config
	m   *emu.Machine
	tex *ebiten.Image
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
	// TODO: poll keyboard → emu.Buttons and call a.m.SetButtons(...)
	a.m.StepFrame()
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
