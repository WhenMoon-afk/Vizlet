package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/gonutz/w32/v2"
	"github.com/gonutz/wui/v2"
	"github.com/rwcarlsen/goexif/exif"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
	"golang.org/x/sys/windows/registry"
)

const maxFileSize = 100 << 20

var imageExts = map[string]bool{".gif": true, ".webp": true, ".png": true, ".jpg": true, ".jpeg": true}

var (
	Version   = "dev"
	CommitSHA = "unknown"
	BuildDate = "unknown"
)

type Frame struct {
	img   *image.NRGBA
	delay time.Duration
}

type App struct {
	win                    *wui.Window
	box                    *wui.PaintBox
	filePath               string
	frames                 []Frame
	metaLines              []string
	encodedFPS             float64
	current                int
	paused                 bool
	speedMult              float64
	timerStop              chan struct{}
	mu                     sync.Mutex
	zoom                   float64
	fit                    bool
	panX, panY             int
	dragging               bool
	dragStartX, dragStartY int
	dragPanX, dragPanY     int
	showHUD                bool
	showMeta               bool
	scaledCache            map[string]*image.NRGBA
	imageCache             map[string]*wui.Image
	dirFiles               []string
	dirIndex               int
}

func main() {
	cleanupOldExe()

	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v" || os.Args[1] == "-version") {
		wui.MessageBox("Vizlet", fmt.Sprintf("Version: %s\nCommitSHA: %s\nBuildDate: %s", Version, CommitSHA, BuildDate))
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "--register" {
		if err := registerFileAssociations(); err != nil {
			wui.MessageBoxError("Vizlet", err.Error())
		} else {
			wui.MessageBoxInfo("Vizlet", "Vizlet was added to Open With for GIF, WEBP, PNG, JPG, and JPEG.")
		}
		return
	}

	if len(os.Args) > 2 && os.Args[1] == "-test" {
		frames, err := loadImageFile(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		b := frames[0].img.Bounds()
		fmt.Fprintf(os.Stderr, "frames=%d width=%d height=%d\n", len(frames), b.Dx(), b.Dy())
		return
	}

	if len(os.Args) == 2 {
		runViewer(os.Args[1])
		return
	}

	if len(os.Args) == 1 {
		showWelcomeScreen()
	}
}

func cleanupOldExe() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	_ = os.Remove(filepath.Join(filepath.Dir(exe), "vizlet-old.exe"))
}

func runViewer(path string) {
	frames, err := loadImageFile(path)
	if err != nil {
		wui.MessageBoxError("Vizlet", err.Error())
		return
	}
	app := &App{filePath: path, frames: frames, zoom: 1, fit: true, timerStop: make(chan struct{}), imageCache: map[string]*wui.Image{}, scaledCache: map[string]*image.NRGBA{}, speedMult: 1, showHUD: isFirstRun()}
	buildMeta(app)
	app.dirFiles, app.dirIndex = buildDirList(path)

	win := wui.NewWindow()
	win.SetTitle("Vizlet - " + filepath.Base(path))
	win.SetBackground(wui.Color(0x1c1c1e))
	w, h := initialWindowSize(frames[0].img.Bounds().Dx(), frames[0].img.Bounds().Dy()+24)
	win.SetInnerSize(w, h)
	box := wui.NewPaintBox()
	box.SetBounds(0, 0, w, h)
	box.SetAnchors(wui.AnchorMinAndMax, wui.AnchorMinAndMax)
	win.Add(box)
	app.win, app.box = win, box

	box.SetOnPaint(func(c *wui.Canvas) { paintViewer(app, c) })
	box.SetOnMouseMove(func(x, y int) {
		app.mouseMove(x, y)
	})
	win.SetOnKeyDown(func(key int) { app.keyDown(key) })
	win.SetOnMouseWheel(func(_, _ int, delta float64) {
		if delta > 0 {
			app.zoom *= 1.15
		} else {
			app.zoom /= 1.15
		}
		if app.zoom < 0.05 {
			app.zoom = 0.05
		}
		app.fit = false
		app.repaint()
	})
	win.SetOnShow(func() {
		app.installPaintBoxMouseHandlers()
	})
	win.SetOnClose(func() { close(app.timerStop) })
	if len(frames) > 1 {
		go startAnimationTimer(app)
	}
	_ = win.Show()
}

func showWelcomeScreen() {
	const winW, winH = 420, 300
	sw, sh := w32.GetSystemMetrics(w32.SM_CXSCREEN), w32.GetSystemMetrics(w32.SM_CYSCREEN)
	win := wui.NewWindow()
	win.SetTitle("Vizlet")
	win.SetResizable(false)
	win.SetBackground(wui.Color(0x1c1c1e))
	win.SetInnerBounds((sw-winW)/2, (sh-winH)/2, winW, winH)
	box := wui.NewPaintBox()
	box.SetBounds(0, 0, winW, winH)
	box.SetAnchors(wui.AnchorMinAndMax, wui.AnchorMinAndMax)
	win.Add(box)
	box.SetOnPaint(func(c *wui.Canvas) {
		c.FillRect(0, 0, c.Width(), c.Height(), wui.Color(0x1c1c1e))
		c.TextRectFormat(0, 32, winW, 40, "Vizlet", wui.FormatTopCenter, wui.Color(0xffffff))
		c.TextRectFormat(0, 78, winW, 22, "GIF / WEBP / Image Viewer", wui.FormatTopCenter, wui.Color(0x888888))
		c.TextRectFormat(0, 102, winW, 20, Version+" ("+CommitSHA+")", wui.FormatTopCenter, wui.Color(0x666666))
		drawButton(c, 50, 136, 320, 52, "Open Image...", 0x2a2a2a, 0x6aa7d8)
		drawButton(c, 80, 202, 260, 38, "Check for Updates", 0x252525, 0x404040)
		c.TextRectFormat(0, 258, winW, 20, "Drag an image onto this window to open it", wui.FormatTopCenter, wui.Color(0x555555))
	})
	openImage := func() {
		dlg := wui.NewFileOpenDialog()
		dlg.SetTitle("Open Image")
		dlg.AddFilter("Images", ".gif", ".webp", ".png", ".jpg", ".jpeg")
		dlg.AddFilter("All files", "*.*")
		if ok, path := dlg.ExecuteSingleSelection(win); ok {
			win.Close()
			runViewer(path)
		}
	}
	win.SetOnMouseDown(func(button wui.MouseButton, x, y int) {
		if button != wui.MouseButtonLeft {
			return
		}
		switch {
		case inRect(x, y, 50, 136, 320, 52):
			openImage()
		case inRect(x, y, 80, 202, 260, 38):
			go showUpdateDialog()
		}
	})
	win.SetOnShow(func() {
		hwnd := w32.HWND(win.Handle())
		if hwnd != 0 {
			w32.DragAcceptFiles(hwnd, true)
		}
	})
	win.SetOnMessage(func(window uintptr, msg uint32, wParam, lParam uintptr) (bool, uintptr) {
		if msg != 0x0233 {
			return false, 0
		}
		if path := droppedFilePath(wParam); path != "" {
			win.Close()
			runViewer(path)
		}
		return true, 0
	})
	_ = win.Show()
}

