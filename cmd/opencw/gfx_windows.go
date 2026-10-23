package main

import (
	"math"
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

// GDI+ draws the anti-aliased shapes; GDI draws text so Korean glyphs keep
// native ClearType rendering. Everything is laid out in 96-DPI units and
// scaled once here.

var (
	gdiplus                   = windows.NewLazySystemDLL("gdiplus.dll")
	gdipStartup               = gdiplus.NewProc("GdiplusStartup")
	gdipCreateFromHDC         = gdiplus.NewProc("GdipCreateFromHDC")
	gdipDeleteGraphics        = gdiplus.NewProc("GdipDeleteGraphics")
	gdipSetSmoothingMode      = gdiplus.NewProc("GdipSetSmoothingMode")
	gdipSetPixelOffsetMode    = gdiplus.NewProc("GdipSetPixelOffsetMode")
	gdipCreateSolidFill       = gdiplus.NewProc("GdipCreateSolidFill")
	gdipDeleteBrush           = gdiplus.NewProc("GdipDeleteBrush")
	gdipCreatePen             = gdiplus.NewProc("GdipCreatePen1")
	gdipSetPenStartCap        = gdiplus.NewProc("GdipSetPenStartCap")
	gdipSetPenEndCap          = gdiplus.NewProc("GdipSetPenEndCap")
	gdipSetPenLineJoin        = gdiplus.NewProc("GdipSetPenLineJoin")
	gdipDeletePen             = gdiplus.NewProc("GdipDeletePen")
	gdipCreatePath            = gdiplus.NewProc("GdipCreatePath")
	gdipDeletePath            = gdiplus.NewProc("GdipDeletePath")
	gdipAddPathArc            = gdiplus.NewProc("GdipAddPathArc")
	gdipAddPathEllipse        = gdiplus.NewProc("GdipAddPathEllipse")
	gdipAddPathPolygon        = gdiplus.NewProc("GdipAddPathPolygon")
	gdipAddPathLine           = gdiplus.NewProc("GdipAddPathLine")
	gdipAddPathClosedCurve2   = gdiplus.NewProc("GdipAddPathClosedCurve2")
	gdipClosePathFigure       = gdiplus.NewProc("GdipClosePathFigure")
	gdipFillPath              = gdiplus.NewProc("GdipFillPath")
	gdipDrawPath              = gdiplus.NewProc("GdipDrawPath")
	gdipCreateBitmapFromScan0 = gdiplus.NewProc("GdipCreateBitmapFromScan0")
	gdipGetImageGraphics      = gdiplus.NewProc("GdipGetImageGraphicsContext")
	gdipDisposeImage          = gdiplus.NewProc("GdipDisposeImage")
)

// GDI+ enumeration values.
const (
	fillModeAlternate      = 0
	unitPixel              = 2
	lineCapRound           = 2
	lineJoinRound          = 2
	smoothingModeAntiAlias = 4
	pixelOffsetHighQuality = 2
	pixelFormat32bppPARGB  = 0xE200B
)

var (
	gdipOnce  sync.Once
	gdipReady bool
)

// startGDIPlus initialises GDI+ once per process and reports whether it is
// available.
func startGDIPlus() bool {
	gdipOnce.Do(func() {
		input := struct {
			Version                  uint32
			DebugEventCallback       uintptr
			SuppressBackgroundThread int32
			SuppressExternalCodecs   int32
		}{Version: 1}
		var token uintptr
		status, _, _ := gdipStartup.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&input)), 0)
		gdipReady = status == 0
	})
	return gdipReady
}

// prepareGraphics sets the anti-aliasing every GDI+ drawing surface uses.
func prepareGraphics(graphics uintptr) {
	gdipSetSmoothingMode.Call(graphics, smoothingModeAntiAlias)
	gdipSetPixelOffsetMode.Call(graphics, pixelOffsetHighQuality)
}

