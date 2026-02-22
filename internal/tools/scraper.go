package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-shiori/go-readability"
	"github.com/microcosm-cc/bluemonday"
)

type ScraperTool struct {
	UserAgent string
}

func NewScraperTool() *ScraperTool {
	return &ScraperTool{
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
	}
}

func (s *ScraperTool) Name() string {
	return "scraper"
}

func (s *ScraperTool) Description() string {
	return "Fetch a webpage URL and extract the main content as clean, sanitized text."
}

func (s *ScraperTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The full URL of the webpage to scrape (e.g., https://example.com/article)",
			},
		},
		"required": []string{"url"},
	}
}

func (s *ScraperTool) Execute(ctx context.Context, input string) (string, error) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}

	// Fetch the URL
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", args.URL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", s.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch URL: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch URL: status code %d", resp.StatusCode)
	}

	parsedURL, err := url.Parse(args.URL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL: %v", err)
	}

	// Use readability to extract content
	article, err := readability.FromReader(resp.Body, parsedURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse article: %v", err)
	}

	// Sanitize output (remove any remaining HTML tags or scripts)
	p := bluemonday.StrictPolicy()
	sanitized := p.Sanitize(article.TextContent)

	// Combine into a structured report for the LLM
	output := fmt.Sprintf("TITLE: %s\n", article.Title)
	if article.Excerpt != "" {
		output += fmt.Sprintf("EXCERPT: %s\n", article.Excerpt)
	}
	output += "\n-- CONTENT --\n"

	// Limit content length to avoid massive token usage (e.g., 50k chars)
	content := sanitized
	if len(content) > 50000 {
		content = content[:50000] + "\n... (content truncated) ..."
	}
	output += content

	return output, nil
}
