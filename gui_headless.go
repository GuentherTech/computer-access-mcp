//go:build !windows || headless

package main

import (
	"context"
	"errors"
)

func runDesktopGUI(context.Context, *agent, bool) error {
	return errors.New("desktop UI is not included in this build; use the Windows desktop release or pass -headless")
}
