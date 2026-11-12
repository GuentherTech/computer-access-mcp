//go:build windows && !headless

package main

import (
	"context"
	_ "embed"
	"image"
	"image/color"
	"io"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/io/clipboard"
	"gioui.org/io/semantic"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"github.com/energye/systray"
)

var (
	guiAccent     = color.NRGBA{R: 49, G: 91, B: 148, A: 255}
	guiInk        = color.NRGBA{R: 232, G: 235, B: 241, A: 255}
	guiMuted      = color.NRGBA{R: 173, G: 183, B: 198, A: 255}
	guiBackground = color.NRGBA{R: 22, G: 24, B: 29, A: 255}
	guiSurface    = color.NRGBA{R: 32, G: 36, B: 44, A: 255}
	guiInput      = color.NRGBA{R: 15, G: 18, B: 23, A: 255}
	guiButton     = color.NRGBA{R: 44, G: 50, B: 61, A: 255}
	guiLine       = color.NRGBA{R: 105, G: 118, B: 138, A: 255}
	guiFocus      = color.NRGBA{R: 130, G: 179, B: 255, A: 255}
	guiRed        = color.NRGBA{R: 255, G: 137, B: 137, A: 255}
)

type desktopUI struct {
	agent          *agent
	window         *app.Window
	theme          *material.Theme
	view           agentView
	selected       string
	tab            string
	buttons        map[string]*widget.Clickable
	list           layout.List
	content        layout.List
	modal          string
	modalID        string
	modalAction    string
	message        string
	errorText      string
	showPassword   bool
	url            widget.Editor
	password       widget.Editor
	name           widget.Editor
	deviceName     widget.Editor
	relayURL       widget.Editor
	enrollmentKey  widget.Editor
	cloudflared    widget.Editor
	relayMode      widget.Bool
	settingsLoaded bool
	quitting       bool
	inlineID       string
	inlineSaved    serviceConfig
	inlineName     widget.Editor
}

