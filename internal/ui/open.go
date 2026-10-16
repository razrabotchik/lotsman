package ui

import (
	"context"
	"html"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// launcherLifetime is how long the launcher file outlives the opener: long
// enough for a browser to start and read it.
const launcherLifetime = 60 * time.Second

// Open asks the desktop to open url. The address carries the token, and a
// command line is visible to every local user (ps, /proc), so the opener is
// handed a private file that forwards to it instead: 0600, in the temporary
// directory, removed after a minute or by the returned cleanup, whichever
// comes first. Failure is not an error: the address is printed either way.
func Open(ctx context.Context, url string) (cleanup func()) {
	page, err := launcher(url)
	if err != nil {
		return func() {}
	}
	var once sync.Once
	cleanup = func() { once.Do(func() { _ = os.Remove(page) }) }
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", page)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", page)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", page)
	}
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }() // reap the opener; its exit status says nothing useful
	}
	go func() {
		timer := time.NewTimer(launcherLifetime)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
		cleanup()
	}()
	return cleanup
}

// launcher writes the forwarding page and returns its path.
func launcher(url string) (string, error) {
	f, err := os.CreateTemp("", "lotsman-ui-*.html") // created 0600
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(launcherPage(url))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func launcherPage(url string) string {
	u := html.EscapeString(url)
	return `<!doctype html><html><head><meta charset="utf-8">` +
		`<meta http-equiv="refresh" content="0;url=` + u + `"><title>lotsman ui</title></head>` +
		`<body><p><a href="` + u + `">Open lotsman ui</a></p></body></html>
`
}