// f32 passes a float through syscall; the Go Windows ABI shim mirrors the
// first four integer registers into XMM0-3 and float32 reads the low half.
func f32(v float64) uintptr { return uintptr(math.Float32bits(float32(v))) }

// color is a straight-alpha ARGB value, the layout GDI+ takes.
type color uint32

func rgb(r, g, b uint8) color { return color(0xFF000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)) }

func (c color) a() float64 { return float64(c>>24) / 255 }

func (c color) r() uint8 { return uint8(c >> 16) }

func (c color) g() uint8 { return uint8(c >> 8) }

func (c color) b() uint8 { return uint8(c) }

// alpha multiplies the existing opacity.
func (c color) alpha(v float64) color {
	v = math.Max(0, math.Min(1, v*c.a()))
	return color(uint32(math.Round(v*255))<<24 | uint32(c)&0xFFFFFF)
}

// colorref converts to the GDI 0x00BBGGRR layout, dropping alpha.
func (c color) colorref() uint32 {
	return uint32(c.r()) | uint32(c.g())<<8 | uint32(c.b())<<16
}

// mix blends a toward b by t in [0, 1], alpha included.
func mix(a, b color, t float64) color {
	t = math.Max(0, math.Min(1, t))
	lerp := func(x, y uint8) uint32 { return uint32(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	alpha := uint32(math.Round((a.a() + (b.a()-a.a())*t) * 255))
	return color(alpha<<24 | lerp(a.r(), b.r())<<16 | lerp(a.g(), b.g())<<8 | lerp(a.b(), b.b()))
}

// rect is a box in 96-DPI units.
type rect struct{ x, y, w, h float64 }

func (r rect) inset(d float64) rect { return rect{r.x + d, r.y + d, r.w - 2*d, r.h - 2*d} }

func (r rect) contains(x, y float64) bool {
	return x >= r.x && y >= r.y && x < r.x+r.w && y < r.y+r.h
}

func circleRect(cx, cy, radius float64) rect {
	return rect{cx - radius, cy - radius, radius * 2, radius * 2}
}

type point struct{ x, y float64 }

type fontSpec struct {
	family string
	size   float64 // pixels at 96 DPI
	weight int32
}

// DrawText formats.
const (
	textLeft          = win.DT_LEFT | win.DT_SINGLELINE | win.DT_VCENTER | win.DT_NOPREFIX | win.DT_END_ELLIPSIS
	textRight         = win.DT_RIGHT | win.DT_SINGLELINE | win.DT_VCENTER | win.DT_NOPREFIX
	textCenter        = win.DT_CENTER | win.DT_SINGLELINE | win.DT_VCENTER | win.DT_NOPREFIX | win.DT_END_ELLIPSIS
	textParagraphLeft = win.DT_LEFT | win.DT_WORDBREAK | win.DT_NOPREFIX
)

type fontKey struct {
	fontSpec
	dpi int
}

var fonts = struct {
	sync.Mutex
	cache map[fontKey]win.HFONT
}{cache: map[fontKey]win.HFONT{}}

// fontHandle returns a cached GDI font. Handles live for the process; the
// set of specs × DPIs is tiny.
func fontHandle(spec fontSpec, dpi int) win.HFONT {
	fonts.Lock()
	defer fonts.Unlock()
	key := fontKey{spec, dpi}
	if h, ok := fonts.cache[key]; ok {
		return h
	}
	lf := win.LOGFONT{
		LfHeight:  -int32(math.Round(spec.size * float64(dpi) / 96)),
		LfWeight:  spec.weight,
		LfCharSet: win.DEFAULT_CHARSET,
		LfQuality: win.CLEARTYPE_QUALITY,
	}
	name, _ := windows.UTF16FromString(spec.family)
	copy(lf.LfFaceName[:len(lf.LfFaceName)-1], name)
	h := win.CreateFontIndirect(&lf)
	fonts.cache[key] = h
	return h
}

// painter draws one frame in 96-DPI units and records the frame's clickable
// spots for the surface.
type painter struct {
	hdc      win.HDC
	scale    float64
	dpi      int
	graphics uintptr
	ui       *surface
	spots    []spot
}

func newPainter(hdc win.HDC, dpi int, ui *surface) *painter {
	startGDIPlus()
	return &painter{hdc: hdc, dpi: dpi, scale: float64(dpi) / 96, ui: ui}
}

// px converts 96-DPI units to device pixels.
func (p *painter) px(v float64) float64 { return v * p.scale }

// context opens a GDI+ context lazily. Text calls close it first, because
// GDI+ batches drawing and must flush before GDI touches the same DC.
func (p *painter) context() uintptr {
	if p.graphics == 0 && gdipReady {
		gdipCreateFromHDC.Call(uintptr(p.hdc), uintptr(unsafe.Pointer(&p.graphics)))
		if p.graphics != 0 {
			prepareGraphics(p.graphics)
		}
	}
	return p.graphics
}

func (p *painter) flush() {
	if p.graphics != 0 {
		gdipDeleteGraphics.Call(p.graphics)
		p.graphics = 0
	}
}

// solid creates a solid brush; fillPath frees it.
func solid(c color) uintptr {
	var brush uintptr
	gdipCreateSolidFill.Call(uintptr(c), uintptr(unsafe.Pointer(&brush)))
	return brush
}

func newPath() uintptr {
	var path uintptr
	gdipCreatePath.Call(fillModeAlternate, uintptr(unsafe.Pointer(&path)))
	return path
}

func (p *painter) roundPath(r rect, radius float64) uintptr {
	path := newPath()
	x, y, w, h := p.px(r.x), p.px(r.y), p.px(r.w), p.px(r.h)
	d := math.Min(p.px(radius)*2, math.Min(w, h))
	if d <= 0.5 {
		corners := [4][2]float32{
			{float32(x), float32(y)},
			{float32(x + w), float32(y)},
			{float32(x + w), float32(y + h)},
			{float32(x), float32(y + h)},
		}
		gdipAddPathPolygon.Call(path, uintptr(unsafe.Pointer(&corners)), 4)
		return path
	}
	gdipAddPathArc.Call(path, f32(x), f32(y), f32(d), f32(d), f32(180), f32(90))
	gdipAddPathArc.Call(path, f32(x+w-d), f32(y), f32(d), f32(d), f32(270), f32(90))
	gdipAddPathArc.Call(path, f32(x+w-d), f32(y+h-d), f32(d), f32(d), f32(0), f32(90))
	gdipAddPathArc.Call(path, f32(x), f32(y+h-d), f32(d), f32(d), f32(90), f32(90))
	gdipClosePathFigure.Call(path)
	return path
}

func (p *painter) ellipsePath(r rect) uintptr {
	path := newPath()
	gdipAddPathEllipse.Call(path, f32(p.px(r.x)), f32(p.px(r.y)), f32(p.px(r.w)), f32(p.px(r.h)))
	return path
}

// curvePath is a closed cardinal spline through points; higher tension
// rounds the corners more.
func (p *painter) curvePath(points []point, tension float64) uintptr {
	path := newPath()
	if len(points) < 3 {
		return path
	}
	raw := make([][2]float32, len(points))
	for i, pt := range points {
		raw[i] = [2]float32{float32(p.px(pt.x)), float32(p.px(pt.y))}
	}
	gdipAddPathClosedCurve2.Call(path, uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw)), f32(tension))
	return path
}

