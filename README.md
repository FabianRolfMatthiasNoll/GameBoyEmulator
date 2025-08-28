# GameBoyEmulator

A fast, portable Game Boy (DMG) and Game Boy Color (CGB) emulator written in Go using Ebiten.

- CPU: Sharp LR35902 (SM83) core with cycle-accurate timing for common cases
- PPU: Tile-based renderer with window/sprites; CGB attributes and palettes supported
- APU: 4 channels (Square1, Square2, Wave, Noise) with basic mixing and stereo
- Mappers: MBC1, MBC3 (RTC), MBC5, with battery-backed RAM (.sav)
- UI: Simple Ebiten UI with ROM browser, settings, screenshots, and save states
- Platforms: Windows, macOS, Linux (Go + Ebiten)

## Features

- DMG and CGB execution paths with a toggle for "CGB Colors"
- DMG-on-CGB compatibility palettes with auto-selection and manual cycling
- Save/Load state per ROM + per-slot, separate from battery saves
- Screenshot capture (PNG)
- Configurable audio buffer/latency and BG renderer path

## Screenshots

Add your own screenshots here (replace placeholders):

- ![DMG grayscale – Tetris](assets/screenshots/dmg_tetris.png)
- ![CGB mode – Zelda Link's Awakening DX](assets/screenshots/cgb_zelda_dx.png)
- ![DMG-on-CGB – Pokémon Red with pastel palette](assets/screenshots/compat_pokemon_red_pastel.png)

Tip: Keep images around 800–1200px width for readability on GitHub.

## Quick start

- Install Go 1.21+ and run the desktop app:

```powershell
# From the repo root
# Run emulator (opens ROM browser if no -rom provided)
go run ./cmd/gbemu

# Or run directly with a ROM
go run ./cmd/gbemu -rom path\to\game.gb
```

- Controls:
  - D-Pad: Arrow keys
  - A/B: Z/X
  - Start/Select: Enter/Right Shift
  - Menu: Esc; Save state: F5; Load state: F9; Screenshot: F12
  - Palette cycle (DMG-on-CGB): [ and ]
  - Speed: Tab; Increase: F7; Decrease: F6

- Settings (in-app): scale, audio (stereo/mono), low-latency audio, BG renderer, ROMs directory, CGB Colors toggle, compat palette.

## ROMs and saves

- Place your Game Boy ROMs (".gb" / ".gbc") in the folder configured under Settings → ROMs Dir.
- Battery saves (.sav) are loaded/saved automatically next to the ROM.
- Save states are per ROM and per slot and do not affect .sav files.

## Project status

This project is a work-in-progress but already runs many commercial and homebrew titles.
Expect the usual emulator caveats: timing-sensitive edge cases, sound differences, and occasional glitches.

## License

MIT. See LICENSE file.
