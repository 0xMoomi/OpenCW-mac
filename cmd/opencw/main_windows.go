package main

import (
	"os"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	dcl "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"opencw/internal/client"
	"opencw/internal/crash"
)

// appVersion is set by build.ps1 (-X main.appVersion); this is the fallback.
var appVersion = "1.0.0"

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	flashWindowEx = user32.NewProc("FlashWindowEx")
)

func main() {
	runtime.LockOSThread()
	if len(os.Args) == 2 && os.Args[1] == crash.MonitorArg {
		path, err := crash.Capture(os.Stdin, "")
		if err != nil || path != "" {
			showCrash(path, err)
		}
		return
	}
	// One instance across all Windows sessions owns the TUN interface and routes.
	name, _ := windows.UTF16PtrFromString(`Global\OpenCW.Client`)
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		// An instance in another Windows session has no window here to focus.
		if !focusExisting() {
			walk.MsgBox(nil, "OpenCW", "OpenCW가 이미 실행 중입니다.", walk.MsgBoxIconInformation)
		}
		return
	}
	defer windows.CloseHandle(mutex)
	// Crash reports are best effort; the app runs without the monitor.
	if stopReporter, err := crash.Start(); err == nil {
		// Keep the monitor attached while a panic unwinds defers.
		defer func() {
			if p := recover(); p != nil {
				panic(p)
			}
			_ = stopReporter()
		}()
	}
	if err := runUI(client.DefaultDependencies()); err != nil {
		walk.MsgBox(nil, "OpenCW", "창을 열 수 없습니다. 다시 실행해주세요.\n\n"+err.Error(), walk.MsgBoxIconError)
	}
}

// focusExisting brings the running instance's window forward and reports
// whether it found one. It matches the window class too, so an Explorer folder
// named "OpenCW" is not mistaken for the app.
func focusExisting() bool {
	class, _ := windows.UTF16PtrFromString(`\o/ Walk_MainWindow_Class \o/`)
	title, _ := windows.UTF16PtrFromString("OpenCW")
	hwnd := win.FindWindow(class, title)
	if hwnd == 0 {
		return false
	}
	if win.IsIconic(hwnd) {
		win.ShowWindow(hwnd, win.SW_RESTORE)
	}
	win.SetForegroundWindow(hwnd)
	return true
}

// flash marks the taskbar button until the window is looked at.
func flash(hwnd win.HWND) {
	info := struct {
		Size    uint32
		Window  win.HWND
		Flags   uint32
		Count   uint32
		Timeout uint32
	}{Window: hwnd, Flags: 2 | 12 /* FLASHW_TRAY | FLASHW_TIMERNOFG */}
	info.Size = uint32(unsafe.Sizeof(info))
	flashWindowEx.Call(uintptr(unsafe.Pointer(&info)))
}

// app is the main window: the home view painted on one surface, driven by a
// controller.
type app struct {
	window       *walk.MainWindow
	view         *homeView
	ui           *surface
	controller   *client.Controller
	closing      atomic.Bool
	readyToClose bool
}

func newApp(deps client.Dependencies) (*app, error) {
	a := &app{view: &homeView{}}
	spec := dcl.MainWindow{AssignTo: &a.window, Title: "OpenCW", Layout: dcl.VBox{MarginsZero: true}, Visible: false}
	if err := spec.Create(); err != nil {
		return nil, err
	}
	ui, err := newSurface(a.window, a.view.paint, a.activate)
	if err != nil {
		return nil, err
	}
	a.ui = ui
	ui.focus = "switch"
	ui.frames = a.view.frames
	ui.onEscape = func() {
		if a.view.details {
			a.view.details = false
			ui.focus = "switch"
			ui.Invalidate()
		}
	}
	setWindowIcon(a.window)
	themeWindow(a.window.Handle(), canvasBg)
	fixWindow(a.window, homeWidth, homeHeight)
	a.controller = client.New(deps, func() {
		if !a.closing.Load() {
			a.window.Synchronize(func() {
				if !a.closing.Load() {
					a.apply(a.controller.Snapshot())
				}
			})
		}
	})
	a.window.Closing().Attach(a.onClosing)
	return a, nil
}

// runUI shows the main window and runs it until it closes.
func runUI(deps client.Dependencies) error {
	a, err := newApp(deps)
	if err != nil {
		return err
	}
	// Also release routes if the message loop exits without the close event.
	defer a.controller.Close()
	a.window.SetVisible(true)
	a.ui.SetFocus()
	a.controller.Start()
	a.window.Run()
	return nil
}

func (a *app) activate(id string) {
	if a.controller == nil || a.closing.Load() {
		return
	}
	view, ui := a.view, a.ui
	switch id {
	case "switch":
		view.toggle()
		a.controller.Toggle()
	case "change":
		view.switching = true
		a.controller.Switch()
	case "details":
		view.details = true
		ui.focus = "back"
	case "back":
		view.details = false
		ui.focus = "switch"
	case "copy":
		walk.Clipboard().SetText(view.diagnostics())
		view.copiedUntil = time.Now().Add(2 * time.Second)
		time.AfterFunc(2100*time.Millisecond, func() { a.window.Synchronize(func() { ui.Invalidate() }) })
	}
	ui.setName(view.accessibleName())
}

func (a *app) apply(s client.Snapshot) {
	if a.view.update(s) && win.GetForegroundWindow() != a.window.Handle() {
		flash(a.window.Handle())
	}
	a.ui.setName(a.view.accessibleName())
	a.ui.Invalidate()
}

// onClosing defers the window's close until the controller has restored the
// network, asking first when the user closes a live connection.
func (a *app) onClosing(canceled *bool, reason walk.CloseReason) {
	if a.readyToClose {
		return
	}
	*canceled = true
	if a.closing.Load() {
		return
	}
	if reason == walk.CloseReasonUser {
		switch a.view.phase() {
		case phaseConnected, phaseRecovering, phaseConnecting:
			answer := walk.MsgBox(a.window, "OpenCW",
				"연결을 해제하고 종료하시겠습니까?\n실행 중인 게임의 연결이 끊어질 수 있습니다.",
				walk.MsgBoxYesNo|walk.MsgBoxIconQuestion)
			if answer != walk.DlgCmdYes {
				return
			}
		}
	}
	a.closing.Store(true)
	a.view.closing = true
	a.view.details = false
	a.view.update(a.view.snap)
	a.ui.setName(a.view.accessibleName())
	a.ui.Invalidate()
	go func() {
		closeErr := a.controller.Close()
		a.window.Synchronize(func() {
			if closeErr != nil {
				walk.MsgBox(a.window, "OpenCW", "네트워크 설정을 복원하지 못했습니다.\nPC를 재시작하면 복원됩니다.",
					walk.MsgBoxIconWarning)
			}
			a.readyToClose = true
			a.window.Close()
		})
	}()
}