func runDesktopGUI(ctx context.Context, a *agent, show bool) error {
	app.ID = desktopAppID
	setDesktopAppIdentity()
	var windowMu sync.Mutex
	var currentWindow *app.Window
	window := func() *app.Window { windowMu.Lock(); defer windowMu.Unlock(); return currentWindow }
	setWindow := func(w *app.Window) { windowMu.Lock(); currentWindow = w; windowMu.Unlock() }
	openWindow := make(chan struct{}, 1)
	if show {
		openWindow <- struct{}{}
	}
	u := &desktopUI{agent: a, theme: material.NewTheme(), buttons: map[string]*widget.Clickable{}, tab: "services", list: layout.List{Axis: layout.Vertical}, content: layout.List{Axis: layout.Vertical}}
	u.theme.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	u.theme.Palette.ContrastBg = guiAccent
	u.theme.Palette.ContrastFg = guiInk
	u.theme.Palette.Fg = guiInk
	u.theme.Palette.Bg = guiBackground
	u.theme.TextSize = 13
	u.url.SingleLine = true
	u.url.ReadOnly = true
	u.password.SingleLine = true
	u.password.ReadOnly = true
	u.password.Mask = '•'
	for _, editor := range []*widget.Editor{&u.name, &u.deviceName, &u.relayURL, &u.enrollmentKey, &u.cloudflared, &u.inlineName} {
		editor.SingleLine = true
	}
	u.enrollmentKey.Mask = '•'
	activate := func() {
		select {
		case openWindow <- struct{}{}:
		default:
		}
		if w := window(); w != nil {
			w.Invalidate()
		}
	}
	a.mu.Lock()
	a.showGUI = activate
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.showGUI = nil; a.mu.Unlock() }()
	go systray.Run(func() {
		systray.SetIcon(desktopTrayIcon())
		systray.SetTooltip("Computer Access")
		systray.AddMenuItem("Open Computer Access", "").Click(activate)
		systray.AddMenuItem("Turn agent on / off", "").Click(func() {
			_ = a.mutate(func(c *agentConfig) error { c.Enabled = !c.Enabled; return nil })
			if w := window(); w != nil {
				w.Invalidate()
			}
		})
		systray.AddSeparator()
		systray.AddMenuItem("Quit", "").Click(func() {
			if a.quit != nil {
				a.quit()
			}
		})
	}, func() {})
	defer systray.Quit()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				if w := window(); w != nil {
					w.Perform(system.ActionClose)
				}
				return
			case <-ticker.C:
				if w := window(); w != nil {
					w.Invalidate()
				}
			}
		}
	}()
	var ops op.Ops
	var w *app.Window
	for {
		if w == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-openWindow:
				w = new(app.Window)
				w.Option(app.Title("Computer Access"), app.Size(unit.Dp(860), unit.Dp(460)), app.MinSize(unit.Dp(700), unit.Dp(380)))
				u.window = w
				setWindow(w)
			}
		} else {
			select {
			case <-openWindow:
				w.Option(app.Windowed.Option())
				w.Perform(system.ActionRaise)
			default:
			}
		}
		switch e := w.Event().(type) {
		case app.Win32ViewEvent:
			setDesktopWindowIcons(e.HWND)
		case app.DestroyEvent:
			setWindow(nil)
			u.window = nil
			w = nil
			if e.Err != nil || ctx.Err() != nil || u.quitting {
				return e.Err
			}
			// X destroys only the GUI window. The tray and agent keep running.
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.view = a.view()
			u.update(gtx)
			u.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (u *desktopUI) button(id string) *widget.Clickable {
	if u.buttons[id] == nil {
		u.buttons[id] = new(widget.Clickable)
	}
	return u.buttons[id]
}
func (u *desktopUI) selectedService() (serviceView, bool) {
	for _, s := range u.view.Services {
		if s.ID == u.selected {
			return s, true
		}
	}
	return serviceView{}, false
}
func (u *desktopUI) report(err error) {
	if err != nil {
		u.errorText = err.Error()
	} else {
		u.errorText = ""
		u.view = u.agent.view()
	}
}
func (u *desktopUI) copy(gtx layout.Context, value, message string) {
	gtx.Execute(clipboard.WriteCmd{Type: "text/plain", Data: io.NopCloser(strings.NewReader(value))})
	u.message = message
}

func (u *desktopUI) update(gtx layout.Context) {
	if _, ok := u.selectedService(); !ok && len(u.view.Services) > 0 {
		u.selected = u.view.Services[0].ID
	}
	if u.modal != "" {
		if u.button("modal-cancel").Clicked(gtx) {
			u.modal = ""
			u.errorText = ""
			return
		}
		if u.button("modal-save").Clicked(gtx) {
			if u.modal == "service" {
				err := u.agent.saveDesktopService("", u.name.Text())
				u.report(err)
				if err == nil {
					u.modal = ""
				}
			} else {
				err := u.agent.desktopServiceAction(u.modalID, u.modalAction)
				u.report(err)
				if err == nil {
					u.modal = ""
				}
			}
		}
		return
	}
	if u.button("tab-services").Clicked(gtx) {
		u.tab = "services"
		u.content.Position = layout.Position{}
	}
	if u.button("tab-connection").Clicked(gtx) {
		u.tab = "connection"
		u.content.Position = layout.Position{}
		u.loadSettings()
	}
	if u.button("agent-power").Clicked(gtx) {
		u.report(u.agent.mutate(func(c *agentConfig) error { c.Enabled = !c.Enabled; return nil }))
	}
	if u.button("add").Clicked(gtx) {
		u.openService()
	}
	if u.button("save-connection").Clicked(gtx) {
		mode := "cloudflare"
		if u.relayMode.Value {
			mode = "relay"
		}
		err := u.agent.saveDesktopConnection(mode, u.deviceName.Text(), u.relayURL.Text(), u.enrollmentKey.Text(), u.cloudflared.Text())
		u.report(err)
		if err == nil {
			u.enrollmentKey.SetText("")
			u.message = "Settings saved"
		}
	}
	if u.button("quit").Clicked(gtx) {
		u.quitting = true
		u.window.Perform(system.ActionClose)
	}
	for _, s := range u.view.Services {
		if u.button("select-" + s.ID).Clicked(gtx) {
			u.selected = s.ID
			u.showPassword = false
		}
	}
	s, ok := u.selectedService()
	if !ok {
		return
	}
	if u.button("copy-url").Clicked(gtx) {
		if s.URL != "" {
			u.copy(gtx, s.URL, "URL copied")
		} else {
			u.message = "No tunnel URL"
		}
	}
	if u.button("copy-password").Clicked(gtx) {
		u.copy(gtx, s.Password, "Token copied")
	}
	if u.button("reveal").Clicked(gtx) {
		u.showPassword = !u.showPassword
	}
	if u.button("service-power").Clicked(gtx) {
		u.report(u.agent.desktopServiceAction(s.ID, "power"))
	}
	if u.button("inline-save").Clicked(gtx) {
		err := u.agent.saveDesktopService(s.ID, u.inlineName.Text())
		u.report(err)
		if err == nil {
			if saved, ok := u.selectedService(); ok {
				u.resetInline(saved.serviceConfig)
			}
			u.message = "Saved"
		}
	}
	if u.button("inline-cancel").Clicked(gtx) {
		u.resetInline(s.serviceConfig)
		u.errorText = ""
	}
	for _, action := range []string{"url", "password", "delete"} {
		if u.button("regen-" + action).Clicked(gtx) {
			u.modal = "confirm"
			u.modalID = s.ID
			u.modalAction = action
			u.errorText = ""
		}
	}
}

func (u *desktopUI) inlineDirty() bool {
	return u.inlineName.Text() != u.inlineSaved.Name
}

func (u *desktopUI) resetInline(s serviceConfig) {
	u.inlineID = s.ID
	u.inlineSaved = s
	u.inlineName.SetText(s.Name)
}

func (u *desktopUI) openService() {
	u.modal = "service"
	u.modalID = ""
	u.errorText = ""
	u.name.SetText("")
}
func (u *desktopUI) loadSettings() {
	c := u.agent.snapshot()
	u.deviceName.SetText(c.Name)
	u.relayURL.SetText(c.RelayURL)
	u.cloudflared.SetText(c.Cloudflared)
	u.relayMode.Value = c.Mode == "relay"
	u.settingsLoaded = true
}

func (u *desktopUI) label(value string, size unit.Sp, bold bool, c color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		l := material.Label(u.theme, size, value)
		l.Color = c
		if bold {
			l.Font.Weight = font.Bold
		}
		return l.Layout(gtx)
	}
}
func (u *desktopUI) btn(id, label string, primary bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		b := material.Button(u.theme, u.button(id), label)
		gtx.Constraints.Min = image.Point{}
		b.CornerRadius = 2
		b.TextSize = 12
		b.Inset = layout.Inset{Top: 5, Bottom: 5, Left: 9, Right: 9}
		if !primary {
			b.Background = guiButton
			b.Color = guiInk
		}
		border := guiLine
		if primary || u.button(id).Hovered() || gtx.Focused(u.button(id)) {
			border = guiFocus
		}
		return widget.Border{Color: border, CornerRadius: 2, Width: 1}.Layout(gtx, b.Layout)
	}
}
func column(gtx layout.Context, children ...layout.Widget) layout.Dimensions {
	items := make([]layout.FlexChild, 0, len(children)*2)
	for i, c := range children {
		if i > 0 {
			items = append(items, layout.Rigid(layout.Spacer{Height: 8}.Layout))
		}
		items = append(items, layout.Rigid(c))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, items...)
}
func row(gtx layout.Context, children ...layout.Widget) layout.Dimensions {
	items := make([]layout.FlexChild, 0, len(children)*2)
	for i, c := range children {
		if i > 0 {
			items = append(items, layout.Rigid(layout.Spacer{Width: 6}.Layout))
		}
		items = append(items, layout.Rigid(c))
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, items...)
}

