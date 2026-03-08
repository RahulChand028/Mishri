package observability

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
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
	EventTypeAgentStart  EventType = "agent_start"
	EventTypeAgentEnd    EventType = "agent_end"
)

// Event represents a structured log entry.
type Event struct {
	Type          EventType `json:"type"`
	ChatID        string    `json:"chat_id,omitempty"`
	TaskID        string    `json:"task_id,omitempty"`
	ParentChatID  string    `json:"parent_chat_id,omitempty"`
	ParentAgentID int       `json:"parent_agent_id,omitempty"`
	ParentTaskID  string    `json:"parent_task_id,omitempty"`
	Data          any       `json:"data"`
	Timestamp     time.Time `json:"timestamp"`
}

// Logger handles structured logging.
type Logger struct {
	logPath string
	maxSize int64

	mu          sync.RWMutex
	subscribers map[chan Event]bool
}

func NewLogger() *Logger {
	return &Logger{
		logPath:     filepath.Join("logs", "events.jsonl"),
		maxSize:     10 * 1024 * 1024, // 10MB
		subscribers: make(map[chan Event]bool),
	}
}

// Subscribe returns a channel that receives all logged events.
func (l *Logger) Subscribe() chan Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch := make(chan Event, 100)
	l.subscribers[ch] = true
	return ch
}

// Unsubscribe removes a event channel from the subscriber list.
func (l *Logger) Unsubscribe(ch chan Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.subscribers, ch)
	close(ch)
}

// isVerbose returns true for event types that should NOT appear on the console.
// These are high-volume or data-heavy events that belong in log files only.
func isVerbose(t EventType) bool {
	switch t {
	case EventTypeLLM, EventTypeReasoning, EventTypeToolCall, EventTypeToolResult:
		return true
	default:
		return false
	}
}

// Log writes all events to the log file. Only non-verbose events get a
// short one-line summary printed to the console (via log.Printf).
func (l *Logger) Log(evt Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	data, err := json.Marshal(evt)
	if err != nil {
		log.Printf("[obs] failed to marshal event: %v", err)
		return
	}

	// Always persist to log file
	l.writeToFile(data)

	// Broadcast to all subscribers
	l.mu.RLock()
	for ch := range l.subscribers {
		// Non-blocking send to avoid hanging if subscriber is slow
		select {
		case ch <- evt:
		default:
		}
	}
	l.mu.RUnlock()

	// Only print non-verbose events to console as a short summary
	if !isVerbose(evt.Type) {
		l.printConsoleSummary(evt)
	}
}

// printConsoleSummary writes a short, human-readable line to the console.
func (l *Logger) printConsoleSummary(evt Event) {
	switch evt.Type {
	case EventTypePlan:
		log.Printf("[PLAN] New agent plan received")
	case EventTypeStep:
		if m, ok := evt.Data.(map[string]string); ok {
			log.Printf("[STEP] %s", m["content"])
		} else {
			log.Printf("[STEP] %s", evt.TaskID)
		}
	case EventTypeCost:
		if m, ok := evt.Data.(map[string]any); ok {
			log.Printf("[COST] %v tokens (model: %v)", m["total_tokens"], m["model"])
		}
	case EventTypePolicyCheck:
		if m, ok := evt.Data.(map[string]string); ok {
			log.Printf("[POLICY] %s: %s", m["tool"], m["result"])
		}
	case EventTypeHeartbeat:
		// silent on console — the live dashboard already shows health
	}
}

func (l *Logger) writeToFile(data []byte) {
	if err := os.MkdirAll(filepath.Dir(l.logPath), 0755); err != nil {
		log.Printf("failed to create log directory: %v", err)
		return
	}

	// Check size before writing
	info, err := os.Stat(l.logPath)
	if err == nil && info.Size() > l.maxSize {
		l.rotateLogs()
	}

	f, err := os.OpenFile(l.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
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
	oldPath := l.logPath + ".old"
	_ = os.Remove(oldPath)
	_ = os.Rename(l.logPath, oldPath)
}

// Helper methods for common events

func (l *Logger) LogReasoning(chatID, taskID string, agentID int, content string) {
	l.Log(Event{
		Type:   EventTypeReasoning,
		ChatID: chatID,
		TaskID: taskID,
		Data: map[string]any{
			"agent_id": agentID,
			"content":  content,
		},
	})
}

func (l *Logger) LogToolCall(chatID, taskID string, agentID int, tool, args string) {
	l.Log(Event{
		Type:   EventTypeToolCall,
		ChatID: chatID,
		TaskID: taskID,
		Data: map[string]any{
			"agent_id": agentID,
			"tool":     tool,
			"args":     args,
		},
	})
}

func (l *Logger) LogToolResult(chatID, taskID string, agentID int, tool string, result string) {
	l.Log(Event{
		Type:   EventTypeToolResult,
		ChatID: chatID,
		TaskID: taskID,
		Data: map[string]any{
			"agent_id": agentID,
			"tool":     tool,
			"result":   result,
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

func (l *Logger) LogAgentStart(chatID, taskID, parentChatID, parentTaskID string, parentAgentID int, agentID int, agentType string, goal string) {
	l.Log(Event{
		Type:          EventTypeAgentStart,
		ChatID:        chatID,
		TaskID:        taskID,
		ParentChatID:  parentChatID,
		ParentTaskID:  parentTaskID,
		ParentAgentID: parentAgentID,
		Data: map[string]any{
			"agent_id":   agentID,
			"agent_type": agentType,
			"goal":       goal,
		},
	})
}

func (l *Logger) LogAgentEnd(chatID, taskID string, agentID int, status string, report string) {
	l.Log(Event{
		Type:   EventTypeAgentEnd,
		ChatID: chatID,
		TaskID: taskID,
		Data: map[string]any{
			"agent_id": agentID,
			"status":   status,
			"report":   report,
		},
	})
}
