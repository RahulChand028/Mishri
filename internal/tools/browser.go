package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

type BrowserTool struct {
	mu            sync.Mutex
	allocCtx      context.Context
	browserCtx    context.Context
	allocCancel   context.CancelFunc
	browserCancel context.CancelFunc
}

func NewBrowserTool() *BrowserTool {
	return &BrowserTool{}
}

func (b *BrowserTool) Name() string {
	return "browser"
}

func (b *BrowserTool) Description() string {
	return "Control a browser to interact with websites. The browser window remains open until you call 'close'. Actions: 'navigate', 'click', 'content', 'type', 'press', 'scroll', 'wait', 'back', 'forward', 'reload', 'screenshot', 'close'. " +
		"IMPORTANT: For search engines (DuckDuckGo, Google, Bing etc.), always use 'navigate' with a pre-built query URL " +
		"(e.g. https://duckduckgo.com/?q=your+query or https://www.google.com/search?q=your+query) instead of 'type' into the search box. " +
		"The 'type' action is for form inputs and interactive elements on regular pages, not search engine homepages."
}

func (b *BrowserTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{
					"navigate", "click", "content", "type", "press",
					"scroll", "wait", "back", "forward", "reload",
					"screenshot", "close",
				},
				"description": "The action to perform.",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "The URL to navigate to (required for 'navigate')",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "CSS selector for the target element (required for 'click', 'type', 'press', 'scroll', 'wait')",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "The text to type or key to press (required for 'type', 'press')",
			},
			"wait_seconds": map[string]any{
				"type":        "integer",
				"description": "Time to wait in seconds (used with 'wait')",
			},
		},
		"required": []string{"action"},
	}
}

func (b *BrowserTool) initBrowser(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.browserCtx != nil {
		select {
		case <-b.browserCtx.Done():
			b.cleanup()
		default:
			return nil
		}
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.Flag("headless", false),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
	)

	// Use Background so the browser persists across multiple tool calls within a session.
	// Per-action contexts are only for individual actions, not the browser lifetime.
	b.allocCtx, b.allocCancel = chromedp.NewExecAllocator(context.Background(), opts...)
	b.browserCtx, b.browserCancel = chromedp.NewContext(b.allocCtx)

	return chromedp.Run(b.browserCtx)
}