func drawButton(c *wui.Canvas, x, y, w, h int, label string, fill, border uint32) {
	c.FillRect(x, y, w, h, wui.Color(fill))
	c.DrawRect(x, y, w, h, wui.Color(border))
	c.TextRectFormat(x, y, w, h, label, wui.FormatCenter, wui.Color(0xffffff))
}

func inRect(px, py, x, y, w, h int) bool {
	return px >= x && px < x+w && py >= y && py < y+h
}

func droppedFilePath(drop uintptr) string {
	var buf [1024]uint16
	procQuery := shell32.NewProc("DragQueryFileW")
	procFinish := shell32.NewProc("DragFinish")
	defer procFinish.Call(drop)
	n, _, _ := procQuery.Call(drop, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

func loadImageFile(path string) ([]Frame, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileSize {
		return nil, fmt.Errorf("%s is larger than 100 MB", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	switch strings.ToLower(filepath.Ext(path)) {
	case ".gif":
		return loadGIF(f)
	case ".webp":
		return loadWEBP(path)
	case ".png":
		img, err := png.Decode(f)
		return singleFrame(img, err)
	case ".jpg", ".jpeg":
		img, err := jpeg.Decode(f)
		return singleFrame(img, err)
	default:
		return nil, fmt.Errorf("unsupported image format: %s", strings.ToLower(filepath.Ext(path)))
	}
}

func loadWEBP(path string) ([]Frame, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if frames, err := loadAnimatedWEBP(data); err == nil {
		return frames, nil
	}
	img, err := webp.Decode(bytes.NewReader(data))
	return singleFrame(img, err)
}

func singleFrame(img image.Image, err error) ([]Frame, error) {
	if err != nil {
		return nil, err
	}
	return []Frame{{img: toNRGBA(img), delay: 100 * time.Millisecond}}, nil
}

func loadGIF(f *os.File) ([]Frame, error) {
	g, err := gif.DecodeAll(f)
	if err != nil {
		return nil, err
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, g.Config.Width, g.Config.Height))
	var frames []Frame
	for i, src := range g.Image {
		before := cloneNRGBA(canvas)
		r := src.Bounds()
		stddraw.Draw(canvas, r, src, src.Bounds().Min, stddraw.Over)
		delay := 1
		if i < len(g.Delay) && g.Delay[i] > 0 {
			delay = g.Delay[i]
		}
		frames = append(frames, Frame{img: cloneNRGBA(canvas), delay: time.Duration(delay*10) * time.Millisecond})
		if i < len(g.Disposal) {
			switch g.Disposal[i] {
			case gif.DisposalBackground:
				stddraw.Draw(canvas, r, &image.Uniform{C: color.NRGBA{}}, image.Point{}, stddraw.Src)
			case gif.DisposalPrevious:
				canvas = before
			}
		}
	}
	if len(frames) == 0 {
		return nil, errors.New("GIF has no frames")
	}
	return frames, nil
}

func loadAnimatedWEBP(data []byte) ([]Frame, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, errors.New("not a WEBP RIFF")
	}
	var animated bool
	canvasW, canvasH := 0, 0
	var animBG color.NRGBA
	canvas := (*image.NRGBA)(nil)
	var frames []Frame

	for off := 12; off+8 <= len(data); {
		fourCC := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		chunkStart := off + 8
		chunkEnd := chunkStart + size
		if size < 0 || chunkEnd > len(data) {
			return nil, errors.New("invalid WEBP chunk")
		}
		chunk := data[chunkStart:chunkEnd]
		switch fourCC {
		case "VP8X":
			if len(chunk) < 10 || chunk[0]&0x02 == 0 {
				return nil, errors.New("not animated")
			}
			animated = true
			canvasW = int(read24(chunk[4:7])) + 1
			canvasH = int(read24(chunk[7:10])) + 1
			canvas = image.NewNRGBA(image.Rect(0, 0, canvasW, canvasH))
		case "ANIM":
			if len(chunk) >= 4 {
				animBG = color.NRGBA{R: chunk[2], G: chunk[1], B: chunk[0], A: chunk[3]}
			}
		case "ANMF":
			if !animated || canvas == nil {
				return nil, errors.New("ANMF before animated canvas")
			}
			frame, err := decodeANMF(chunk)
			if err != nil {
				return nil, err
			}
			if frame.x+frame.img.Bounds().Dx() > canvasW || frame.y+frame.img.Bounds().Dy() > canvasH {
				return nil, errors.New("WEBP frame outside canvas")
			}
			dst := image.Rect(frame.x, frame.y, frame.x+frame.img.Bounds().Dx(), frame.y+frame.img.Bounds().Dy())
			op := stddraw.Over
			if frame.noBlend {
				op = stddraw.Src
			}
			stddraw.Draw(canvas, dst, frame.img, image.Point{}, op)
			delay := time.Duration(frame.duration) * time.Millisecond
			if delay <= 0 {
				delay = 10 * time.Millisecond
			}
			frames = append(frames, Frame{img: cloneNRGBA(canvas), delay: delay})
			if frame.dispose {
				stddraw.Draw(canvas, dst, &image.Uniform{C: animBG}, image.Point{}, stddraw.Src)
			}
		}
		off = chunkEnd
		if off%2 == 1 {
			off++
		}
	}
	if !animated {
		return nil, errors.New("not animated")
	}
	if len(frames) == 0 {
		return nil, errors.New("animated WEBP has no frames")
	}
	return frames, nil
}

type webpFrame struct {
	img      *image.NRGBA
	x, y     int
	duration uint32
	noBlend  bool
	dispose  bool
}

func decodeANMF(chunk []byte) (webpFrame, error) {
	if len(chunk) < 16 {
		return webpFrame{}, errors.New("short ANMF chunk")
	}
	frame := webpFrame{
		x:        int(read24(chunk[0:3])) * 2,
		y:        int(read24(chunk[3:6])) * 2,
		duration: read24(chunk[12:15]),
		dispose:  chunk[15]&0x01 != 0,
		noBlend:  chunk[15]&0x02 != 0,
	}
	frameW := int(read24(chunk[6:9])) + 1
	frameH := int(read24(chunk[9:12])) + 1
	img, err := webp.Decode(bytes.NewReader(wrapWebPFrame(chunk[16:])))
	if err != nil {
		return webpFrame{}, err
	}
	nrgba := toNRGBA(img)
	if nrgba.Bounds().Dx() != frameW || nrgba.Bounds().Dy() != frameH {
		resized := image.NewNRGBA(image.Rect(0, 0, frameW, frameH))
		stddraw.Draw(resized, resized.Bounds(), nrgba, image.Point{}, stddraw.Src)
		nrgba = resized
	}
	frame.img = nrgba
	return frame, nil
}

func read24(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}

func wrapWebPFrame(frameData []byte) []byte {
	riffSize := uint32(4 + len(frameData))
	out := make([]byte, 12+len(frameData))
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], riffSize)
	copy(out[8:12], "WEBP")
	copy(out[12:], frameData)
	return out
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	stddraw.Draw(dst, dst.Bounds(), src, b.Min, stddraw.Src)
	return dst
}

