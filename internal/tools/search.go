package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tmc/langchaingo/tools/duckduckgo"
)

type SearchTool struct {
	client *duckduckgo.Tool
}

func NewSearchTool() (*SearchTool, error) {
	ddg, err := duckduckgo.New(10, duckduckgo.DefaultUserAgent)
	if err != nil {
		return nil, err
	}
	return &SearchTool{client: ddg}, nil
}

func (s *SearchTool) Name() string {
	return "search"
}

func (s *SearchTool) Description() string {
	return "Search the web using DuckDuckGo for real-time information."
}

func (s *SearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query to look up",
			},
		},
		"required": []string{"query"},
	}
}

func (s *SearchTool) Execute(ctx context.Context, input string) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}

	res, err := s.client.Call(ctx, args.Query)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	return res, nil
}
