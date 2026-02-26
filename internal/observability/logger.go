package observability

import (
	"encoding/json"
	"fmt"
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
type Logger struct{}

func NewLogger() *Logger {
	return &Logger{}
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
