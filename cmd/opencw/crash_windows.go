package main

import (
	"os"
	"os/exec"
	"time"

	"github.com/lxn/walk"
	dcl "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
)

const (
	crashWidth  = 460
	crashHeight = 216
)

// crashView follows the standard dialog shape: what happened, what to do,
// then commands. Restart is the default; the log is one quiet click away.
type crashView struct {
	path        string // saved report, empty if saving failed
	copiedUntil time.Time
}

func (v *crashView) paint(p *painter, width, height float64) {
	p.fillRound(rect{0, 0, width, height}, 0, cardBg)
	body := "다시 시작하면 정상적으로 사용할 수 있습니다.\n문제가 계속되면 오류 기록을 복사해 전달해주세요."
	if v.path == "" {
		body = "다시 시작하면 정상적으로 사용할 수 있습니다.\n오류 기록을 저장하지 못했습니다."
	}
	p.text("OpenCW가 예기치 않게 종료되었습니다", fontDialogHead, ink, rect{28, 24, width - 56, 30}, textLeft)
	p.text(body, fontBody, muted, rect{28, 62, width - 56, 48}, textParagraphLeft)

	bar := rect{0, height - 80, width, 80}
	p.fillRound(bar, 0, canvasBg)
	p.line(point{0, bar.y + .5}, point{width, bar.y + .5}, 1, divider)
	p.primary("restart", rect{width - 28 - 120, bar.y + 18, 120, 44}, "다시 시작")
	p.button("close", rect{width - 28 - 120 - 10 - 92, bar.y + 18, 92, 44}, "닫기", true)
	if v.path != "" {
		label := "오류 기록 복사"
		if time.Now().Before(v.copiedUntil) {
			label = "복사됨"
		}
		p.link("log", 28, bar.y+25, label)
	}
}

// showCrash runs the crash dialog for the report saved at path.
func showCrash(path string, saveErr error) {
	v := &crashView{}
	if saveErr == nil {
		v.path = path
	}
	fallback := func() {
		walk.MsgBox(nil, "OpenCW", "OpenCW가 예기치 않게 종료되었습니다.\n\n"+path, walk.MsgBoxIconError)
	}
	var dlg *walk.Dialog
	spec := dcl.Dialog{AssignTo: &dlg, Title: "OpenCW", FixedSize: true, Layout: dcl.VBox{MarginsZero: true}}
	if err := spec.Create(nil); err != nil {
		fallback()
		return
	}
	var ui *surface
	var err error
	ui, err = newSurface(dlg, v.paint, func(id string) {
		switch id {
		case "restart":
			exe, err := os.Executable()
			if err == nil {
				err = exec.Command(exe).Start()
			}
			if err != nil {
				walk.MsgBox(dlg, "OpenCW", "다시 시작하지 못했습니다. OpenCW를 직접 실행해주세요.",
					walk.MsgBoxIconWarning)
			}
			dlg.Close(walk.DlgCmdOK)
		case "close":
			dlg.Close(walk.DlgCmdCancel)
		case "log":
			if data, err := os.ReadFile(path); err == nil {
				walk.Clipboard().SetText(string(data))
				v.copiedUntil = time.Now().Add(2 * time.Second)
				time.AfterFunc(2100*time.Millisecond, func() { dlg.Synchronize(func() { ui.Invalidate() }) })
			}
		}
	})
	if err != nil {
		fallback()
		return
	}
	ui.focus = "restart"
	ui.onEscape = func() { dlg.Close(walk.DlgCmdCancel) }
	setWindowIcon(dlg)
	themeWindow(dlg.Handle(), cardBg)
	fixWindow(dlg, crashWidth, crashHeight)
	// The monitor is started hidden; consume that STARTUPINFO show state so
	// the dialog's own ShowWindow is honoured.
	win.ShowWindow(dlg.Handle(), win.SW_HIDE)
	dlg.Starting().Attach(func() {
		ui.SetFocus()
		win.SetForegroundWindow(dlg.Handle())
	})
	dlg.Run()
}
