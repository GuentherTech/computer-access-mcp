//go:build windows && !headless

package main

import (
	"image"
	"testing"

	"gioui.org/font/gofont"
	"gioui.org/io/input"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

func layoutTestUI() *desktopUI {
	u := &desktopUI{theme: material.NewTheme(), buttons: map[string]*widget.Clickable{}, tab: "services", list: layout.List{Axis: layout.Vertical}, content: layout.List{Axis: layout.Vertical}}
	u.theme.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	u.name.SingleLine = true
	u.inlineName.SingleLine = true
	u.url.SingleLine = true
	u.password.SingleLine = true
	s := newService("PowerShell MCP")
	u.selected = s.ID
	u.view = agentView{Name: "DEV-PC", Enabled: true, Connected: true, Services: []serviceView{{serviceConfig: s, URL: "https://example.trycloudflare.com/s/" + s.URLID}}}
	return u
}

func uiSemantics(u *desktopUI, size image.Point, scale float32) []input.SemanticNode {
	var ops op.Ops
	var router input.Router
	gtx := layout.Context{Ops: &ops, Constraints: layout.Exact(size), Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Source: router.Source()}
	u.layout(gtx)
	router.Frame(&ops)
	return router.AppendSemantics(nil)
}

func walkSemantics(nodes []input.SemanticNode, visit func(input.SemanticNode)) {
	for _, node := range nodes {
		visit(node)
		walkSemantics(node.Children, visit)
	}
}

func TestDialogsCentered(t *testing.T) {
	for _, scale := range []float32{1, 1.5, 2} {
		for _, modal := range []string{"service", "confirm"} {
			u := layoutTestUI()
			u.modal = modal
			u.modalAction = "delete"
			size := image.Pt(int(860*scale), int(460*scale))
			found := false
			walkSemantics(uiSemantics(u, size, scale), func(node input.SemanticNode) {
				if node.Desc.Description != "dialog" {
					return
				}
				found = true
				bounds := node.Desc.Bounds
				dx := bounds.Min.X + bounds.Max.X - size.X
				dy := bounds.Min.Y + bounds.Max.Y - size.Y
				if dx < -1 || dx > 1 || dy < -1 || dy > 1 {
					t.Errorf("%s at scale %v is not centered: %v in %v", modal, scale, bounds, size)
				}
				if bounds.Dx() > int(361*scale) || bounds.Dy() > int(200*scale) {
					t.Errorf("dialog unexpectedly large: %v", bounds)
				}
			})
			if !found {
				t.Fatal("dialog missing from layout")
			}
		}
	}
}

func TestCompactControlsFitWindow(t *testing.T) {
	for _, size := range []image.Point{image.Pt(700, 380), image.Pt(860, 460)} {
		u := layoutTestUI()
		buttons := 0
		walkSemantics(uiSemantics(u, size, 1), func(node input.SemanticNode) {
			if node.Desc.Class != semantic.Button {
				return
			}
			buttons++
			bounds := node.Desc.Bounds
			if !bounds.In(image.Rectangle{Max: size}) {
				t.Errorf("control outside window: %q %v", node.Desc.Label, bounds)
			}
			if bounds.Dy() > 32 {
				t.Errorf("oversized control: %q %v", node.Desc.Label, bounds)
			}
		})
		if buttons < 10 {
			t.Errorf("expected service actions, got %d controls", buttons)
		}
	}
}

func TestServiceListRowsFillWidth(t *testing.T) {
	u := layoutTestUI()
	second := newService("B")
	u.view.Services = append(u.view.Services, serviceView{serviceConfig: second})
	rows := map[string]input.SemanticDesc{}
	walkSemantics(uiSemantics(u, image.Pt(860, 460), 1), func(node input.SemanticNode) {
		if node.Desc.Description == "service:"+u.selected || node.Desc.Description == "service:"+second.ID {
			rows[node.Desc.Description] = node.Desc
		}
	})
	firstRow, ok := rows["service:"+u.selected]
	if !ok {
		t.Fatal("first service missing")
	}
	secondRow, ok := rows["service:"+second.ID]
	if !ok {
		t.Fatal("second service missing")
	}
	if firstRow.Bounds.Dx() != secondRow.Bounds.Dx() || firstRow.Bounds.Dx() != 160 || firstRow.Bounds.Min.X != 12 {
		t.Fatalf("rows do not fill list: %v, %v", firstRow.Bounds, secondRow.Bounds)
	}
	if firstRow.Bounds.Max.Y != secondRow.Bounds.Min.Y {
		t.Fatalf("rows are not contiguous: %v, %v", firstRow.Bounds, secondRow.Bounds)
	}
	if !firstRow.Selected || secondRow.Selected {
		t.Fatal("wrong selection state")
	}
}
