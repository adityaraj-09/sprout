package auth

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// OpenBrowser opens url in the default browser. Failure is non-fatal for login.
func OpenBrowser(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("empty url")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = os.Stdin
	return cmd.Start()
}