func (u *desktopUI) editor(editor *widget.Editor, hint string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.Y = 0
		border := guiLine
		if gtx.Focused(editor) {
			border = guiFocus
		}
		return widget.Border{Color: border, CornerRadius: 2, Width: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return filledBox(gtx, guiInput, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 5, Bottom: 5, Left: 7, Right: 7}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					ed := material.Editor(u.theme, editor, hint)
					ed.Color = guiInk
					ed.HintColor = guiMuted
					ed.TextSize = 12
					if editor == &u.url || editor == &u.password {
						ed.Font.Typeface = "Go Mono"
					}
					return ed.Layout(gtx)
				})
			})
		})
	}
}

func (u *desktopUI) field(label string, editor *widget.Editor, hint string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(u.label(label, 11, false, guiMuted)),
			layout.Rigid(layout.Spacer{Height: 4}.Layout),
			layout.Rigid(u.editor(editor, hint)),
		)
	}
}

func (u *desktopUI) valueRow(label string, editor *widget.Editor, hint string, actions ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(u.label(label, 11, false, guiMuted)),
			layout.Rigid(layout.Spacer{Height: 4}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, u.editor(editor, hint)),
					layout.Rigid(layout.Spacer{Width: 6}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return row(gtx, actions...) }),
				)
			}),
		)
	}
}

