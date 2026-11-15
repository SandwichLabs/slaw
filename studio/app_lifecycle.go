package main

import (
	"context"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// beforeClose is wired as Wails' OnBeforeClose option (studio/main.go). It
// intercepts the first window-close/quit attempt and returns true
// (preventing the close) while emitting "app:before-close" so the frontend can
// flush any pending debounced saves (e.g. a notebook's autosave). The
// frontend then calls Quit(); Quit() sets quitConfirmed so THIS same check
// lets the next close attempt through instead of looping forever.
//
// A one-shot watchdog timer (closeWatchdogTimeout) force-quits if the
// frontend never responds — a hung or suspended webview process must not be
// able to make the window impossible to close. A SECOND close attempt while
// one is already pending lets the close through immediately.
func (a *App) beforeClose(_ context.Context) (prevent bool) {
	// An update is installing: closing now would orphan install.sh and lose its result. Hold the
	// close (the frontend says why); the person quits again once it has finished.
	if a.installing.Load() {
		a.emitEvent("app:update-installing", map[string]any{})
		return true
	}
	if a.quitConfirmed.Load() {
		return false
	}

	if a.closePending.CompareAndSwap(false, true) {
		a.emitEvent("app:before-close", map[string]any{})
		watchdog := a.closeWatchdog
		if watchdog == nil {
			watchdog = func(d time.Duration, f func()) func() {
				t := time.AfterFunc(d, f)
				return func() { t.Stop() }
			}
		}
		stop := watchdog(closeWatchdogTimeout, a.Quit)
		a.watchdogMu.Lock()
		a.watchdogStop = stop
		a.watchdogMu.Unlock()
		return true
	}

	a.quitConfirmed.Store(true)
	return false
}

// Quit is called by the frontend once it has flushed (or timed out waiting
// on) every pending save in response to "app:before-close", and by
// beforeClose's own watchdog timer if the frontend never responds at all. It
// marks the close as confirmed and asks Wails to quit again, which this time
// reaches beforeClose with quitConfirmed already set.
func (a *App) Quit() {
	a.quitConfirmed.Store(true)
	quit := a.quitFn
	if quit == nil {
		quit = func() { runtime.Quit(a.ctx) }
	}
	quit()
}

// CancelQuit abandons a close attempt (the person chose to stay because a
// notebook could not be saved); the next close starts the flow again.
func (a *App) CancelQuit() {
	a.restart.disarm() // an update's "Restart now" is off too: nothing starts a second instance
	a.HoldQuit()
	a.closePending.Store(false)
}

// HoldQuit stops the hung-webview watchdog. The frontend calls it when a flush
// outlasts 3 s (it keeps waiting, bounded, and treats a timeout as a failed
// save: #62) and before it asks "Quit anyway?": a frontend that can show a
// question is not hung, and the timer must not quit the app while the person
// is deciding.
func (a *App) HoldQuit() {
	a.watchdogMu.Lock()
	stop := a.watchdogStop
	a.watchdogStop = nil
	a.watchdogMu.Unlock()
	if stop != nil {
		stop()
	}
}
