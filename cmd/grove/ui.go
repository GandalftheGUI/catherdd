package main

import "fmt"

const (
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorReset  = "\033[0m"
)

func colorState(state string) string {
	switch state {
	case "RUNNING":
		return "\033[32m"
	case "WAITING":
		return "\033[33m"
	case "ATTACHED":
		return "\033[36m"
	case "CHECKING":
		return "\033[36m"
	case "EXITED":
		return "\033[2m"
	case "CRASHED":
		return "\033[31m"
	case "KILLED":
		return "\033[33m"
	case "FINISHED":
		return "\033[2m"
	default:
		return ""
	}
}

func formatUptime(secs int64) string {
	if secs < 0 {
		secs = 0
	}
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	if secs < 3600 {
		return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%dh%02dm", secs/3600, (secs%3600)/60)
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