func cloneNRGBA(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

func initialWindowSize(w, h int) (int, int) {
	sw, sh := w32.GetSystemMetrics(w32.SM_CXSCREEN)*9/10, w32.GetSystemMetrics(w32.SM_CYSCREEN)*9/10
	if sw <= 0 {
		sw, sh = 1200, 800
	}
	scale := math.Min(float64(sw)/float64(w), float64(sh)/float64(h))
	if scale > 1 {
		scale = 1
	}
	return max(240, int(float64(w)*scale)), max(180, int(float64(h)*scale))
}

func paintViewer(a *App, c *wui.Canvas) {
	c.FillRect(0, 0, c.Width(), c.Height(), wui.Color(0x1c1c1e))
	a.mu.Lock()
	current, paused, speed := a.current, a.paused, a.speedMult
	img := a.frames[current].img
	a.mu.Unlock()
	iw, ih := img.Bounds().Dx(), img.Bounds().Dy()
	areaW, areaH := c.Width(), max(1, c.Height()-24)
	if a.showMeta {
		areaW = max(1, areaW-280)
	}
	dw, dh := iw, ih
	if a.fit {
		scale := math.Min(float64(areaW)/float64(iw), float64(areaH)/float64(ih))
		if scale <= 0 {
			scale = 1
		}
		dw, dh = max(1, int(float64(iw)*scale)), max(1, int(float64(ih)*scale))
	} else {
		dw, dh = max(1, int(float64(iw)*a.zoom)), max(1, int(float64(ih)*a.zoom))
	}
	drawn := img
	if dw != iw || dh != ih {
		drawn = a.scaledFrame(img, dw, dh)
	}
	key := fmt.Sprintf("%p-%dx%d", img, dw, dh)
	wimg := a.imageCache[key]
	if wimg == nil {
		wimg = wui.NewImage(drawn)
		a.imageCache[key] = wimg
		if len(a.imageCache) > 256 {
			a.imageCache = map[string]*wui.Image{}
		}
	}
	x, y := (areaW-dw)/2, (areaH-dh)/2
	if !a.fit {
		x += a.panX
		y += a.panY
	}
	c.DrawImage(wimg, wimg.Bounds(), x, y)
	if len(a.frames) > 1 {
		counter := fmt.Sprintf("%d/%d", current+1, len(a.frames))
		tw, _ := c.TextExtent(counter)
		c.FillRect(6, 4, tw+8, 20, wui.Color(0x2a2a2a))
		c.TextOut(10, 8, counter, wui.Color(0xffffff))
	}
	drawStatusBar(a, c, current, paused, speed)
	if a.showMeta {
		paintMetaPanel(a, c, c.Width(), c.Height())
	}
	if a.showHUD {
		paintHUD(a, c, c.Width(), c.Height())
	}
}

func (a *App) scaledFrame(img *image.NRGBA, dw, dh int) *image.NRGBA {
	key := fmt.Sprintf("%p-%dx%d", img, dw, dh)
	if cached := a.scaledCache[key]; cached != nil {
		return cached
	}
	scaled := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	xdraw.BiLinear.Scale(scaled, scaled.Bounds(), img, img.Bounds(), stddraw.Over, nil)
	a.scaledCache[key] = scaled
	if len(a.scaledCache) > 512 {
		a.scaledCache = map[string]*image.NRGBA{}
		a.scaledCache[key] = scaled
	}
	return scaled
}

func (a *App) drawStatusBar(c *wui.Canvas) {
	filename := filepath.Base(a.filePath)
	text := " " + filename + " "
	if len(a.frames) > 1 {
		total := time.Duration(0)
		for _, f := range a.frames {
			total += f.delay
		}
		encodedFPS := float64(len(a.frames)) / total.Seconds()
		playingFPS := a.speedMult / a.frames[a.current].delay.Seconds()
		text = fmt.Sprintf(" frame %d/%d  |  %.1ffps encoded  |  %.1ffps playing  |  %.1fx speed  |  %s ", a.current+1, len(a.frames), encodedFPS, playingFPS, a.speedMult, filename)
	}
	y := max(0, c.Height()-24)
	c.FillRect(0, y, c.Width(), 24, wui.Color(0x101010))
	c.TextOut(8, y+5, text, wui.Color(0xFFFFFF))
}

func drawStatusBar(a *App, c *wui.Canvas, current int, paused bool, speed float64) {
	y := max(0, c.Height()-24)
	c.FillRect(0, y, c.Width(), 24, wui.Color(0x1e1e1e))
	c.Line(0, y, c.Width(), y, wui.Color(0x2e2e2e))
	if len(a.frames) > 1 {
		icon := ">"
		fps := fmt.Sprintf("%.1ffps", a.encodedFPS)
		if paused {
			icon = "||"
			fps = "PAUSED"
		}
		text := fmt.Sprintf("%s  %d/%d  |  %s  |  %.3gx", icon, current+1, len(a.frames), fps, speed)
		c.TextOut(8, y+5, text, wui.Color(0xcccccc))
	}
	right := filepath.Base(a.filePath)
	c.TextOut(max(8, c.Width()-12-len(right)*7), y+5, right, wui.Color(0xaaaaaa))
}

func (a *App) drawHUD(c *wui.Canvas) {
	lines := []string{
		"H          hide this help",
		"Space      pause / resume",
		"< >        step frame",
		"[ ]        speed  0.5x / 2x",
		"F          fit to window",
		"scroll     zoom",
		"drag       pan (when zoomed)",
		"+ -        zoom in/out",
		"L / R      rotate",
		"arrows     next/prev file",
		"Del        recycle bin",
		"Ctrl+C     copy frame",
		"M          metadata panel",
		"right-click  context menu",
	}
	w, lineH := 250, 18
	h := len(lines)*lineH + 16
	x, y := 16, max(16, c.Height()-h-36)
	c.FillRect(x, y, w, h, wui.Color(0x1A1A1A))
	for i, line := range lines {
		c.TextOut(x+10, y+8+i*lineH, line, wui.Color(0xF0F0F0))
	}
}

func (a *App) drawMeta(c *wui.Canvas) {
	if len(a.metaLines) == 0 {
		return
	}
	w, lineH := 280, 18
	h := min(c.Height()-32, len(a.metaLines)*lineH+20)
	x, y := max(0, c.Width()-w-16), 16
	c.FillRect(x, y, w, h, wui.Color(0x1A1A1A))
	for i, line := range a.metaLines {
		ty := y + 10 + i*lineH
		if ty+lineH > y+h {
			break
		}
		c.TextOut(x+10, ty, line, wui.Color(0xFFFFFF))
	}
}

func paintMetaPanel(a *App, c *wui.Canvas, boxW, boxH int) {
	panelX, panelW := boxW-280, 280
	panelH := max(0, boxH-24)
	c.FillRect(panelX, 0, panelW, panelH, wui.Color(0x252525))
	c.Line(panelX, 0, panelX, panelH, wui.Color(0x383838))
	y := 16
	firstSection := true
	for _, line := range a.metaLines {
		if y > panelH-18 {
			break
		}
		if strings.HasPrefix(line, "##") {
			if !firstSection {
				c.Line(panelX, y, boxW, y, wui.Color(0x383838))
			}
			firstSection = false
			y += 8
			c.TextOut(panelX+12, y, strings.TrimPrefix(line, "##"), wui.Color(0xffffff))
			y += 22
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		label, value := line, ""
		if len(parts) == 2 {
			label, value = parts[0], parts[1]
		}
		c.TextOut(panelX+12, y, label, wui.Color(0x888888))
		c.TextOut(panelX+110, y, value, wui.Color(0xffffff))
		y += 18
	}
}

func paintHUD(a *App, c *wui.Canvas, boxW, boxH int) {
	_ = a
	panelW, panelH := 520, 320
	x, y := (boxW-panelW)/2, (boxH-panelH)/2
	c.FillRect(x, y, panelW, panelH, wui.Color(0x2c2c2c))
	c.FillRect(x, y, panelW, 1, wui.Color(0x444444))
	c.FillRect(x, y+panelH-1, panelW, 1, wui.Color(0x444444))
	c.FillRect(x, y, 1, panelH, wui.Color(0x444444))
	c.FillRect(x+panelW-1, y, 1, panelH, wui.Color(0x444444))
	c.TextOut(x+16, y+14, "Keyboard Shortcuts", wui.Color(0xffffff))
	c.Line(x+12, y+36, x+508, y+36, wui.Color(0x444444))
	left := [][2]string{{"Space", "pause / resume"}, {"< >", "prev / next file"}, {"scroll", "zoom in / out"}, {"drag", "pan when zoomed"}, {"[ ]", "half / double speed"}, {", .", "prev / next frame"}, {"+ -", "zoom in / out"}}
	right := [][2]string{{"F", "fit to window"}, {"1", "actual size 1:1"}, {"L / R", "rotate left / right"}, {"M", "metadata panel"}, {"H", "toggle this overlay"}, {"Del", "recycle bin"}, {"Ctrl+C", "copy frame"}, {"U", "check for updates"}, {"Esc", "close"}}
	drawRows := func(baseX int, rows [][2]string) {
		for i, row := range rows {
			yy := y + 48 + i*22
			c.TextOut(baseX, yy, row[0], wui.Color(0x6aa7d8))
			c.TextOut(baseX+144, yy, row[1], wui.Color(0xaaaaaa))
		}
	}
	drawRows(x+16, left)
	drawRows(x+276, right)
	c.TextOut(x+16, y+302, "H to toggle  |  right-click for menu", wui.Color(0x555555))
}

func (a *App) keyDown(key int) {
	switch key {
	case int(wui.KeyLeft):
		a.navigate(-1)
	case int(wui.KeyRight):
		a.navigate(1)
	case int(wui.KeyAdd), int(wui.KeyOEMPlus):
		a.zoom *= 1.25
		a.fit = false
		a.repaint()
	case int(wui.KeySubtract), int(wui.KeyOEMMinus):
		a.zoom /= 1.25
		if a.zoom < 0.05 {
			a.zoom = 0.05
		}
		a.fit = false
		a.repaint()
	case int(wui.KeyF):
		a.fit = !a.fit
		if a.fit {
			a.panX, a.panY = 0, 0
		}
		a.repaint()
	case 0x31:
		a.zoom = 1
		a.fit = false
		a.panX, a.panY = 0, 0
		a.repaint()
	case int(wui.KeySpace):
		a.mu.Lock()
		a.paused = !a.paused
		a.mu.Unlock()
		a.repaint()
	case int(wui.KeyOEMComma):
		a.mu.Lock()
		a.paused = true
		if len(a.frames) > 0 {
			if a.current > 0 {
				a.current--
			} else {
				a.current = len(a.frames) - 1
			}
		}
		a.mu.Unlock()
		a.repaint()
	case int(wui.KeyOEMPeriod):
		a.mu.Lock()
		a.paused = true
		if len(a.frames) > 0 {
			a.current = (a.current + 1) % len(a.frames)
		}
		a.mu.Unlock()
		a.repaint()
	case int(wui.KeyOEM4):
		a.mu.Lock()
		if a.speedMult > 0.125 {
			a.speedMult /= 2
		}
		a.mu.Unlock()
		a.repaint()
	case int(wui.KeyOEM6):
		a.mu.Lock()
		if a.speedMult < 8.0 {
			a.speedMult *= 2
		}
		a.mu.Unlock()
		a.repaint()
	case int(wui.KeyH):
		a.showHUD = !a.showHUD
		a.repaint()
	case int(wui.KeyM):
		a.showMeta = !a.showMeta
		a.repaint()
	case int(wui.KeyU):
		go showUpdateDialog()
	case int(wui.KeyL):
		a.rotate(-1)
	case int(wui.KeyR):
		a.rotate(1)
	case int(wui.KeyDelete):
		a.deleteCurrentFile()
	case int(wui.KeyEscape):
		a.win.Close()
	case int(wui.KeyC):
		if w32.GetKeyState(w32.VK_CONTROL)&0x8000 != 0 {
			copyFrameToClipboard(a.frames[a.current].img)
		}
	}
}

func (a *App) startAnimation() {
	if len(a.frames) < 2 {
		return
	}
	go startAnimationTimer(a)
}

func (a *App) adjustedDelay(delay time.Duration) time.Duration {
	if a.speedMult <= 0 {
		a.speedMult = 1
	}
	adjusted := time.Duration(float64(delay) / a.speedMult)
	if adjusted < 10*time.Millisecond {
		return 10 * time.Millisecond
	}
	return adjusted
}

func startAnimationTimer(a *App) {
	if len(a.frames) < 2 {
		return
	}
	timer := time.NewTimer(a.adjustedDelay(a.frames[a.current].delay))
	defer timer.Stop()
	for {
		select {
		case <-a.timerStop:
			return
		case <-timer.C:
			a.mu.Lock()
			if !a.paused {
				a.current = (a.current + 1) % len(a.frames)
			}
			delay := a.adjustedDelay(a.frames[a.current].delay)
			a.mu.Unlock()
			a.repaint()
			timer.Reset(delay)
		}
	}
}

func (a *App) setFrames(path string, frames []Frame) {
	stopTimer(a)
	a.mu.Lock()
	a.filePath, a.frames, a.current, a.zoom, a.fit = path, frames, 0, 1, true
	a.paused = false
	a.timerStop = make(chan struct{})
	a.imageCache = map[string]*wui.Image{}
	a.scaledCache = map[string]*image.NRGBA{}
	a.panX, a.panY = 0, 0
	buildMeta(a)
	a.mu.Unlock()
	a.win.SetTitle("Vizlet - " + filepath.Base(path))
	w, h := initialWindowSize(frames[0].img.Bounds().Dx(), frames[0].img.Bounds().Dy()+24)
	a.win.SetInnerSize(w, h)
	if len(frames) > 1 {
		go startAnimationTimer(a)
	}
	a.repaint()
}

func stopTimer(a *App) {
	defer func() { _ = recover() }()
	if a.timerStop != nil {
		close(a.timerStop)
	}
}

func (a *App) repaint() {
	if a.box != nil {
		a.box.Paint()
	}
}

func (a *App) installPaintBoxMouseHandlers() {
	if a.box == nil || a.box.Handle() == 0 {
		return
	}
	hwnd := w32.HWND(a.box.Handle())
	w32.SetWindowSubclass(hwnd, syscall.NewCallback(func(window w32.HWND, msg uint32, wParam, lParam uintptr, subclassID uintptr, refData uintptr) uintptr {
		x, y := mousePoint(lParam)
		switch msg {
		case w32.WM_LBUTTONDOWN:
			w32.SetCapture(window)
			a.mouseDown(wui.MouseButtonLeft, x, y)
			return 0
		case w32.WM_LBUTTONUP:
			w32.ReleaseCapture()
			a.mouseUp(wui.MouseButtonLeft, x, y)
			return 0
		case w32.WM_RBUTTONDOWN:
			a.mouseDown(wui.MouseButtonRight, x, y)
			return 0
		case w32.WM_MOUSEMOVE:
			a.mouseMove(x, y)
		}
		return w32.DefSubclassProc(window, msg, wParam, lParam)
	}), 42, 0)
}

func mousePoint(lParam uintptr) (int, int) {
	return int(int16(lParam & 0xFFFF)), int(int16((lParam >> 16) & 0xFFFF))
}

func (a *App) mouseDown(button wui.MouseButton, x, y int) {
	if button == wui.MouseButtonRight {
		a.showContextMenu(x, y)
		return
	}
	if button == wui.MouseButtonLeft {
		a.mu.Lock()
		a.dragging = true
		a.dragStartX, a.dragStartY = x, y
		a.dragPanX, a.dragPanY = a.panX, a.panY
		a.mu.Unlock()
	}
}

func (a *App) mouseMove(x, y int) {
	a.mu.Lock()
	if !a.dragging {
		a.mu.Unlock()
		return
	}
	a.panX = a.dragPanX + (x - a.dragStartX)
	a.panY = a.dragPanY + (y - a.dragStartY)
	a.mu.Unlock()
	a.repaint()
}

func (a *App) mouseUp(button wui.MouseButton, x, y int) {
	if button == wui.MouseButtonLeft {
		a.mu.Lock()
		a.dragging = false
		a.mu.Unlock()
	}
}

func buildDirList(current string) ([]string, int) {
	dir := filepath.Dir(current)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{current}, 0
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && imageExts[strings.ToLower(filepath.Ext(e.Name()))] {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Slice(files, func(i, j int) bool { return naturalPathLess(files[i], files[j]) })
	for i, f := range files {
		if samePath(f, current) {
			return files, i
		}
	}
	return files, 0
}

func naturalPathLess(a, b string) bool {
	return naturalLess(filepath.Base(a), filepath.Base(b))
}

func naturalLess(a, b string) bool {
	ai, bi := 0, 0
	al, bl := strings.ToLower(a), strings.ToLower(b)
	for ai < len(al) && bi < len(bl) {
		ar, br := al[ai], bl[bi]
		if isASCIIDigit(ar) && isASCIIDigit(br) {
			anumStart, bnumStart := ai, bi
			for ai < len(al) && isASCIIDigit(al[ai]) {
				ai++
			}
			for bi < len(bl) && isASCIIDigit(bl[bi]) {
				bi++
			}
			anum := strings.TrimLeft(al[anumStart:ai], "0")
			bnum := strings.TrimLeft(bl[bnumStart:bi], "0")
			if anum == "" {
				anum = "0"
			}
			if bnum == "" {
				bnum = "0"
			}
			if len(anum) != len(bnum) {
				return len(anum) < len(bnum)
			}
			if anum != bnum {
				return anum < bnum
			}
			// Same numeric value: shorter digit run sorts first, so image1
			// comes before image001 while remaining deterministic.
			alen, blen := ai-anumStart, bi-bnumStart
			if alen != blen {
				return alen < blen
			}
			continue
		}
		if ar != br {
			return ar < br
		}
		ai++
		bi++
	}
	if len(al) != len(bl) {
		return len(al) < len(bl)
	}
	// If the case-insensitive comparison found the names equivalent, fall back
	// to the original spelling so case-only distinct filenames still sort in a
	// deterministic total order on case-sensitive filesystems.
	return a < b
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func (a *App) navigate(delta int) {
	if len(a.dirFiles) == 0 {
		return
	}
	for tries := 0; tries < len(a.dirFiles); tries++ {
		a.dirIndex = (a.dirIndex + delta + len(a.dirFiles)) % len(a.dirFiles)
		path := a.dirFiles[a.dirIndex]
		frames, err := loadImageFile(path)
		if err == nil {
			a.setFrames(path, frames)
			return
		}
		a.win.SetTitle(filepath.Base(path) + " - " + err.Error())
	}
}

func (a *App) rotate(dir int) {
	a.mu.Lock()
	for i, f := range a.frames {
		a.frames[i].img = rotateNRGBA(f.img, dir)
	}
	a.imageCache = map[string]*wui.Image{}
	a.scaledCache = map[string]*image.NRGBA{}
	buildMeta(a)
	a.mu.Unlock()
	a.repaint()
}

func rotateNRGBA(src *image.NRGBA, dir int) *image.NRGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.NRGBAAt(x, y)
			if dir > 0 {
				dst.SetNRGBA(h-1-y, x, c)
			} else {
				dst.SetNRGBA(y, w-1-x, c)
			}
		}
	}
	return dst
}

func (a *App) deleteCurrentFile() {
	old := a.filePath
	if err := recycleFile(old); err != nil {
		a.win.SetTitle(filepath.Base(old) + " - delete failed: " + err.Error())
		return
	}
	a.dirFiles, a.dirIndex = buildDirList(old)
	if len(a.dirFiles) == 0 {
		a.win.Close()
		return
	}
	if a.dirIndex >= len(a.dirFiles) {
		a.dirIndex = 0
	}
	frames, err := loadImageFile(a.dirFiles[a.dirIndex])
	if err != nil {
		a.navigate(1)
		return
	}
	a.setFrames(a.dirFiles[a.dirIndex], frames)
}

func (a *App) showContextMenu(x, y int) {
	hwnd := w32.HWND(a.win.Handle())
	if hwnd == 0 {
		return
	}
	sx, sy := w32.ClientToScreen(w32.HWND(a.box.Handle()), x, y)
	hMenu := w32.CreatePopupMenu()
	if hMenu == 0 {
		return
	}
	defer w32.DestroyMenu(hMenu)

	const (
		idPauseResume  = 1
		idFitWindow    = 2
		idActualSize   = 3
		idRotateL      = 4
		idRotateR      = 5
		idCopyFrame    = 6
		idMetadata     = 7
		idShortcuts    = 8
		idRegister     = 9
		idUpdate       = 10
		idDelete       = 11
		tpmReturnCmd   = 0x0100
		tpmRightButton = 0x0002
	)

	a.mu.Lock()
	pauseLabel := "Pause"
	if a.paused {
		pauseLabel = "Resume"
	}
	a.mu.Unlock()
	w32.AppendMenu(hMenu, w32.MF_STRING, idPauseResume, pauseLabel)
	w32.AppendMenu(hMenu, w32.MF_STRING, idFitWindow, "Fit to Window (F)")
	w32.AppendMenu(hMenu, w32.MF_STRING, idActualSize, "Actual Size (1:1)")
	w32.AppendMenu(hMenu, w32.MF_STRING, idRotateL, "Rotate Left (L)")
	w32.AppendMenu(hMenu, w32.MF_STRING, idRotateR, "Rotate Right (R)")
	w32.AppendMenu(hMenu, w32.MF_STRING, idCopyFrame, "Copy Frame (Ctrl+C)")
	w32.AppendMenu(hMenu, w32.MF_SEPARATOR, 0, "")
	w32.AppendMenu(hMenu, w32.MF_STRING, idMetadata, "File Info / Metadata (M)")
	w32.AppendMenu(hMenu, w32.MF_STRING, idShortcuts, "Keyboard Shortcuts (H)")
	w32.AppendMenu(hMenu, w32.MF_SEPARATOR, 0, "")
	w32.AppendMenu(hMenu, w32.MF_STRING, idDelete, "Move to Recycle Bin (Del)")
	w32.AppendMenu(hMenu, w32.MF_SEPARATOR, 0, "")
	w32.AppendMenu(hMenu, w32.MF_STRING, idUpdate, "Check for Updates (U)")
	w32.AppendMenu(hMenu, w32.MF_STRING, idRegister, "Register Vizlet (Open With)")

	cmd := w32.TrackPopupMenuEx(hMenu, tpmReturnCmd|tpmRightButton, sx, sy, hwnd, nil)
	switch cmd {
	case idPauseResume:
		a.mu.Lock()
		a.paused = !a.paused
		a.mu.Unlock()
		a.repaint()
	case idFitWindow:
		a.fit = true
		a.panX, a.panY = 0, 0
		a.repaint()
	case idActualSize:
		a.fit = false
		a.zoom = 1
		a.panX, a.panY = 0, 0
		a.repaint()
	case idRotateL:
		a.rotate(-1)
	case idRotateR:
		a.rotate(1)
	case idCopyFrame:
		a.mu.Lock()
		img := a.frames[a.current].img
		a.mu.Unlock()
		_ = copyFrameToClipboard(img)
	case idMetadata:
		a.showMeta = !a.showMeta
		a.repaint()
	case idShortcuts:
		a.showHUD = !a.showHUD
		a.repaint()
	case idDelete:
		a.deleteCurrentFile()
	case idUpdate:
		go showUpdateDialog()
	case idRegister:
		if err := registerFileAssociations(); err != nil {
			wui.MessageBoxError("Vizlet", err.Error())
		} else {
			wui.MessageBoxInfo("Vizlet", "Registered.")
		}
	}
}

func samePath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return strings.EqualFold(aa, bb)
}