func (u *desktopUI) layout(gtx layout.Context) {
	paint.Fill(gtx.Ops, guiBackground)
	base := gtx
	if u.modal != "" {
		base = base.Disabled()
	}
	layout.Stack{}.Layout(gtx,
		// Expanded children get the largest Stacked child as their minimum size,
		// which is zero without a dialog. Fill the window with its own constraints.
		layout.Expanded(func(layout.Context) layout.Dimensions {
			return layout.UniformInset(12).Layout(base, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(u.header),
					layout.Rigid(layout.Spacer{Height: 12}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						if u.tab == "connection" {
							return u.content.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions { return u.connection(gtx) })
						}
						return u.services(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if u.view.Error != "" && u.view.Enabled {
							return u.label(u.view.Error, 11, false, guiRed)(gtx)
						}
						if u.message != "" {
							return u.label(u.message, 11, false, guiMuted)(gtx)
						}
						return layout.Dimensions{}
					}),
				)
			})
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			if u.modal == "" {
				return layout.Dimensions{}
			}
			// Stacked children start with zero minimum bounds. Fill the viewport
			// before centering so the dialog cannot anchor itself to the top left.
			gtx.Constraints.Min = gtx.Constraints.Max
			paint.Fill(gtx.Ops, color.NRGBA{A: 90})
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				width := min(gtx.Dp(360), gtx.Constraints.Max.X-24)
				gtx.Constraints.Min.X = width
				gtx.Constraints.Max.X = width
				return u.card(gtx, u.modalContent)
			})
		}),
	)
}

func filledBox(gtx layout.Context, fill color.NRGBA, content layout.Widget) layout.Dimensions {
	return layout.Background{}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		paint.FillShape(gtx.Ops, fill, clip.Rect(image.Rectangle{Max: gtx.Constraints.Min}).Op())
		return layout.Dimensions{Size: gtx.Constraints.Min}
	}, content)
}

func panel(gtx layout.Context, content layout.Widget) layout.Dimensions {
	return widget.Border{Color: guiLine, CornerRadius: 2, Width: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return filledBox(gtx, guiSurface, func(gtx layout.Context) layout.Dimensions { return layout.UniformInset(8).Layout(gtx, content) })
	})
}

func (u *desktopUI) card(gtx layout.Context, content layout.Widget) layout.Dimensions {
	return layout.Background{}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		defer clip.Rect(image.Rectangle{Max: gtx.Constraints.Min}).Push(gtx.Ops).Pop()
		semantic.DescriptionOp("dialog").Add(gtx.Ops)
		return filledBox(gtx, guiSurface, func(gtx layout.Context) layout.Dimensions {
			return widget.Border{Color: guiFocus, CornerRadius: 2, Width: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} })
		})
	}, func(gtx layout.Context) layout.Dimensions { return layout.UniformInset(16).Layout(gtx, content) })
}

func (u *desktopUI) header(gtx layout.Context) layout.Dimensions {
	status := "off"
	if u.view.Enabled {
		status = "connecting"
		if u.view.Connected {
			status = "online"
		}
	}
	power := "Start"
	if u.view.Enabled {
		power = "Stop"
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return row(gtx, u.btn("tab-services", "Services", u.tab == "services"), u.btn("tab-connection", "Settings", u.tab == "connection"))
		}),
		layout.Rigid(layout.Spacer{Width: 12}.Layout),
		layout.Flexed(1, u.label(u.view.Name+" / "+status, 12, false, guiMuted)),
		layout.Rigid(u.btn("agent-power", power, true)),
	)
}

func (u *desktopUI) serviceRow(gtx layout.Context, s serviceView) layout.Dimensions {
	selected := s.ID == u.selected
	click := u.button("select-" + s.ID)
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, gtx.Dp(28)))
	return click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.SelectedOp(selected).Add(gtx.Ops)
		semantic.DescriptionOp("service:" + s.ID).Add(gtx.Ops)
		background := guiSurface
		if selected {
			background = guiAccent
		} else if click.Hovered() || gtx.Focused(click) {
			background = guiButton
		}
		return filledBox(gtx, background, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 6, Bottom: 6, Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				name := s.Name
				if !s.Enabled {
					name += " [off]"
				}
				label := material.Label(u.theme, 12, name)
				label.Color = guiInk
				label.MaxLines = 1
				label.Truncator = "..."
				return label.Layout(gtx)
			})
		})
	})
}

