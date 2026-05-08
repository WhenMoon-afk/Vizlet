# Vizlet

A lightweight, standalone Windows image viewer for GIF (animated), WEBP (animated + static), PNG, and JPG files. Single `.exe`, no installer, no DLL dependencies.

## Download

Grab `vizlet.exe` from the [Releases](https://github.com/WhenMoon-afk/Vizlet/releases) page.

## Register as Default Viewer

```
vizlet.exe --register
```

Then right-click any GIF → Open With → Choose another app → find Vizlet → check "Always use this app".

> Windows blocks programmatic default forcing since Windows 8. The `--register` flag adds Vizlet to the Open With list; you set it as default manually.

## Keyboard Shortcuts

| Key | Action |
|---|---|
| Left / Right | Previous / next file in folder using natural filename order |
| Space | Pause / resume |
| , / . | Step prev / next frame |
| [ / ] | Half / double playback speed |
| + / - | Zoom in / out |
| Scroll wheel | Zoom in / out |
| F | Fit to window |
| 1 | Actual size (1:1) |
| L / R | Rotate left / right |
| M | Toggle metadata panel |
| H | Toggle keyboard shortcuts overlay |
| Del | Move to recycle bin |
| Ctrl+C | Copy current frame to clipboard |
| U | Check for updates |
| Esc | Close |

## Building from Source

Requires Go 1.22+. Cross-compiles from Linux/macOS (no CGO).

```bash
GOOS=windows GOARCH=amd64 go build \
  -ldflags "-H windowsgui -s -w \
    -X main.Version=v0.0.1 \
    -X main.CommitSHA=$(git rev-parse --short HEAD) \
    -X main.BuildDate=$(date -u +%Y-%m-%d)" \
  -o vizlet.exe .
```

## Supported Formats

- **GIF** — animated and static, all disposal methods
- **WEBP** — animated and static (manual RIFF parsing for animation)
- **PNG** — standard decode
- **JPG / JPEG** — standard decode

## Known Limitations

- Animated WEBP falls back to first frame only if RIFF parsing fails on malformed files
- Self-update requires `vizlet.exe` to be in a user-writable location (e.g. Desktop, not `C:\Program Files`)
- No code signing; Windows SmartScreen may warn on first launch

## License

MIT — see [LICENSE](LICENSE)
