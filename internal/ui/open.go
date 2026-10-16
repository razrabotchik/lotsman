package ui

import (
	"context"
	"os/exec"
	"runtime"
)

// Open asks the desktop to open url. Failure is not an error: the address
// is printed either way.
func Open(ctx context.Context, url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", url)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", url)
	}
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }() // reap the opener; its exit status says nothing useful
	}
}