func (p *painter) polylinePath(points []point) uintptr {
	path := newPath()
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		gdipAddPathLine.Call(path, f32(p.px(a.x)), f32(p.px(a.y)), f32(p.px(b.x)), f32(p.px(b.y)))
	}
	return path
}

// fillPath fills path with brush, then frees both.
func (p *painter) fillPath(path, brush uintptr) {
	if g := p.context(); g != 0 && path != 0 && brush != 0 {
		gdipFillPath.Call(g, brush, path)
	}
	gdipDeleteBrush.Call(brush)
	gdipDeletePath.Call(path)
}

// pen is a round-capped, round-joined pen of width units.
func (p *painter) pen(c color, width float64) uintptr {
	var pen uintptr
	gdipCreatePen.Call(uintptr(c), f32(p.px(width)), unitPixel, uintptr(unsafe.Pointer(&pen)))
	gdipSetPenStartCap.Call(pen, lineCapRound)
	gdipSetPenEndCap.Call(pen, lineCapRound)
	gdipSetPenLineJoin.Call(pen, lineJoinRound)
	return pen
}

// strokePath outlines path, then frees it.
func (p *painter) strokePath(path uintptr, c color, width float64) {
	pen := p.pen(c, width)
	if g := p.context(); g != 0 && path != 0 && pen != 0 {
		gdipDrawPath.Call(g, pen, path)
	}
	gdipDeletePen.Call(pen)
	gdipDeletePath.Call(path)
}