func (b *BrowserTool) cleanup() {
	if b.browserCancel != nil {
		b.browserCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	b.browserCtx = nil
	b.allocCtx = nil
}

func (b *BrowserTool) Execute(ctx context.Context, input string) (string, error) {
	var args struct {
		Action      string `json:"action"`
		URL         string `json:"url"`
		Selector    string `json:"selector"`
		Text        string `json:"text"`
		WaitSeconds int    `json:"wait_seconds"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}

	if args.Action == "close" {
		b.mu.Lock()
		b.cleanup()
		b.mu.Unlock()
		return "Successfully closed the browser.", nil
	}

	if err := b.initBrowser(ctx); err != nil {
		return "", fmt.Errorf("failed to initialize browser: %v", err)
	}

	// Per-action timeout: navigate is slow (page loads), so gets more time.
	// All other actions should fail fast.
	timeout := 15 * time.Second
	if args.Action == "navigate" {
		timeout = 30 * time.Second
	}
	actionCtx, cancel := context.WithTimeout(b.browserCtx, timeout)
	defer cancel()

	var result string
	var err error

	switch args.Action {
	case "navigate":
		if args.URL == "" {
			return "Error: url is required for 'navigate'", nil
		}
		// Navigate and wait for DOMContentLoaded so content is ready immediately.
		err = chromedp.Run(actionCtx,
			chromedp.Navigate(args.URL),
			chromedp.WaitReady("body", chromedp.ByQuery),
		)
		result = fmt.Sprintf("Successfully navigated to %s", args.URL)

	case "content":
		// Extract visible text with innerText, not raw HTML.
		// navigate already waits for DOMContentLoaded, but for SPAs we
		// use a short JS-based idle wait rather than a hardcoded sleep.
		var text string
		err = chromedp.Run(actionCtx,
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &text),
		)
		const maxChars = 8000
		if len(text) > maxChars {
			text = text[:maxChars] + "\n... (truncated, page has more content)"
		}
		if text == "" {
			result = "Page loaded but no visible text found (may be empty or require interaction)."
		} else {
			result = text
		}

	case "click":
		if args.Selector == "" {
			return "Error: selector required", nil
		}
		// Fast element-existence check (same pattern as 'type').
		checkCtxC, checkCancelC := context.WithTimeout(b.browserCtx, 5*time.Second)
		var elExists bool
		_ = chromedp.Run(checkCtxC, chromedp.Evaluate(
			fmt.Sprintf(`document.querySelector(%q) !== null`, args.Selector), &elExists,
		))
		checkCancelC()
		if !elExists {
			return fmt.Sprintf(
				"Element not found: selector %q does not exist. Try a different selector or scroll the page first.",
				args.Selector,
			), nil
		}
		err = chromedp.Run(actionCtx, chromedp.Click(args.Selector, chromedp.ByQuery))
		result = fmt.Sprintf("Clicked %s", args.Selector)

	case "type":
		if args.Selector == "" || args.Text == "" {
			return "Error: selector and text required", nil
		}
		// Block typing into search engine homepages — use navigate with a query URL instead.
		{
			var currentURL string
			guardCtx, guardCancel := context.WithTimeout(b.browserCtx, 3*time.Second)
			_ = chromedp.Run(guardCtx, chromedp.Evaluate(`window.location.href`, &currentURL))
			guardCancel()
			for _, se := range []string{"duckduckgo.com", "google.com/search", "bing.com/search"} {
				if strings.Contains(currentURL, se) {
					return "Error: do not type into search engine pages. Use the 'navigate' action with a pre-built query URL instead, e.g. navigate to https://duckduckgo.com/?q=" + strings.ReplaceAll(args.Text, " ", "+"), nil
				}
			}
		}
		// Fast element-existence check using JS (≤3s).
		// This avoids blocking for the full timeout on a missing/stale selector.
		checkCtx, checkCancel := context.WithTimeout(b.browserCtx, 3*time.Second)
		var exists bool
		jsCheck := fmt.Sprintf(`document.querySelector(%q) !== null`, args.Selector)
		_ = chromedp.Run(checkCtx, chromedp.Evaluate(jsCheck, &exists))
		checkCancel()

		if !exists {
			return fmt.Sprintf(
				"Element not found: selector %q does not exist on the current page. "+
					"Tip: for search engines, use the 'navigate' action with a pre-built query URL "+
					"(e.g. https://duckduckgo.com/?q=your+query) instead of typing in the search box.",
				args.Selector,
			), nil
		}

		// Strip any trailing newline/carriage-return from the typed text.
		// Some LLMs append \n or "\nReturn" to submit a form — this causes
		// the literal characters to be inserted instead of triggering Enter.
		inputText := strings.TrimRight(args.Text, "\r\n")
		shouldSubmit := len(inputText) < len(args.Text) // trailing newline was present

		actions := []chromedp.Action{
			chromedp.SendKeys(args.Selector, inputText, chromedp.ByQuery),
		}
		if shouldSubmit {
			actions = append(actions, chromedp.KeyEvent("Return"))
		}
		err = chromedp.Run(actionCtx, actions...)
		if shouldSubmit {
			result = fmt.Sprintf("Typed %q in %s and pressed Enter to submit", inputText, args.Selector)
		} else {
			result = fmt.Sprintf("Typed text in %s", args.Selector)
		}

	case "press":
		if args.Text == "" {
			return "Error: text (key) required", nil
		}
		// Normalize common aliases so workers can say "Enter" or "Return" interchangeably.
		key := args.Text
		if strings.EqualFold(key, "enter") {
			key = "Return"
		}
		err = chromedp.Run(actionCtx, chromedp.KeyEvent(key))
		result = fmt.Sprintf("Pressed key: %s", key)

	case "scroll":
		if args.Selector != "" {
			err = chromedp.Run(actionCtx, chromedp.ScrollIntoView(args.Selector, chromedp.ByQuery))
			result = fmt.Sprintf("Scrolled to %s", args.Selector)
		} else {
			err = chromedp.Run(actionCtx, chromedp.Evaluate("window.scrollTo(0, document.body.scrollHeight)", nil))
			result = "Scrolled to bottom"
		}

	case "wait":
		if args.Selector != "" {
			err = chromedp.Run(actionCtx, chromedp.WaitVisible(args.Selector, chromedp.ByQuery))
			result = fmt.Sprintf("Finished waiting for %s", args.Selector)
		} else if args.WaitSeconds > 0 {
			time.Sleep(time.Duration(args.WaitSeconds) * time.Second)
			result = fmt.Sprintf("Waited for %d seconds", args.WaitSeconds)
		} else {
			result = "Nothing to wait for"
		}

	case "back":
		err = chromedp.Run(actionCtx, chromedp.NavigateBack())
		result = "Navigated back"

	case "forward":
		err = chromedp.Run(actionCtx, chromedp.NavigateForward())
		result = "Navigated forward"

	case "reload":
		err = chromedp.Run(actionCtx, chromedp.Reload())
		result = "Page reloaded"

	case "screenshot":
		var buf []byte
		err = chromedp.Run(actionCtx, chromedp.CaptureScreenshot(&buf))
		if err == nil {
			os.MkdirAll("screenshots", 0755)
			filename := fmt.Sprintf("screenshot_%d.png", time.Now().Unix())
			path := filepath.Join("screenshots", filename)
			err = os.WriteFile(path, buf, 0644)
			if err == nil {
				absPath, _ := filepath.Abs(path)
				result = fmt.Sprintf("Screenshot saved to %s", absPath)
			}
		}

	default:
		return "Invalid action", nil
	}

	if err != nil {
		return fmt.Sprintf("Browser action failed: %v", err), nil
	}

	return result, nil
}
