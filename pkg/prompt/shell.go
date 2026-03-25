package prompt

import (
	"os"
	"runtime"
)

func getShellInfo() string {
	switch runtime.GOOS {
	case "darwin", "linux", "freebsd", "openbsd":
		if s := os.Getenv("SHELL"); s != "" {
			return s
		}
		if runtime.GOOS == "darwin" {
			return "/bin/zsh"
		}
		return "/bin/bash"
	case "windows":
		return "PowerShell/cmd.exe"
	default:
		if s := os.Getenv("SHELL"); s != "" {
			return s
		}
		return "/bin/bash"
	}
}