func (u *desktopUI) services(gtx layout.Context) layout.Dimensions {
	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(160)
			gtx.Constraints.Max.X = gtx.Dp(160)
			return widget.Border{Color: guiLine, Width: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return u.list.Layout(gtx, len(u.view.Services), func(gtx layout.Context, i int) layout.Dimensions {
							return u.serviceRow(gtx, u.view.Services[i])
						})
					}),
					layout.Rigid(u.btn("add", "+ Add", false)),
				)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return panel(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.content.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions { return u.serviceDetails(gtx) })
				})
			})
		}),
	)
}

func (u *desktopUI) serviceDetails(gtx layout.Context) layout.Dimensions {
	s, ok := u.selectedService()
	if !ok {
		return u.label("No services", 12, false, guiMuted)(gtx)
	}
	if u.inlineID != s.ID || (!u.inlineDirty() && u.inlineSaved != s.serviceConfig) {
		u.resetInline(s.serviceConfig)
	}
	if u.url.Text() != s.URL {
		u.url.SetText(s.URL)
	}
	if u.password.Text() != s.Password {
		u.password.SetText(s.Password)
	}
	u.password.Mask = '•'
	if u.showPassword {
		u.password.Mask = 0
	}
	power := "Enable"
	if s.Enabled {
		power = "Disable"
	}
	reveal := "Show"
	if u.showPassword {
		reveal = "Hide"
	}
	children := []layout.Widget{u.field("Name", &u.inlineName, "Name")}
	if u.inlineDirty() {
		children = append(children, func(gtx layout.Context) layout.Dimensions {
			return row(gtx, u.btn("inline-save", "Save", true), u.btn("inline-cancel", "Cancel", false))
		})
	}
	children = append(children,
		u.valueRow("URL", &u.url, "Waiting for tunnel", u.btn("copy-url", "Copy", false), u.btn("regen-url", "Rotate", false)),
		u.valueRow("Token", &u.password, "", u.btn("copy-password", "Copy", false), u.btn("reveal", reveal, false), u.btn("regen-password", "Rotate", false)),
		layout.Spacer{Height: 4}.Layout,
		func(gtx layout.Context) layout.Dimensions {
			return row(gtx, u.btn("service-power", power, false), u.btn("regen-delete", "Delete", false))
		},
	)
	if u.errorText != "" {
		children = append(children, u.label(u.errorText, 13, false, guiRed))
	}
	return column(gtx, children...)
}

func (u *desktopUI) connection(gtx layout.Context) layout.Dimensions {
	if !u.settingsLoaded {
		u.loadSettings()
	}
	children := []layout.Widget{u.field("Device", &u.deviceName, "Hostname"), func(gtx layout.Context) layout.Dimensions {
		return material.CheckBox(u.theme, &u.relayMode, "Hosted relay").Layout(gtx)
	}}
	if !u.relayMode.Value {
		children = append(children, u.field("cloudflared", &u.cloudflared, "Auto-detect"))
	} else {
		children = append(children, u.field("Relay URL", &u.relayURL, "https://connect.example.com"), u.field("Enrollment key", &u.enrollmentKey, "Unchanged"))
	}
	children = append(children, func(gtx layout.Context) layout.Dimensions {
		return row(gtx, u.btn("save-connection", "Save", true), u.btn("quit", "Quit", false))
	})
	if u.errorText != "" {
		children = append(children, u.label(u.errorText, 13, false, guiRed))
	}
	return column(gtx, children...)
}

func (u *desktopUI) modalContent(gtx layout.Context) layout.Dimensions {
	if u.modal == "confirm" {
		message := "Rotate URL? The old URL will stop working."
		if u.modalAction == "password" {
			message = "Rotate token? The old token will stop working."
		}
		if u.modalAction == "delete" {
			message = "Delete service? This revokes its URL and token."
		}
		return column(gtx, u.label(message, 13, false, guiInk), func(gtx layout.Context) layout.Dimensions {
			return row(gtx, u.btn("modal-cancel", "Cancel", false), u.btn("modal-save", "Confirm", true))
		})
	}
	children := []layout.Widget{u.label("Add service", 14, true, guiInk), u.editor(&u.name, "Name")}
	if u.errorText != "" {
		children = append(children, u.label(u.errorText, 13, false, guiRed))
	}
	children = append(children, func(gtx layout.Context) layout.Dimensions {
		return row(gtx, u.btn("modal-cancel", "Cancel", false), u.btn("modal-save", "Add", true))
	})
	return column(gtx, children...)
}

//go:embed assets/app.ico
var desktopIcon []byte

func desktopTrayIcon() []byte { return desktopIcon }
