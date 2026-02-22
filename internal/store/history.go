package store

import (
	"database/sql"

	_ "github.com/glebarez/go-sqlite"
	"github.com/tmc/langchaingo/llms"
)

type HistoryStore struct {
	DB *sql.DB
}

func NewHistoryStore(dbPath string) (*HistoryStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Create tables if not exist
	queries := []string{
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id TEXT,
			role TEXT,
			content TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id TEXT,
			task_description TEXT,
			interval_seconds INTEGER,
			last_run DATETIME,
			status TEXT DEFAULT 'active'
		);`,
	}
	for _, q := range queries {
		_, err = db.Exec(q)
		if err != nil {
			return nil, err
		}
	}

	return &HistoryStore{DB: db}, nil
}

func (h *HistoryStore) AddMessage(chatID string, role string, content string) error {
	query := `INSERT INTO messages (chat_id, role, content) VALUES (?, ?, ?)`
	_, err := h.DB.Exec(query, chatID, role, content)
	return err
}

func (h *HistoryStore) AddTask(chatID string, description string, intervalSeconds int) error {
	query := `INSERT INTO tasks (chat_id, task_description, interval_seconds, last_run) VALUES (?, ?, ?, datetime('now', '-365 days'))`
	_, err := h.DB.Exec(query, chatID, description, intervalSeconds)
	return err
}

func (h *HistoryStore) GetPendingTasks() ([]map[string]any, error) {
	query := `
		SELECT id, chat_id, task_description, interval_seconds, last_run 
		FROM tasks 
		WHERE status = 'active' 
		AND (last_run IS NULL OR (julianday('now') - julianday(last_run)) * 86400 >= interval_seconds)`
	rows, err := h.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []map[string]any
	for rows.Next() {
		var id, interval int
		var chatID, desc, lastRun string
		if err := rows.Scan(&id, &chatID, &desc, &interval, &lastRun); err != nil {
			return nil, err
		}
		tasks = append(tasks, map[string]any{
			"id":               id,
			"chat_id":          chatID,
			"task_description": desc,
			"interval_seconds": interval,
		})
	}
	return tasks, nil
}

func (h *HistoryStore) UpdateTaskLastRun(id int) error {
	query := `UPDATE tasks SET last_run = datetime('now') WHERE id = ?`
	_, err := h.DB.Exec(query, id)
	return err
}

func (h *HistoryStore) ClearTasks(chatID string) error {
	query := `DELETE FROM tasks WHERE chat_id = ?`
	_, err := h.DB.Exec(query, chatID)
	return err
}

func (h *HistoryStore) GetHistory(chatID string, limit int) ([]llms.MessageContent, error) {
	query := `SELECT role, content FROM messages WHERE chat_id = ? ORDER BY timestamp DESC LIMIT ?`
	rows, err := h.DB.Query(query, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []llms.MessageContent
	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err != nil {
			return nil, err
		}

		// Convert role string to llms.ChatMessageType
		var msgRole llms.ChatMessageType
		switch role {
		case "human":
			msgRole = llms.ChatMessageTypeHuman
		case "ai":
			msgRole = llms.ChatMessageTypeAI
		case "system":
			msgRole = llms.ChatMessageTypeSystem
		default:
			msgRole = llms.ChatMessageTypeHuman
		}

		history = append(history, llms.MessageContent{
			Role: msgRole,
			Parts: []llms.ContentPart{
				llms.TextPart(content),
			},
		})
	}

	// Reverse to get chronological order
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}

	return history, nil
}
