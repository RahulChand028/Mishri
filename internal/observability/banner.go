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
	// Dashboard Line 1: 10 (role, health, agent, task)
	// Dashboard Line 2: 11 (pipeline, tokens, cost)
	// Gap: 12
	// Scrolling Logs: 13+
	fmt.Print("\033[13;r")  // Set scrolling region from line 13 to the bottom
	fmt.Print("\033[13;1H") // Move cursor to the start of the scrolling region
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

	d := GetDashboard()

	// ---- Pulse / Health ----
	pulseIcon := "🔴"
	pulseText := "OFFLINE"
	pulseColor := colorNeonMag

	delta := time.Since(d.LastHeartbeat)
	if delta < 40*time.Second {
		pulseIcon = "🟢"
		pulseText = "HEALTHY"
		pulseColor = colorNeonCyan
	} else if delta < 90*time.Second {
		pulseIcon = "🟡"
		pulseText = "LAGGING"
		pulseColor = colorPurple
	}

	// ---- Role ----
	icon := "💤"
	roleColor := colorReset
	switch d.Role {
	case RoleMaster:
		icon = "🛰️"
		roleColor = colorNeonCyan
	case RoleSlave:
		icon = "⚙️"
		roleColor = colorNeonMag
	}

	// ---- Radar ----
	radar := " "
	if d.Role != RoleIdle {
		radar = radarFrames[radarIdx]
		radarIdx = (radarIdx + 1) % len(radarFrames)
	}

	// ---- Task ----
	displayTask := d.Task
	if displayTask == "" {
		displayTask = "Waiting..."
	}
	if len(displayTask) > 30 {
		displayTask = displayTask[:27] + "..."
	}

	// ---- LINE 1: Health | Role | Agent | Task | Memory ----
	totalMB := float64(m.Sys) / 1024 / 1024
	memPercent := memMB / totalMB
	barWidth := 12
	filled := clamp(int(memPercent*float64(barWidth)), 0, barWidth)
	memBar := strings.Repeat("█", filled) + strings.Repeat("▒", barWidth-filled)
	memColor := colorNeonCyan
	if memPercent > 0.7 {
		memColor = colorNeonMag
	}

	line1 := fmt.Sprintf(
		"\033[10;1H\033[K%s[%s] %s%s %-7s%s │ %s%s %-6s%s │ %s%s%s │ %s │ %s%s%.0fMB%s",
		colorReset,
		d.LastHeartbeat.Format("15:04:05"),
		pulseColor, pulseIcon, pulseText, colorReset,
		roleColor, icon, d.Role, colorReset,
		colorPurple, radar, colorReset,
		displayTask,
		memColor, memBar, memMB, colorReset,
	)

	// ---- LINE 2: Pipeline | Tokens | Cost | Elapsed | Parallel ----
	var line2 string
	if d.TotalAgents > 0 || d.PromptTokens > 0 || d.Role != RoleIdle {
		// Pipeline progress bar
		pipeBar := ""
		if d.TotalAgents > 0 {
			pw := 15
			done := clamp(d.CompletedAgents*pw/d.TotalAgents, 0, pw)
			pipeBar = fmt.Sprintf(
				"%s[%s%s%s%s] %d/%d",
				colorNeonCyan,
				colorNeonCyan, strings.Repeat("█", done),
				colorReset+strings.Repeat("░", pw-done),
				colorNeonCyan,
				d.CompletedAgents, d.TotalAgents,
			)
			if d.FailedAgents > 0 {
				pipeBar += fmt.Sprintf(" %s✗%d%s", colorNeonMag, d.FailedAgents, colorReset)
			}
		} else {
			pipeBar = fmt.Sprintf("%s─%s", colorCyan, colorReset)
		}

		// Active agent
		agentInfo := ""
		if d.ActiveAgentID > 0 {
			agentInfo = fmt.Sprintf(" │ %s⚙ Agent %d (%s)%s", colorNeonMag, d.ActiveAgentID, d.ActiveAgentType, colorReset)
		}

		// Parallel indicator
		parallelInfo := ""
		if d.ParallelCount > 1 {
			parallelInfo = fmt.Sprintf(" │ %s⚡%d parallel%s", colorPurple, d.ParallelCount, colorReset)
		}

		// Tokens
		totalTokens := d.PromptTokens + d.CompletionTokens
		tokenStr := ""
		if totalTokens > 0 {
			if totalTokens > 1000 {
				tokenStr = fmt.Sprintf(" │ 📊 %.1fk tok", float64(totalTokens)/1000)
			} else {
				tokenStr = fmt.Sprintf(" │ 📊 %d tok", totalTokens)
			}
		}

		// Cost
		costStr := ""
		if d.TotalCost > 0 {
			costStr = fmt.Sprintf(" │ 💰 $%.4f", d.TotalCost)
		}

		// Elapsed time for current task
		elapsed := ""
		if !d.TaskStart.IsZero() && d.Role != RoleIdle {
			dur := time.Since(d.TaskStart).Round(time.Second)
			elapsed = fmt.Sprintf(" │ ⏱ %v", dur)
		}

		line2 = fmt.Sprintf(
			"\033[11;1H\033[K  %s%s%s%s%s%s │ %s%v%s",
			pipeBar, agentInfo, parallelInfo, tokenStr, costStr, elapsed,
			colorCyan, uptime, colorReset,
		)
	} else {
		// Idle — just show uptime
		line2 = fmt.Sprintf(
			"\033[11;1H\033[K  %s─────── STANDBY ───────%s │ %s%v%s",
			colorCyan, colorReset,
			colorCyan, uptime, colorReset,
		)
	}

	// Lock, write both lines atomically, unlock.
	termMu.Lock()
	fmt.Print("\033[s" + line1 + line2 + "\033[u")
	termMu.Unlock()
}
