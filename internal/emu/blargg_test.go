package emu

import (
    "bytes"
    "os"
    "path/filepath"
    "runtime"
    "strconv"
    "strings"
    "testing"
)

// findROMs recursively collects .gb/.gbc files under dir.
func findROMs(dir string) ([]string, error) {
    var out []string
    err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
        if err != nil {
            return err
        }
        if d.IsDir() {
            return nil
        }
        low := strings.ToLower(d.Name())
        if strings.HasSuffix(low, ".gb") || strings.HasSuffix(low, ".gbc") {
            out = append(out, path)
        }
        return nil
    })
    return out, err
}

// runBlargg executes a test ROM until it reports via serial or times out.
func runBlargg(t *testing.T, romPath string, maxFrames int) {
    t.Helper()
    // Create a minimal machine in default (DMG) mode
    m := New(Config{})

    // Capture serial output
    var buf bytes.Buffer
    if err := m.LoadROMFromFile(romPath); err != nil {
        t.Fatalf("load ROM: %v", err)
    }
    // Attach serial writer AFTER loading ROM (which creates a new Bus)
    m.SetSerialWriter(&buf)

    // Step up to N frames without rendering, checking serial for pass/fail
    for i := 0; i < maxFrames; i++ {
        m.StepFrameNoRender()
        out := buf.String()
        if strings.Contains(out, "Passed") || strings.Contains(out, "passed") {
            return
        }
        if strings.Contains(out, "Failed") || strings.Contains(out, "failed") {
            t.Fatalf("%s reported failure via serial:\n%s", filepath.Base(romPath), out)
        }
    }
    t.Fatalf("timeout waiting for serial 'Passed' in %s; last output:\n%s", filepath.Base(romPath), buf.String())
}

// TestBlargg scans testroms/blargg (or BLARGG_DIR) and runs all .gb/.gbc found.
func TestBlargg(t *testing.T) {
    // Opt-in via env to avoid long test runs by default.
    if os.Getenv("RUN_BLARGG") == "" {
        t.Skip("set RUN_BLARGG=1 and place ROMs under testroms/blargg or set BLARGG_DIR to run")
    }

    base := os.Getenv("BLARGG_DIR")
    if base == "" {
        // Resolve relative to module root (directory containing go.mod)
        var root string
        if _, file, _, ok := runtime.Caller(0); ok {
            dir := filepath.Dir(file)
            for {
                if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
                    root = dir
                    break
                }
                parent := filepath.Dir(dir)
                if parent == dir { // reached filesystem root
                    break
                }
                dir = parent
            }
        }
        if root == "" {
            // Fallback to process CWD
            if wd, err := os.Getwd(); err == nil {
                root = wd
            } else {
                root = "."
            }
        }
        base = filepath.Join(root, "testroms", "blargg")
    }
    if _, err := os.Stat(base); err != nil {
        t.Skipf("blargg ROM dir missing: %s", base)
    }

    roms, err := findROMs(base)
    if err != nil {
        t.Fatalf("scan ROMs: %v", err)
    }
    if len(roms) == 0 {
        t.Skipf("no ROMs found in %s", base)
    }

    maxFrames := 1800
    if v := os.Getenv("BLARGG_MAX_FRAMES"); v != "" {
        if n, err := strconv.Atoi(v); err == nil && n > 0 {
            maxFrames = n
        }
    }

    for _, rom := range roms {
        rom := rom
        name := strings.TrimSuffix(filepath.Base(rom), filepath.Ext(rom))
        t.Run(name, func(t *testing.T) { runBlargg(t, rom, maxFrames) })
    }
}