func (a *App) buildMetaLines() []string {
	return buildMeta(a)
}

func buildMeta(a *App) []string {
	lines := []string{"##Image"}
	info, err := os.Stat(a.filePath)
	if err == nil {
		ext := strings.TrimPrefix(strings.ToUpper(filepath.Ext(a.filePath)), ".")
		img := a.frames[0].img
		lines = append(lines,
			"File\t"+filepath.Base(a.filePath),
			"Format\t"+ext,
			fmt.Sprintf("Dimensions\t%d x %d", img.Bounds().Dx(), img.Bounds().Dy()),
			"File size\t"+humanSize(info.Size()),
			"Modified\t"+info.ModTime().Format("2006-01-02 15:04"),
		)
	}
	totalDur := time.Duration(0)
	for _, f := range a.frames {
		totalDur += f.delay
	}
	if totalDur > 0 {
		a.encodedFPS = float64(len(a.frames)) / totalDur.Seconds()
	}
	if len(a.frames) > 1 {
		lines = append(lines,
			"##Animation",
			fmt.Sprintf("Frames\t%d", len(a.frames)),
			fmt.Sprintf("Encoded FPS\t%.2f", a.encodedFPS),
			fmt.Sprintf("Duration\t%.2fs", totalDur.Seconds()),
		)
	}
	if exif := exifLines(a.filePath); len(exif) > 0 {
		lines = append(lines, "##EXIF")
		lines = append(lines, exif...)
	}
	a.metaLines = lines
	return lines
}

func exifLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	x, err := exif.Decode(f)
	if err != nil {
		return nil
	}
	var lines []string
	for _, item := range []struct {
		name string
		tag  exif.FieldName
	}{
		{"Camera make", exif.Make},
		{"Camera model", exif.Model},
		{"Date taken", exif.DateTime},
		{"Exposure", exif.ExposureTime},
		{"F-stop", exif.FNumber},
		{"ISO", exif.ISOSpeedRatings},
	} {
		if val, err := x.Get(item.tag); err == nil {
			if text, err := val.StringVal(); err == nil {
				lines = append(lines, item.name+"\t"+text)
			} else {
				lines = append(lines, item.name+"\t"+strings.TrimSpace(val.String()))
			}
		}
	}
	if lat, long, err := x.LatLong(); err == nil {
		lines = append(lines, fmt.Sprintf("GPS coords\t%.6f, %.6f", lat, long))
	}
	return lines
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<10:
		if n < 1<<20 {
			return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
		}
		if n < 1<<30 {
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		}
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func recycleFile(path string) error {
	p := utf16.Encode([]rune(path))
	p = append(p, 0, 0)
	op := shFileOp{WFunc: foDelete, PFrom: &p[0], FFlags: fofAllowUndo | fofNoConfirmation | fofSilent}
	ret, _, _ := shell32.NewProc("SHFileOperationW").Call(uintptr(unsafe.Pointer(&op)))
	if ret != 0 {
		return fmt.Errorf("SHFileOperation returned %d", ret)
	}
	return nil
}

