package observability

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

var startTime = time.Now()

const (
	colorReset    = "\033[0m"
	colorCyan     = "\033[36m"
	colorBlue     = "\033[34m"
	colorBold     = "\033[1m"
	colorPurple   = "\033[35m"
	colorNeonCyan = "\033[96m"
	colorNeonMag  = "\033[95m"
)

var radarFrames = []string{"◜", "◝", "◞", "◟"}
var radarIdx = 0

// termMu synchronizes ALL terminal output so that the cursor
// save/restore in PrintLiveStatus can never be interrupted by a log write.
var termMu sync.Mutex

// ------------------------------------------------------------
// Utility
// ------------------------------------------------------------

func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 80
	}
	return w
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// ------------------------------------------------------------
// TermWriter – a mutex-guarded io.Writer for log output.
// Every log.Println call will go through this writer, ensuring
// the cursor is safely inside the scroll region before writing.
// ------------------------------------------------------------

type termWriter struct{}

func (tw termWriter) Write(p []byte) (n int, err error) {
	termMu.Lock()
	defer termMu.Unlock()
	return os.Stderr.Write(p)
}

// NewTermWriter returns an io.Writer suitable for log.SetOutput().
// It serialises writes with PrintLiveStatus via termMu.
func NewTermWriter() *termWriter {
	return &termWriter{}
}

// ------------------------------------------------------------
// Banner
// ------------------------------------------------------------

func PrintBanner() {
	fmt.Print("\033[2J\033[H")

	banner := `
   __  _________   _______  ______  ____
  /  |/  /  _/   | / ___/ / / / __ \/  _/
 / /|_/ // // /| | \__ \ /_/ / /_/ // /
/ /  / // // ___ |___/ / __  / _, _// /
/_/  /_/___/_/  |_/____/_/ /_/_/ |_/___/

        >> THE SOVEREIGN AGENTIC ENGINE <<
`

	width := termWidth()
	lines := strings.Split(banner, "\n")

	for _, l := range lines {
		padding := (width - len(l)) / 2
		if padding < 0 {
			padding = 0
		}
		fmt.Printf("%s%s%s\n", strings.Repeat(" ", padding), colorNeonCyan+l, colorReset)
	}
}

func InitializeTerminal() {
	// Header/Logo area: 1-9
	// Dashboard/Status: 10
	// Gap: 11
	// Scrolling Logs: 12+
	fmt.Print("\033[12;r")  // Set scrolling region from line 12 to the bottom
	fmt.Print("\033[12;1H") // Move cursor to the start of the scrolling region
}

func CleanupTerminal() {
	fmt.Print("\033[r\033[2J\033[H")
}

// ------------------------------------------------------------
// Live Status
// ------------------------------------------------------------

func PrintLiveStatus() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	uptime := time.Since(startTime).Round(time.Second)
	memMB := float64(m.Alloc) / 1024 / 1024

	role, task, lastHB := GetStatus()

	// Pulse Logic
	pulseIcon := "🔴"
	pulseText := "OFFLINE"
	pulseColor := colorNeonMag

	delta := time.Since(lastHB)

	if delta < 40*time.Second {
		pulseIcon = "🟢"
		pulseText = "HEALTHY"
		pulseColor = colorNeonCyan
	} else if delta < 90*time.Second {
		pulseIcon = "🟡"
		pulseText = "LAGGING"
		pulseColor = colorPurple
	}

	// Role Icon
	icon := "💤"
	roleColor := colorReset

	switch role {
	case RoleMaster:
		icon = "🛰️"
		roleColor = colorNeonCyan
	case RoleSlave:
		icon = "⚙️"
		roleColor = colorNeonMag
	}

	// Radar Animation
	radar := " "
	if role != RoleIdle {
		radar = radarFrames[radarIdx]
		radarIdx = (radarIdx + 1) % len(radarFrames)
	}

	// Task Truncation
	displayTask := task
	if displayTask == "" {
		displayTask = "Waiting..."
	}
	if len(displayTask) > 25 {
		displayTask = displayTask[:22] + "..."
	}

	// Memory Bar (Percent Based)
	totalMB := float64(m.Sys) / 1024 / 1024
	memPercent := memMB / totalMB

	barWidth := 20
	filled := clamp(int(memPercent*float64(barWidth)), 0, barWidth)

	bar := strings.Repeat("█", filled) +
		strings.Repeat("▒", barWidth-filled)

	barColor := colorNeonCyan
	if memPercent > 0.7 {
		barColor = colorNeonMag
	}

	// Build the status string BEFORE locking, to minimise lock hold time.
	statusStr := fmt.Sprintf(
		"\033[s\033[10;1H\033[K%s[%s] %s%s %-10s%s | %s[%s%s %-6s%s] [%s] %s%s%s [%v] [%s%s %.1fMB%s]\033[u",
		colorReset,
		lastHB.Format("15:04:05"),
		pulseColor, pulseIcon, pulseText, colorReset,
		colorReset,
		roleColor, icon, role, colorReset,
		displayTask,
		colorPurple, radar, colorReset,
		uptime,
		barColor, bar, memMB, colorReset,
	)

	// Lock, write the ENTIRE escape sequence atomically, unlock.
	termMu.Lock()
	fmt.Print(statusStr)
	termMu.Unlock()
}
