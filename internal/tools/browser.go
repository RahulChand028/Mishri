package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chromedp/cdproto/dom"
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
	return "Control a browser to interact with websites. The browser window remains open until you call 'close'. Actions: 'navigate', 'click', 'content', 'type', 'press', 'scroll', 'wait', 'back', 'forward', 'reload', 'screenshot', 'close'."
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

	actionCtx, cancel := context.WithTimeout(b.browserCtx, 60*time.Second)
	defer cancel()

	var result string
	var err error

	switch args.Action {
	case "navigate":
		if args.URL == "" {
			return "Error: url is required for 'navigate'", nil
		}
		err = chromedp.Run(actionCtx, chromedp.Navigate(args.URL))
		result = fmt.Sprintf("Successfully navigated to %s", args.URL)

	case "content":
		var html string
		err = chromedp.Run(actionCtx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				node, err := dom.GetDocument().Do(ctx)
				if err != nil {
					return err
				}
				html, err = dom.GetOuterHTML().WithNodeID(node.NodeID).Do(ctx)
				return err
			}),
		)
		if len(html) > 50000 {
			html = html[:50000] + "\n... (truncated)"
		}
		result = html

	case "click":
		if args.Selector == "" {
			return "Error: selector required", nil
		}
		err = chromedp.Run(actionCtx, chromedp.Click(args.Selector, chromedp.ByQuery))
		result = fmt.Sprintf("Clicked %s", args.Selector)

	case "type":
		if args.Selector == "" || args.Text == "" {
			return "Error: selector and text required", nil
		}
		err = chromedp.Run(actionCtx, chromedp.SendKeys(args.Selector, args.Text, chromedp.ByQuery))
		result = fmt.Sprintf("Typed text in %s", args.Selector)

	case "press":
		if args.Text == "" {
			return "Error: text (key) required", nil
		}
		err = chromedp.Run(actionCtx, chromedp.KeyEvent(args.Text))
		result = fmt.Sprintf("Pressed key: %s", args.Text)

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