var shell32 = syscall.NewLazyDLL("shell32.dll")

const (
	foDelete          = 3
	fofAllowUndo      = 0x0040
	fofNoConfirmation = 0x0010
	fofSilent         = 0x0004
)

type shFileOp struct {
	Hwnd    uintptr
	WFunc   uint32
	_pad0   [4]byte
	PFrom   *uint16
	PTo     *uint16
	FFlags  uint16
	_pad1   [2]byte
	AnyOps  int32
	NameMap uintptr
	Title   *uint16
}

func copyFrameToClipboard(img *image.NRGBA) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rowStride := ((w*3 + 3) / 4) * 4
	size := 40 + rowStride*h
	data := make([]byte, size)
	binary.LittleEndian.PutUint32(data[0:4], 40)
	binary.LittleEndian.PutUint32(data[4:8], uint32(w))
	binary.LittleEndian.PutUint32(data[8:12], uint32(int32(-h)))
	binary.LittleEndian.PutUint16(data[12:14], 1)
	binary.LittleEndian.PutUint16(data[14:16], 24)
	for y := 0; y < h; y++ {
		row := 40 + y*rowStride
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x+b.Min.X, y+b.Min.Y)
			i := row + x*3
			data[i+0] = c.B
			data[i+1] = c.G
			data[i+2] = c.R
		}
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	hmem, _, err := kernel32.NewProc("GlobalAlloc").Call(0x0002, uintptr(len(data)))
	if hmem == 0 {
		return err
	}
	ptr, _, err := kernel32.NewProc("GlobalLock").Call(hmem)
	if ptr == 0 {
		kernel32.NewProc("GlobalFree").Call(hmem)
		return err
	}
	kernel32.NewProc("RtlMoveMemory").Call(ptr, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	kernel32.NewProc("GlobalUnlock").Call(hmem)

	if !w32.OpenClipboard(0) {
		kernel32.NewProc("GlobalFree").Call(hmem)
		return fmt.Errorf("OpenClipboard failed")
	}
	defer w32.CloseClipboard()
	if !w32.EmptyClipboard() {
		kernel32.NewProc("GlobalFree").Call(hmem)
		return fmt.Errorf("EmptyClipboard failed")
	}
	if w32.SetClipboardData(8, w32.HANDLE(hmem)) == 0 {
		kernel32.NewProc("GlobalFree").Call(hmem)
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

func registerFileAssociations() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	for _, ext := range []string{".gif", ".webp", ".png", ".jpg", ".jpeg"} {
		progID := "Vizlet" + ext[1:]
		k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+progID, registry.SET_VALUE)
		if err != nil {
			return err
		}
		k.SetStringValue("", "Vizlet Image Viewer")
		k.Close()
		k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+progID+`\shell\open\command`, registry.SET_VALUE)
		if err != nil {
			return err
		}
		k.SetStringValue("", fmt.Sprintf(`"%s" "%%1"`, exe))
		k.Close()
		k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+progID+`\DefaultIcon`, registry.SET_VALUE)
		if err != nil {
			return err
		}
		k.SetStringValue("", exe+",0")
		k.Close()
		k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+ext+`\OpenWithProgids`, registry.SET_VALUE)
		if err != nil {
			return err
		}
		k.SetStringValue(progID, "")
		k.Close()
	}
	return nil
}

