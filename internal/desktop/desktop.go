// Package desktop talks to the user's desktop environment: it opens URLs in
// the default browser and directories in the file manager.
//
// Linux and the BSDs use xdg-open (falling back to gio and, for URLs,
// sensible-browser); macOS uses open.
package desktop

import (
	"errors"
	"os/exec"
	"runtime"
)

// ErrNoOpener is returned when no suitable program is installed.
var ErrNoOpener = errors.New("no program to open files found (install xdg-utils)")

// Open shows target – a URL or a local path – with the desktop's default
// application. It does not wait for that application to exit.
func Open(target string) error {
	for _, c := range commands(runtime.GOOS, target) {
		bin, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(bin, c[1:]...)
		if err := cmd.Start(); err != nil {
			return err
		}
		go cmd.Wait() // reap the process
		return nil
	}
	return ErrNoOpener
}

// commands lists the candidate command lines for an OS, best first.
func commands(goos, target string) [][]string {
	if goos == "darwin" {
		return [][]string{{"open", target}}
	}
	return [][]string{{"xdg-open", target}, {"gio", "open", target}, {"sensible-browser", target}}
}