func (p *painter) fillRound(r rect, radius float64, c color) {
	p.fillPath(p.roundPath(r, radius), solid(c))
}

func (p *painter) strokeRound(r rect, radius, width float64, c color) {
	p.strokePath(p.roundPath(r, radius), c, width)
}

func (p *painter) fillCircle(cx, cy, radius float64, c color) {
	p.fillPath(p.ellipsePath(circleRect(cx, cy, radius)), solid(c))
}

func (p *painter) strokeCircle(cx, cy, radius, width float64, c color) {
	p.strokePath(p.ellipsePath(circleRect(cx, cy, radius)), c, width)
}

// arc strokes clockwise from start degrees (0 = 3 o'clock).
func (p *painter) arc(cx, cy, radius, width, start, sweep float64, c color) {
	path := newPath()
	r := circleRect(cx, cy, radius)
	gdipAddPathArc.Call(path, f32(p.px(r.x)), f32(p.px(r.y)), f32(p.px(r.w)), f32(p.px(r.h)), f32(start), f32(sweep))
	p.strokePath(path, c, width)
}

func (p *painter) line(a, b point, width float64, c color) {
	p.strokePath(p.polylinePath([]point{a, b}), c, width)
}

func (p *painter) pixelRect(r rect) win.RECT {
	return win.RECT{
		Left:   int32(math.Round(p.px(r.x))),
		Top:    int32(math.Round(p.px(r.y))),
		Right:  int32(math.Round(p.px(r.x + r.w))),
		Bottom: int32(math.Round(p.px(r.y + r.h))),
	}
}

func (p *painter) text(s string, font fontSpec, c color, r rect, format uint32) {
	if s == "" {
		return
	}
	p.flush()
	old := win.SelectObject(p.hdc, win.HGDIOBJ(fontHandle(font, p.dpi)))
	defer win.SelectObject(p.hdc, old)
	win.SetBkMode(p.hdc, win.TRANSPARENT)
	win.SetTextColor(p.hdc, win.COLORREF(c.colorref()))
	// GDI takes an explicit length, so the text needs no terminator and a NUL
	// in feed-supplied text cannot fail the conversion.
	chars := utf16.Encode([]rune(s))
	box := p.pixelRect(r)
	win.DrawTextEx(p.hdc, &chars[0], int32(len(chars)), &box, format, nil)
}

// measure returns the width in 96-DPI units.
func (p *painter) measure(s string, font fontSpec) float64 {
	if s == "" {
		return 0
	}
	p.flush()
	old := win.SelectObject(p.hdc, win.HGDIOBJ(fontHandle(font, p.dpi)))
	defer win.SelectObject(p.hdc, old)
	chars := utf16.Encode([]rune(s))
	var size win.SIZE
	win.GetTextExtentPoint32(p.hdc, &chars[0], int32(len(chars)), &size)
	return float64(size.CX) / p.scale
}