func isFirstRun() bool {
	marker := filepath.Join(os.TempDir(), "vizlet_firstrun")
	if _, err := os.Stat(marker); err == nil {
		return false
	}
	f, err := os.OpenFile(marker, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func checkLatestRelease() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/WhenMoon-afk/Vizlet/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Vizlet/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("GitHub returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", errors.New("latest release has no tag_name")
	}
	return body.TagName, nil
}

func isUpdateAvailable(latestTag string) bool {
	return Version != "dev" && Version != "unknown" && latestTag != Version
}

func showUpdateDialog() {
	type state int
	const (
		stateChecking state = iota
		stateCurrent
		stateUpdate
		stateError
	)
	sw, sh := w32.GetSystemMetrics(w32.SM_CXSCREEN), w32.GetSystemMetrics(w32.SM_CYSCREEN)
	win := wui.NewWindow()
	win.SetTitle("Vizlet Updates")
	win.SetResizable(false)
	win.SetBackground(wui.Color(0x1c1c1e))
	win.SetInnerBounds((sw-360)/2, (sh-200)/2, 360, 200)
	box := wui.NewPaintBox()
	box.SetBounds(0, 0, 360, 200)
	box.SetAnchors(wui.AnchorMinAndMax, wui.AnchorMinAndMax)
	win.Add(box)
	closeBtn := wui.NewButton()
	closeBtn.SetText("Close")
	closeBtn.SetBounds(130, 148, 100, 32)
	closeBtn.SetVisible(false)
	closeBtn.SetOnClick(win.Close)
	win.Add(closeBtn)
	updateBtn := wui.NewButton()
	updateBtn.SetText("Update Now")
	updateBtn.SetBounds(80, 148, 120, 32)
	updateBtn.SetVisible(false)
	win.Add(updateBtn)
	stateMu := sync.Mutex{}
	curState := stateChecking
	latest, errText := "", ""
	setState := func(s state, tag, err string) {
		stateMu.Lock()
		curState, latest, errText = s, tag, err
		stateMu.Unlock()
		closeBtn.SetVisible(s != stateChecking)
		updateBtn.SetVisible(s == stateUpdate)
		box.Paint()
	}
	updateBtn.SetOnClick(func() {
		updateBtn.SetEnabled(false)
		go selfUpdate(latest)
	})
	box.SetOnPaint(func(c *wui.Canvas) {
		stateMu.Lock()
		s, tag, e := curState, latest, errText
		stateMu.Unlock()
		c.FillRect(0, 0, c.Width(), c.Height(), wui.Color(0x1c1c1e))
		switch s {
		case stateChecking:
			c.TextRectFormat(0, 84, 360, 24, "Checking for updates...", wui.FormatTopCenter, wui.Color(0xffffff))
		case stateCurrent:
			c.FillEllipse(12, 56, 40, 40, wui.Color(0x3a9a3a))
			c.Line(22, 76, 30, 84, wui.Color(0xffffff))
			c.Line(30, 84, 44, 66, wui.Color(0xffffff))
			c.TextOut(64, 68, "Vizlet is up to date", wui.Color(0xffffff))
			c.TextOut(64, 88, Version, wui.Color(0x888888))
		case stateUpdate:
			c.FillEllipse(12, 56, 40, 40, wui.Color(0xc8860a))
			c.Line(32, 66, 32, 86, wui.Color(0xffffff))
			c.Line(32, 66, 24, 76, wui.Color(0xffffff))
			c.Line(32, 66, 40, 76, wui.Color(0xffffff))
			c.TextOut(64, 68, "Update available: "+tag, wui.Color(0xffffff))
			c.TextOut(64, 88, "Current: "+Version, wui.Color(0x888888))
		case stateError:
			c.FillEllipse(12, 56, 40, 40, wui.Color(0xc83a3a))
			c.TextOut(29, 64, "!", wui.Color(0xffffff))
			c.TextOut(64, 68, "Could not check for updates", wui.Color(0xffffff))
			if len(e) > 42 {
				e = e[:42] + "..."
			}
			c.TextOut(64, 88, e, wui.Color(0x888888))
		}
	})
	go func() {
		tag, err := checkLatestRelease()
		if err != nil {
			setState(stateError, "", err.Error())
		} else if isUpdateAvailable(tag) {
			setState(stateUpdate, tag, "")
		} else {
			setState(stateCurrent, tag, "")
		}
	}()
	_ = win.Show()
}

func selfUpdate(newTag string) {
	sw, sh := w32.GetSystemMetrics(w32.SM_CXSCREEN), w32.GetSystemMetrics(w32.SM_CYSCREEN)
	win := wui.NewWindow()
	win.SetTitle("Updating Vizlet...")
	win.SetResizable(false)
	win.SetBackground(wui.Color(0x1c1c1e))
	win.SetInnerBounds((sw-320)/2, (sh-100)/2, 320, 100)
	box := wui.NewPaintBox()
	box.SetBounds(0, 0, 320, 100)
	win.Add(box)
	mu := sync.Mutex{}
	label := "Downloading..."
	box.SetOnPaint(func(c *wui.Canvas) {
		mu.Lock()
		text := label
		mu.Unlock()
		c.FillRect(0, 0, c.Width(), c.Height(), wui.Color(0x1c1c1e))
		c.TextRectFormat(0, 40, 320, 24, text, wui.FormatTopCenter, wui.Color(0xffffff))
	})
	setLabel := func(s string) {
		mu.Lock()
		label = s
		mu.Unlock()
		box.Paint()
	}
	go func() {
		if err := doSelfUpdate(newTag, setLabel); err != nil {
			win.Close()
			wui.MessageBoxError("Vizlet", err.Error())
		}
	}()
	_ = win.Show()
}

func doSelfUpdate(newTag string, setLabel func(string)) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	newPath := filepath.Join(dir, "vizlet-new.exe")
	oldPath := filepath.Join(dir, "vizlet-old.exe")
	url := "https://github.com/WhenMoon-afk/Vizlet/releases/download/" + newTag + "/vizlet.exe"
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	out, err := os.Create(newPath)
	if err != nil {
		return err
	}
	defer out.Close()
	var written int64
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return err
			}
			written += int64(n)
			if resp.ContentLength > 0 {
				setLabel(fmt.Sprintf("Downloading... %d%%", written*100/resp.ContentLength))
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	_ = os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil {
		_ = os.Remove(newPath)
		return err
	}
	if err := os.Rename(newPath, exe); err != nil {
		_ = os.Rename(oldPath, exe)
		return err
	}
	if err := exec.Command(exe).Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
