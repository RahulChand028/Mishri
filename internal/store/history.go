package store

import (
	"database/sql"
	"strings"

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
		`CREATE TABLE IF NOT EXISTS plans (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id TEXT,
			input TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS steps (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			plan_id INTEGER,
			step_id_in_plan INTEGER,
			description TEXT,
			status TEXT,
			result TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(plan_id) REFERENCES plans(id)
		);`,
		`CREATE TABLE IF NOT EXISTS agents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			plan_id INTEGER,
			agent_id_in_plan INTEGER,
			type TEXT,
			goal TEXT,
			system_prompt TEXT,
			tools TEXT,
			status TEXT,
			report TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(plan_id) REFERENCES plans(id)
		);`,
		`CREATE TABLE IF NOT EXISTS costs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id TEXT,
			model TEXT,
			prompt_tokens INTEGER,
			completion_tokens INTEGER,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_messages_chat_id ON messages(chat_id);`,
		`CREATE INDEX IF NOT EXISTS idx_steps_plan_id ON steps(plan_id);`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_chat_id ON tasks(chat_id);`,
		`CREATE INDEX IF NOT EXISTS idx_plans_chat_id ON plans(chat_id);`,
		`CREATE TABLE IF NOT EXISTS escalations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_chat_id TEXT,
			sub_chat_id TEXT,
			plan_id INTEGER,
			goal TEXT,
			completed_agents TEXT,
			pending_agents TEXT,
			question TEXT,
			options TEXT,
			parent_agent_id INTEGER DEFAULT 0,
			parent_task_id TEXT,
			status TEXT DEFAULT 'pending',
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_escalations_parent_chat ON escalations(parent_chat_id, status);`,
	}
	for _, q := range queries {
		_, err = db.Exec(q)
		if err != nil {
			return nil, err
		}
	}

	// Migration: ensure parent_agent_id exists in escalations table
	// SQLite's ALTER TABLE ADD COLUMN doesn't support IF NOT EXISTS.
	var hasParentAgentID bool
	var hasParentTaskID bool
	if rows, err := db.Query("PRAGMA table_info(escalations)"); err == nil {
		defer rows.Close()
		// hasParentAgentID := false // This line was creating a new local variable, shadowing the outer one.
		for rows.Next() {
			var cid int
			var name, dtype string
			var notnull int
			var dflt_value any
			var pk int
			if err := rows.Scan(&cid, &name, &dtype, &notnull, &dflt_value, &pk); err == nil {
				if name == "parent_agent_id" {
					hasParentAgentID = true
				}
				if name == "parent_task_id" {
					hasParentTaskID = true
				}
			}
		}
		if !hasParentAgentID {
			_, _ = db.Exec("ALTER TABLE escalations ADD COLUMN parent_agent_id INTEGER DEFAULT 0")
		}
		if !hasParentTaskID {
			_, _ = db.Exec("ALTER TABLE escalations ADD COLUMN parent_task_id TEXT")
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

func (h *HistoryStore) ListTasks(chatID string) ([]map[string]any, error) {
	query := `SELECT id, task_description, interval_seconds, last_run, status FROM tasks WHERE chat_id = ?`
	rows, err := h.DB.Query(query, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []map[string]any
	for rows.Next() {
		var id, interval int
		var desc, lastRun, status string
		if err := rows.Scan(&id, &desc, &interval, &lastRun, &status); err != nil {
			return nil, err
		}
		tasks = append(tasks, map[string]any{
			"id":               id,
			"task_description": desc,
			"interval_seconds": interval,
			"last_run":         lastRun,
			"status":           status,
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

// ClearAllHistory wipes all conversation data while preserving the DB schema.
// Equivalent to: DELETE FROM messages; DELETE FROM plans; ... etc.
func (h *HistoryStore) ClearAllHistory() error {
	tables := []string{"messages", "plans", "agents", "steps", "costs", "escalations"}
	tx, err := h.DB.Begin()
	if err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (h *HistoryStore) DeleteTask(chatID string, taskID int) error {
	query := `DELETE FROM tasks WHERE chat_id = ? AND id = ?`
	_, err := h.DB.Exec(query, chatID, taskID)
	return err
}

func (h *HistoryStore) GetHistory(chatID string, limit int) ([]llms.MessageContent, error) {
	query := `SELECT role, content FROM messages WHERE chat_id = ? ORDER BY timestamp DESC, id DESC LIMIT ?`
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

func (h *HistoryStore) SavePlan(chatID string, input string) (int64, error) {
	res, err := h.DB.Exec(`INSERT INTO plans (chat_id, input) VALUES (?, ?)`, chatID, input)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (h *HistoryStore) SyncPlanSteps(planID int64, steps []Step) error {
	// Simple strategy: delete and recreate steps for a plan to sync state
	_, err := h.DB.Exec(`DELETE FROM steps WHERE plan_id = ?`, planID)
	if err != nil {
		return err
	}

	for _, step := range steps {
		_, err = h.DB.Exec(`INSERT INTO steps (plan_id, step_id_in_plan, description, status, result) VALUES (?, ?, ?, ?, ?)`,
			planID, step.ID, step.Description, step.Status, step.Result)
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *HistoryStore) RecordCost(chatID string, model string, promptTokens, completionTokens int) error {
	_, err := h.DB.Exec(`INSERT INTO costs (chat_id, model, prompt_tokens, completion_tokens) VALUES (?, ?, ?, ?)`,
		chatID, model, promptTokens, completionTokens)
	return err
}

// SyncPlanAgents persists the current state of all agents in an AgentPlan.
// It replaces all agents for the given plan with the current state (delete+insert).
func (h *HistoryStore) SyncPlanAgents(planID int64, agents []Agent) error {
	_, err := h.DB.Exec(`DELETE FROM agents WHERE plan_id = ?`, planID)
	if err != nil {
		return err
	}

	for _, a := range agents {
		toolsJSON := strings.Join(a.Tools, ",")
		_, err = h.DB.Exec(
			`INSERT INTO agents (plan_id, agent_id_in_plan, type, goal, system_prompt, tools, status, report) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			planID, a.ID, string(a.Type), a.Goal, a.SystemPrompt, toolsJSON, a.Status, a.Report,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// SaveEscalation persists the Sub-Manager's state when it pauses for user input.
func (h *HistoryStore) SaveEscalation(esc *EscalationState) (int64, error) {
	query := `INSERT INTO escalations (parent_chat_id, sub_chat_id, plan_id, goal, completed_agents, prior_reports, question, options, parent_agent_id, parent_task_id, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')`
	res, err := h.DB.Exec(query, esc.ParentChatID, esc.SubChatID, esc.PlanID, esc.Goal, esc.CompletedAgents, esc.PendingAgents, esc.Question, esc.Options, esc.ParentAgentID, esc.ParentTaskID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LoadEscalation retrieves a pending escalation by ID.
func (h *HistoryStore) LoadEscalation(id int64) (*EscalationState, error) {
	query := `SELECT id, parent_chat_id, sub_chat_id, plan_id, goal, completed_agents, prior_reports, question, options, parent_agent_id, parent_task_id, status FROM escalations WHERE id = ?`
	row := h.DB.QueryRow(query, id)
	var esc EscalationState
	err := row.Scan(&esc.ID, &esc.ParentChatID, &esc.SubChatID, &esc.PlanID, &esc.Goal,
		&esc.CompletedAgents, &esc.PendingAgents, &esc.Question, &esc.Options, &esc.ParentAgentID, &esc.ParentTaskID, &esc.Status)
	if err != nil {
		return nil, err
	}
	return &esc, nil
}

// GetPendingEscalation retrieves the most recent pending escalation for a chat.
func (h *HistoryStore) GetPendingEscalation(parentChatID string) (*EscalationState, error) {
	query := `SELECT id, parent_chat_id, sub_chat_id, plan_id, goal, completed_agents, prior_reports, question, options, parent_agent_id, parent_task_id, status FROM escalations WHERE parent_chat_id = ? AND status = 'pending' ORDER BY id DESC LIMIT 1`
	row := h.DB.QueryRow(query, parentChatID)
	var esc EscalationState
	err := row.Scan(&esc.ID, &esc.ParentChatID, &esc.SubChatID, &esc.PlanID, &esc.Goal,
		&esc.CompletedAgents, &esc.PendingAgents, &esc.Question, &esc.Options, &esc.ParentAgentID, &esc.ParentTaskID, &esc.Status)
	if err != nil {
		return nil, err
	}
	return &esc, nil
}

// ResolveEscalation marks an escalation as answered.
func (h *HistoryStore) ResolveEscalation(id int64) error {
	_, err := h.DB.Exec(`UPDATE escalations SET status = 'answered' WHERE id = ?`, id)
	return err
}
