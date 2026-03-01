package observability

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// EventType defines the category of the log event.
type EventType string

const (
	EventTypeReasoning   EventType = "reasoning"
	EventTypeToolCall    EventType = "tool_call"
	EventTypeToolResult  EventType = "tool_result"
	EventTypePolicyCheck EventType = "policy_check"
	EventTypeCost        EventType = "cost"
	EventTypePlan        EventType = "plan"
	EventTypeStep        EventType = "step"
	EventTypeHeartbeat   EventType = "heartbeat"
	EventTypeLLM         EventType = "llm"
)

// Event represents a structured log entry.
type Event struct {
	Type      EventType `json:"type"`
	ChatID    string    `json:"chat_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	Data      any       `json:"data"`
	Timestamp time.Time `json:"timestamp"`
}

// Logger handles structured logging.
type Logger struct {
	llmLogPath string
	maxSize    int64
}

func NewLogger() *Logger {
	return &Logger{
		llmLogPath: filepath.Join("logs", "llm.jsonl"),
		maxSize:    10 * 1024 * 1024, // 10MB
	}
}

// Log emits a structured JSON event to stdout.
func (l *Logger) Log(evt Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	data, err := json.Marshal(evt)
	if err != nil {
		fmt.Printf("{\"error\": \"failed to marshal event: %v\"}\n", err)
		return
	}
	fmt.Println(string(data))

	if evt.Type == EventTypeLLM {
		l.writeToFile(data)
	}
}

func (l *Logger) writeToFile(data []byte) {
	if err := os.MkdirAll(filepath.Dir(l.llmLogPath), 0755); err != nil {
		log.Printf("failed to create log directory: %v", err)
		return
	}

	// Check size before writing
	info, err := os.Stat(l.llmLogPath)
	if err == nil && info.Size() > l.maxSize {
		l.rotateLogs()
	}

	f, err := os.OpenFile(l.llmLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("failed to open log file: %v", err)
		return
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		log.Printf("failed to write to log file: %v", err)
	}
}

func (l *Logger) rotateLogs() {
	// Simple rotation: keep one .old file
	oldPath := l.llmLogPath + ".old"
	_ = os.Remove(oldPath)
	_ = os.Rename(l.llmLogPath, oldPath)
}

// Helper methods for common events

func (l *Logger) LogReasoning(chatID, taskID, content string) {
	l.Log(Event{
		Type:   EventTypeReasoning,
		ChatID: chatID,
		TaskID: taskID,
		Data:   map[string]string{"content": content},
	})
}

func (l *Logger) LogToolCall(chatID, taskID, tool, args string) {
	l.Log(Event{
		Type:   EventTypeToolCall,
		ChatID: chatID,
		TaskID: taskID,
		Data: map[string]string{
			"tool": tool,
			"args": args,
		},
	})
}

func (l *Logger) LogCost(chatID, taskID string, promptTokens, completionTokens int, model string) {
	l.Log(Event{
		Type:   EventTypeCost,
		ChatID: chatID,
		TaskID: taskID,
		Data: map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
			"model":             model,
		},
	})
}

func (l *Logger) LogHeartbeat() {
	l.Log(Event{
		Type: EventTypeHeartbeat,
		Data: map[string]string{"status": "alive"},
	})
}

func (l *Logger) LogLLM(chatID, taskID string, prompt any, response string, toolCalls any) {
	l.Log(Event{
		Type:   EventTypeLLM,
		ChatID: chatID,
		TaskID: taskID,
		Data: map[string]any{
			"prompt":     prompt,
			"response":   response,
			"tool_calls": toolCalls,
		},
	})
}
