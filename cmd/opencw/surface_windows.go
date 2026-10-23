package main

import (
	"time"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const (
	surfaceClass = `OpenCW_Surface`

	// activationCooldown ignores a second activation this soon after the
	// first, so a double-click or a doubled Enter cannot connect and then
	// immediately undo it.
	activationCooldown = 450 * time.Millisecond

	animationTimer = 1 // WM_TIMER id
)

var notifyWinEvent = user32.NewProc("NotifyWinEvent")

func init() {
	walk.AppendToWalkInit(func() {
		walk.MustRegisterWindowClassWithStyle(surfaceClass, win.CS_HREDRAW|win.CS_VREDRAW)
	})
}

// spot is a clickable region recorded while painting (immediate-mode UI).
type spot struct {
	id      string
	r       rect
	round   bool
	enabled bool
	link    bool // text links get the hand cursor; buttons keep the arrow
}

func (s spot) hit(x, y float64) bool {
	if !s.r.contains(x, y) {
		return false
	}
	if !s.round {
		return true
	}
	cy, radius := s.r.y+s.r.h/2, s.r.h/2
	// Stadium shape: a pill hit-tests by distance to its centre segment.
	dx := 0.0
	if x < s.r.x+radius {
		dx = s.r.x + radius - x
	} else if x > s.r.x+s.r.w-radius {
		dx = x - (s.r.x + s.r.w - radius)
	}
	return dx*dx+(y-cy)*(y-cy) <= radius*radius
}

// buttonState is how a control should look in the current frame.
type buttonState struct{ hover, pressed, focus bool }

// surface is one fully custom-painted widget. The whole window is drawn by a
// scene function, so there are no stock controls to theme.
type surface struct {
	walk.WidgetBase
	scene      func(p *painter, width, height float64)
	onClick    func(id string)
	onEscape   func()
	frames     func() time.Duration // repaint interval wanted after a frame
	spots      []spot
	hover      string
	pressed    string
	focus      string
	keyboard   bool
	tracking   bool
	interval   time.Duration
	started    time.Time
	lastAction time.Time
	name       string
	buffer     win.HDC
	bitmap     win.HBITMAP
	previous   win.HGDIOBJ
	bufferW    int32
	bufferH    int32
}

func newSurface(parent walk.Container, scene func(*painter, float64, float64), onClick func(string)) (*surface, error) {
	s := &surface{scene: scene, onClick: onClick, started: time.Now()}
	if err := walk.InitWidget(s, parent, surfaceClass, win.WS_VISIBLE|win.WS_TABSTOP, 0); err != nil {
		return nil, err
	}
	return s, nil
}

func (*surface) CreateLayoutItem(*walk.LayoutContext) walk.LayoutItem {
	return walk.NewGreedyLayoutItem()
}

// elapsed drives animations from one clock so every frame is consistent.
func (s *surface) elapsed() time.Duration {
	return time.Since(s.started)
}

// animate repaints on a timer only while something moves; a connected,
// idle window costs nothing while the game runs.
func (s *surface) animate(interval time.Duration) {
	if s.interval == interval {
		return
	}
	s.interval = interval
	if interval == 0 {
		win.KillTimer(s.Handle(), animationTimer)
		return
	}
	win.SetTimer(s.Handle(), animationTimer, uint32(interval/time.Millisecond), 0)
}

// setName exposes the current state as the window text, which screen readers
// announce for this single custom control.
func (s *surface) setName(name string) {
	if s.name == name {
		return
	}
	s.name = name
	text, _ := windows.UTF16PtrFromString(name)
	win.SendMessage(s.Handle(), win.WM_SETTEXT, 0, uintptr(unsafe.Pointer(text)))
	const eventObjectNameChange, objidClient = 0x800C, 0xFFFFFFFC
	notifyWinEvent.Call(eventObjectNameChange, uintptr(s.Handle()), objidClient, 0)
}

func (s *surface) render(hdc win.HDC, width, height int32) {
	p := newPainter(hdc, s.DPI(), s)
	s.scene(p, float64(width)/p.scale, float64(height)/p.scale)
	p.flush()
	s.spots = p.spots
	if s.focus != "" && !s.enabled(s.focus) {
		s.focus = ""
	}
	if s.frames != nil {
		s.animate(s.frames())
	}
}

func (s *surface) enabled(id string) bool {
	for _, sp := range s.spots {
		if sp.id == id {
			return sp.enabled
		}
	}
	return false
}

func (s *surface) paint() {
	var ps win.PAINTSTRUCT
	hdc := win.BeginPaint(s.Handle(), &ps)
	if hdc == 0 {
		return
	}
	defer win.EndPaint(s.Handle(), &ps)
	var client win.RECT
	win.GetClientRect(s.Handle(), &client)
	w, h := client.Right, client.Bottom
	if w <= 0 || h <= 0 {
		return
	}
	// One back buffer is reused across frames; it is rebuilt only when the
	// size changes, so an animation frame allocates nothing.
	if s.buffer == 0 || s.bufferW != w || s.bufferH != h {
		s.release()
		s.buffer = win.CreateCompatibleDC(hdc)
		s.bitmap = win.CreateCompatibleBitmap(hdc, w, h)
		s.previous = win.SelectObject(s.buffer, win.HGDIOBJ(s.bitmap))
		s.bufferW, s.bufferH = w, h
	}
	s.render(s.buffer, w, h)
	r := ps.RcPaint
	win.BitBlt(hdc, r.Left, r.Top, r.Right-r.Left, r.Bottom-r.Top, s.buffer, r.Left, r.Top, win.SRCCOPY)
	s.refreshHover()
}

func (s *surface) release() {
	if s.buffer != 0 {
		win.SelectObject(s.buffer, s.previous)
		win.DeleteObject(win.HGDIOBJ(s.bitmap))
		win.DeleteDC(s.buffer)
		s.buffer, s.bitmap = 0, 0
	}
}

// enabledAt returns the id of the topmost enabled control under a
// client-area pixel, or "" if there is none.
func (s *surface) enabledAt(px, py int32) string {
	scale := float64(s.DPI()) / 96
	x, y := float64(px)/scale, float64(py)/scale
	for i := len(s.spots) - 1; i >= 0; i-- {
		if s.spots[i].hit(x, y) {
			if s.spots[i].enabled {
				return s.spots[i].id
			}
			return ""
		}
	}
	return ""
}

// enabledUnder is enabledAt for the cursor position of a mouse message.
func (s *surface) enabledUnder(lParam uintptr) string {
	return s.enabledAt(win.GET_X_LPARAM(lParam), win.GET_Y_LPARAM(lParam))
}

// refreshHover re-checks the cursor after a frame, because controls appear,
// move and disable without the mouse moving.
func (s *surface) refreshHover() {
	if !s.tracking || s.pressed != "" {
		return
	}
	var pt win.POINT
	if !win.GetCursorPos(&pt) || !win.ScreenToClient(s.Handle(), &pt) {
		return
	}
	s.setHover(s.enabledAt(pt.X, pt.Y))
}

func (s *surface) setHover(id string) {
	if s.hover != id {
		s.hover = id
		s.Invalidate()
	}
}

func (s *surface) activate(id string) {
	if id == "" || !s.enabled(id) || s.onClick == nil || time.Since(s.lastAction) < activationCooldown {
		return
	}
	s.lastAction = time.Now()
	s.onClick(id)
	s.Invalidate()
}

func (s *surface) moveFocus(step int) {
	var ids []string
	for _, sp := range s.spots {
		if sp.enabled {
			ids = append(ids, sp.id)
		}
	}
	if len(ids) == 0 {
		return
	}
	index := -1
	for i, id := range ids {
		if id == s.focus {
			index = i
		}
	}
	if index < 0 && step < 0 {
		index = 0
	}
	s.focus = ids[(index+step+len(ids))%len(ids)]
	s.keyboard = true
	s.Invalidate()
}

func (s *surface) cursorIsLink() bool {
	for _, sp := range s.spots {
		if sp.id == s.hover {
			return sp.link
		}
	}
	return false
}

func (s *surface) WndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case win.WM_PAINT:
		s.paint()
		return 0
	case win.WM_PRINTCLIENT:
		var client win.RECT
		win.GetClientRect(hwnd, &client)
		s.render(win.HDC(wParam), client.Right, client.Bottom)
		return 0
	case win.WM_ERASEBKGND:
		return 1
	case win.WM_DESTROY:
		win.KillTimer(hwnd, animationTimer)
		s.release()
	case win.WM_TIMER:
		if win.IsIconic(win.GetAncestor(hwnd, win.GA_ROOT)) {
			// Nothing to show while minimised; the next restore repaints.
			s.animate(0)
			return 0
		}
		s.Invalidate()
		return 0
	case win.WM_MOUSEMOVE:
		if !s.tracking {
			tme := win.TRACKMOUSEEVENT{
				CbSize:    uint32(unsafe.Sizeof(win.TRACKMOUSEEVENT{})),
				DwFlags:   win.TME_LEAVE,
				HwndTrack: hwnd,
			}
			s.tracking = win.TrackMouseEvent(&tme)
		}
		s.setHover(s.enabledUnder(lParam))
		return 0
	case win.WM_MOUSELEAVE:
		s.tracking = false
		s.setHover("")
		return 0
	case win.WM_LBUTTONDOWN:
		s.SetFocus()
		s.keyboard = false
		if id := s.enabledUnder(lParam); id != "" {
			s.pressed, s.hover, s.focus = id, id, id
			win.SetCapture(hwnd)
			s.Invalidate()
		}
		return 0
	case win.WM_LBUTTONUP:
		if s.pressed == "" {
			return 0
		}
		id := s.pressed
		released := s.enabledUnder(lParam)
		s.pressed = ""
		win.ReleaseCapture()
		s.Invalidate()
		if released == id {
			s.activate(id)
		}
		return 0
	case win.WM_CAPTURECHANGED:
		if s.pressed != "" {
			s.pressed = ""
			s.Invalidate()
		}
	case win.WM_SETCURSOR:
		if win.LOWORD(uint32(lParam)) == win.HTCLIENT && s.cursorIsLink() {
			win.SetCursor(win.LoadCursor(0, win.MAKEINTRESOURCE(win.IDC_HAND)))
			return 1
		}
	case win.WM_GETDLGCODE:
		return win.DLGC_WANTALLKEYS | win.DLGC_WANTARROWS | win.DLGC_WANTCHARS | win.DLGC_WANTTAB
	case win.WM_KEYDOWN:
		repeat := lParam&(1<<30) != 0 // previous key state: auto-repeat
		switch wParam {
		case win.VK_TAB:
			if win.GetKeyState(win.VK_SHIFT) < 0 {
				s.moveFocus(-1)
			} else {
				s.moveFocus(1)
			}
		case win.VK_LEFT, win.VK_UP:
			s.moveFocus(-1)
		case win.VK_RIGHT, win.VK_DOWN:
			s.moveFocus(1)
		case win.VK_RETURN, win.VK_SPACE:
			if repeat {
				break
			}
			if s.focus == "" {
				// Never guess a target: a vanished control must not pass
				// its Enter on to whatever sits first (e.g. disconnect).
				s.moveFocus(1)
				break
			}
			s.keyboard = true
			s.activate(s.focus)
		case win.VK_ESCAPE:
			if !repeat && s.onEscape != nil {
				s.onEscape()
			}
		}
		return 0
	case win.WM_SETFOCUS, win.WM_KILLFOCUS:
		s.Invalidate()
	}
	return s.WidgetBase.WndProc(hwnd, msg, wParam, lParam)
}

// spot registers a clickable area for this frame and returns its state.
func (p *painter) spot(id string, r rect, round, enabled bool) buttonState {
	return p.register(spot{id: id, r: r, round: round, enabled: enabled})
}

func (p *painter) register(sp spot) buttonState {
	p.spots = append(p.spots, sp)
	if p.ui == nil || !sp.enabled {
		return buttonState{}
	}
	return buttonState{
		hover:   p.ui.hover == sp.id,
		pressed: p.ui.pressed == sp.id && p.ui.hover == sp.id,
		focus:   p.ui.focus == sp.id && p.ui.keyboard && p.ui.Focused(),
	}
}

func (p *painter) elapsed() float64 {
	if p.ui == nil {
		return 0
	}
	return p.ui.elapsed().Seconds()
}
