// Command turnrelay-tray is a small cross-platform system-tray front end for
// the turnrelay VPN. It is a thin client: it drives the `turnrelay` CLI
// (up / down / status --json) and renders the result, so it shares no code
// with the privileged daemon and stays isolated in its own cgo module.
package main

import (
	"fmt"
	"time"

	"github.com/getlantern/systray"
)

func main() {
	systray.Run(onReady, func() {})
}

func onReady() {
	systray.SetTitle("turnrelay ○")
	systray.SetTooltip("turnrelay VPN")
	mStatus := systray.AddMenuItem("checking...", "")
	mStatus.Disable()
	mToggle := systray.AddMenuItem("Connect", "start or stop the VPN")
	systray.AddSeparator()
	mProfile := systray.AddMenuItem("Edit profile", "open profile.json")
	mQuit := systray.AddMenuItem("Quit", "")

	// busy guards against overlapping up/down while one is in flight.
	busy := false
	done := make(chan struct{}, 1)

	render := func(s Status, err error) {
		switch {
		case err != nil:
			systray.SetTitle("turnrelay ○")
			mStatus.SetTitle("daemon unreachable")
			mToggle.SetTitle("Connect")
		case s.Running:
			systray.SetTitle("turnrelay ●")
			line := "connected"
			if s.Egress != "" {
				line += "  " + s.Egress
			}
			if s.Workers > 0 {
				line += fmt.Sprintf("  (%d)", s.Workers)
			}
			if s.MeanRTTMs > 0 {
				line += fmt.Sprintf("  %dms  loss %.1f%%", s.MeanRTTMs, s.MaxLossPct)
			}
			if s.Evictions > 0 {
				line += fmt.Sprintf("  evicted %d", s.Evictions)
			}
			mStatus.SetTitle(line)
			mToggle.SetTitle("Disconnect")
		default:
			systray.SetTitle("turnrelay ○")
			mStatus.SetTitle("disconnected")
			mToggle.SetTitle("Connect")
		}
	}
	poll := func() { render(fetchStatus()) }
	poll()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-mToggle.ClickedCh:
			if busy {
				break
			}
			busy = true
			mStatus.SetTitle("working...")
			go func() {
				s, _ := fetchStatus()
				if s.Running {
					_ = down()
				} else {
					_ = up()
				}
				done <- struct{}{}
			}()
		case <-done:
			busy = false
			poll()
		case <-mProfile.ClickedCh:
			openInEditor(profilePath())
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		case <-ticker.C:
			if !busy {
				poll()
			}
		}
	}
}
