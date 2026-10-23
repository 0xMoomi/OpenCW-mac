package main

import (
	"image"
	imagecolor "image/color"
	"math"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

// Design tokens. Warm neutrals carry the layout; maple orange is the one
// brand colour (the switch, progress and primary action). Every text colour
// meets 4.5:1 on its background.
var (
	canvasBg   = rgb(0xF6, 0xF4, 0xF0)
	cardBg     = rgb(0xFF, 0xFF, 0xFF)
	border     = rgb(0xE0, 0xDB, 0xD2)
	divider    = rgb(0xEC, 0xE8, 0xE1)
	ink        = rgb(0x1C, 0x19, 0x15)
	muted      = rgb(0x5F, 0x58, 0x4F) // 6.6:1 on canvas
	brand      = rgb(0xE2, 0x62, 0x1B) // fills only (switch, bar)
	brandHover = rgb(0xD1, 0x57, 0x15)
	brandText  = rgb(0xC4, 0x4F, 0x0F) // white on this is 4.6:1
	brandDown  = rgb(0xA9, 0x43, 0x0B)
	trackOff   = rgb(0xDB, 0xD5, 0xCB)
	trackHover = rgb(0xCF, 0xC8, 0xBC)
	barTrack   = rgb(0xE7, 0xE2, 0xDA)
	good       = rgb(0x13, 0x80, 0x4A)
	fair       = rgb(0x9A, 0x62, 0x00)
	bad        = rgb(0xB3, 0x26, 0x1E)
	white      = rgb(0xFF, 0xFF, 0xFF)
	black      = rgb(0x00, 0x00, 0x00)
	focusRing  = ink.alpha(.6) // keyboard focus outline
)

const uiFont = "Malgun Gothic"

var (
	fontHeadline   = fontSpec{uiFont, 28, 700}
	fontBody       = fontSpec{uiFont, 15, 400}
	fontSmall      = fontSpec{uiFont, 13, 400}
	fontPing       = fontSpec{uiFont, 30, 700}
	fontButton     = fontSpec{uiFont, 15, 700}
	fontLink       = fontSpec{uiFont, 14, 400}
	fontRowKey     = fontSpec{uiFont, 14, 400}
	fontRowValue   = fontSpec{uiFont, 14, 700}
	fontPageHead   = fontSpec{uiFont, 20, 700}
	fontDialogHead = fontSpec{uiFont, 19, 700}
)

// The leaf is drawn comic-style in a 100×100 box: five fat, smooth lobes
// around a round core, plus a stubby stem.
type lobe struct{ angle, length, width float64 }

var (
	leafLobes   = []lobe{{0, 50, 30}, {-70, 46, 28}, {70, 46, 28}, {-135, 32, 22}, {135, 32, 22}}
	leafCenter  = point{50, 52}
	lobeProfile = [][2]float64{{-.36, .1}, {-.5, -.45}, {-.2, -.84}, {0, -1}, {.2, -.84}, {.5, -.45}, {.36, .1}}
)

const leafCore, leafTension = 12.0, .45

// leaf draws the leaf centred at cx,cy; skew leans it forward.
func (p *painter) leaf(cx, cy, size, skew float64, c color) {
	at := func(x, y float64) point {
		x, y = (x-50)/100*size, (y-50)/100*size
		return point{cx + x - y*skew, cy + y}
	}
	for _, l := range leafLobes {
		sin, cos := math.Sincos(l.angle * math.Pi / 180)
		points := make([]point, len(lobeProfile))
		for i, q := range lobeProfile {
			x, y := q[0]*l.width, q[1]*l.length
			points[i] = at(leafCenter.x+x*cos-y*sin, leafCenter.y+x*sin+y*cos)
		}
		p.fillPath(p.curvePath(points, leafTension), solid(c))
	}
	core := make([]point, 12)
	for i := range core {
		a := float64(i) / float64(len(core)) * 2 * math.Pi
		core[i] = at(leafCenter.x+leafCore*math.Cos(a), leafCenter.y+leafCore*math.Sin(a))
	}
	p.fillPath(p.curvePath(core, .5), solid(c))
	stem := []point{at(leafCenter.x, leafCenter.y+leafCore*.6), at(leafCenter.x+1, 78), at(leafCenter.x+3, 90)}
	p.strokePath(p.polylinePath(stem), c, size*.07)
}

// toggle is the main control: the switch is both the action and the state.
// knob runs 0 (off) to 1 (on); busy shows a spinner inside the knob.
func (p *painter) toggle(id string, r rect, knob float64, on, busy, enabled bool, t float64) {
	st := p.spot(id, r, true, enabled)
	radius := r.h / 2
	trackColor := mix(trackOff, brand, knob)
	switch {
	case st.hover && on:
		trackColor = brandHover
	case st.hover:
		trackColor = trackHover
	}
	if !enabled {
		trackColor = mix(trackColor, canvasBg, .45)
	}
	p.fillRound(r, radius, trackColor)
	if st.focus {
		p.strokeRound(r.inset(-4), radius+4, 2, focusRing)
	}
	d := r.h - 12
	if st.pressed {
		d -= 2
	}
	cx := r.x + 6 + d/2 + (r.w-12-d)*knob
	cy := r.y + r.h/2
	p.fillCircle(cx, cy+1.5, d/2+.5, black.alpha(.10))
	p.fillCircle(cx, cy, d/2, white)
	if busy {
		p.arc(cx, cy, d*.22, 3, math.Mod(t*360, 360), 260, brand)
		return
	}
	mark := trackOff
	if on {
		mark = brand
	}
	p.leaf(cx, cy+1, d*.5, .1, mark)
}

// button is a quiet secondary action: white face, hairline border.
func (p *painter) button(id string, r rect, label string, enabled bool) {
	st := p.spot(id, r, false, enabled)
	face, edge, fg := cardBg, border, ink
	switch {
	case !enabled:
		face, fg = canvasBg, muted
	case st.pressed:
		face = rgb(0xEF, 0xEC, 0xE6)
	case st.hover:
		face, edge = rgb(0xF8, 0xF6, 0xF2), rgb(0xC9, 0xC2, 0xB6)
	}
	p.fillRound(r, 8, face)
	p.strokeRound(r.inset(.5), 8, 1, edge)
	if st.focus {
		p.strokeRound(r.inset(-3), 10, 2, focusRing)
	}
	p.text(label, fontButton, fg, r, textCenter)
}

// primary is the filled brand action (used where a dialog needs a default).
func (p *painter) primary(id string, r rect, label string) {
	st := p.spot(id, r, false, true)
	face := brandText
	switch {
	case st.pressed:
		face = brandDown
	case st.hover:
		face = mix(brandText, brandDown, .5)
	}
	p.fillRound(r, 8, face)
	if st.focus {
		p.strokeRound(r.inset(-3), 10, 2, focusRing)
	}
	p.text(label, fontButton, white, r, textCenter)
}

// link is a text action starting at x.
func (p *painter) link(id string, x, y float64, label string) {
	w := p.measure(label, fontLink)
	r := rect{x - 6, y, w + 12, 30}
	st := p.register(spot{id: id, r: r, enabled: true, link: true})
	c := muted
	if st.hover || st.focus {
		c = ink
		p.line(point{x, r.y + 24}, point{x + w, r.y + 24}, 1, c.alpha(.7))
	}
	if st.focus {
		p.strokeRound(r, 6, 1.5, ink.alpha(.5))
	}
	p.text(label, fontLink, c, rect{x, r.y, w + 2, r.h}, textLeft)
}

// iconButton is a round, label-free control for secondary navigation.
func (p *painter) iconButton(id string, cx, cy float64, icon func(cx, cy float64, c color)) {
	const radius = 18.0
	st := p.spot(id, circleRect(cx, cy, radius), true, true)
	c := muted
	switch {
	case st.pressed:
		p.fillCircle(cx, cy, radius, rgb(0xE2, 0xDD, 0xD4))
		c = ink
	case st.hover:
		p.fillCircle(cx, cy, radius, rgb(0xEC, 0xE8, 0xE1))
		c = ink
	}
	if st.focus {
		p.strokeCircle(cx, cy, radius+2, 2, focusRing)
	}
	icon(cx, cy, c)
}

func (p *painter) infoIcon(cx, cy float64, c color) {
	p.strokeCircle(cx, cy, 9.5, 1.6, c)
	p.fillCircle(cx, cy-4.2, 1.35, c)
	p.line(point{cx, cy - 1}, point{cx, cy + 4.6}, 1.9, c)
}

func (p *painter) closeIcon(cx, cy float64, c color) {
	p.line(point{cx - 5.5, cy - 5.5}, point{cx + 5.5, cy + 5.5}, 1.8, c)
	p.line(point{cx + 5.5, cy - 5.5}, point{cx - 5.5, cy + 5.5}, 1.8, c)
}

// signalBars draws four rising bars with the first lit ones in c.
func (p *painter) signalBars(r rect, lit int, c color) {
	const count = 4
	gap := r.w * .12
	w := (r.w - gap*(count-1)) / count
	for i := range count {
		h := r.h * (.34 + .22*float64(i))
		bar := rect{r.x + float64(i)*(w+gap), r.y + r.h - h, w, h}
		fill := barTrack
		if i < lit {
			fill = c
		}
		p.fillRound(bar, 1.5, fill)
	}
}

// progress fills value (0 to 1) of the bar, or sweeps an indeterminate
// segment when value is negative.
func (p *painter) progress(r rect, value, t float64) {
	p.fillRound(r, r.h/2, barTrack)
	if value >= 0 {
		p.fillRound(rect{r.x, r.y, math.Max(r.h, r.w*math.Min(1, value)), r.h}, r.h/2, brand)
		return
	}
	span := r.w * .3
	x := r.x - span + math.Mod(t*.7, 1)*(r.w+span)
	left, right := math.Max(r.x, x), math.Min(r.x+r.w, x+span)
	if right > left {
		p.fillRound(rect{left, r.y, right - left, r.h}, r.h/2, brand)
	}
}

// Icon palette: a charcoal tile, a red leaf, and three flame tones.
var (
	iconTile   = rgb(0x2B, 0x23, 0x1D)
	iconLeaf   = rgb(0xB9, 0x2B, 0x1A)
	flameOuter = rgb(0xF2, 0x6A, 0x1B)
	flameMid   = rgb(0xFF, 0x9E, 0x2C)
	flameCore  = rgb(0xFF, 0xD3, 0x4E)
)

// flameOutline is one cartoon flame in a 100×100 box: round belly, a tall
// centre tip and two smaller side tips, base at (50,96).
var flameOutline = []point{
	{50, 96}, {74, 92}, {88, 74}, {86, 52}, {76, 28}, {68, 44}, {62, 30},
	{50, 4}, {40, 26}, {34, 44}, {26, 36}, {16, 54}, {14, 74}, {28, 92},
}

// flame draws the outline three times, shrinking toward the base: outer,
// middle and core tones.
func (p *painter) flame(bx, by, size float64) {
	for _, layer := range []struct {
		scale float64
		c     color
	}{{1, flameOuter}, {.66, flameMid}, {.36, flameCore}} {
		points := make([]point, len(flameOutline))
		for i, q := range flameOutline {
			points[i] = point{bx + (q.x-50)/100*size*layer.scale, by + (q.y-96)/100*size*layer.scale}
		}
		p.fillPath(p.curvePath(points, .4), solid(layer.c))
	}
}

// drawAppMark is the icon: a leaf on fire. The leaf is a deeper red than the
// outer flame so it stays distinct without an outline.
func drawAppMark(p *painter, s float64) {
	p.fillRound(rect{0, 0, s, s}, s*.22, iconTile)
	p.flame(s*.5, s*.9, s*.8)
	p.leaf(s*.5, s*.68, s*.5, .06, iconLeaf)
}

// appIcon renders the app mark as a size×size icon.
func appIcon(size int) (*walk.Icon, error) {
	img, err := renderImage(size, size, func(p *painter) { drawAppMark(p, float64(size)) })
	if err != nil {
		return nil, err
	}
	return walk.NewIconFromImageForDPI(img, 96)
}

// setWindowIcon uses the EXE's multi-size icon so the 16px title-bar icon
// is a real 16px drawing, not a downscaled 32px one.
func setWindowIcon(form walk.Form) {
	hwnd := form.Handle()
	instance := win.GetModuleHandle(nil)
	dpi := uint32(form.DPI())
	loaded := false
	for _, size := range []struct {
		which  uintptr
		metric int32
	}{{0 /* ICON_SMALL */, win.SM_CXSMICON}, {1 /* ICON_BIG */, win.SM_CXICON}} {
		px := win.GetSystemMetricsForDpi(size.metric, dpi)
		icon := win.LoadImage(instance, win.MAKEINTRESOURCE(2), win.IMAGE_ICON, px, px, 0)
		if icon != 0 {
			win.SendMessage(hwnd, win.WM_SETICON, size.which, uintptr(icon))
			loaded = true
		}
	}
	if !loaded {
		if icon, err := appIcon(32); err == nil {
			form.SetIcon(icon)
		}
	}
}

// renderImage draws into a premultiplied GDI+ bitmap so the result keeps a
// real alpha channel (an HDC-backed GDI surface would drop it).
func renderImage(width, height int, draw func(*painter)) (image.Image, error) {
	if !startGDIPlus() {
		return nil, windows.ERROR_NOT_SUPPORTED
	}
	buf := make([]byte, width*height*4)
	var bitmap, graphics uintptr
	gdipCreateBitmapFromScan0.Call(uintptr(width), uintptr(height), uintptr(width*4), pixelFormat32bppPARGB,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bitmap)))
	if bitmap == 0 {
		return nil, windows.ERROR_NOT_SUPPORTED
	}
	defer gdipDisposeImage.Call(bitmap)
	gdipGetImageGraphics.Call(bitmap, uintptr(unsafe.Pointer(&graphics)))
	if graphics == 0 {
		return nil, windows.ERROR_NOT_SUPPORTED
	}
	prepareGraphics(graphics)
	p := &painter{scale: 1, dpi: 96, graphics: graphics}
	draw(p)
	p.flush()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := range width * height {
		b, g, r, a := buf[i*4], buf[i*4+1], buf[i*4+2], buf[i*4+3]
		if a > 0 && a < 255 {
			r = uint8(min(255, int(r)*255/int(a)))
			g = uint8(min(255, int(g)*255/int(a)))
			b = uint8(min(255, int(b)*255/int(a)))
		}
		img.SetNRGBA(i%width, i/width, imagecolor.NRGBA{R: r, G: g, B: b, A: a})
	}
	return img, nil
}

var dwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")

// themeWindow matches the Windows 11 title bar to the window body so the
// frame and content read as one surface. Older Windows ignores this.
func themeWindow(hwnd win.HWND, caption color) {
	set := func(attr uintptr, value uint32) {
		dwmSetWindowAttribute.Call(uintptr(hwnd), attr, uintptr(unsafe.Pointer(&value)), 4)
	}
	set(35, caption.colorref()) // DWMWA_CAPTION_COLOR
	set(36, ink.colorref())     // DWMWA_TEXT_COLOR
}

// fixWindow makes a non-resizable window of the given client size, centred
// on the monitor's work area.
func fixWindow(form walk.Form, width, height int) {
	hwnd := form.Handle()
	style := win.GetWindowLong(hwnd, win.GWL_STYLE)
	win.SetWindowLong(hwnd, win.GWL_STYLE, style&^(win.WS_MAXIMIZEBOX|win.WS_THICKFRAME))
	win.SetWindowPos(hwnd, 0, 0, 0, 0, 0,
		win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_FRAMECHANGED|win.SWP_NOACTIVATE)
	form.SetClientSize(walk.Size{Width: width, Height: height})
	var bounds win.RECT
	win.GetWindowRect(hwnd, &bounds)
	info := win.MONITORINFO{CbSize: uint32(unsafe.Sizeof(win.MONITORINFO{}))}
	if win.GetMonitorInfo(win.MonitorFromWindow(hwnd, win.MONITOR_DEFAULTTONEAREST), &info) {
		area := info.RcWork
		w, h := bounds.Right-bounds.Left, bounds.Bottom-bounds.Top
		x := area.Left + (area.Right-area.Left-w)/2
		y := area.Top + (area.Bottom-area.Top-h)/2
		win.SetWindowPos(hwnd, 0, x, y, w, h, win.SWP_NOZORDER|win.SWP_FRAMECHANGED|win.SWP_NOACTIVATE)
	}
}
