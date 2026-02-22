package agent

import (
	"context"
	"fmt"
	"log"
	"time"
)

type Messenger interface {
	Send(chatID string, text string) error
}

type Scheduler struct {
	Brain   Brain
	Store   *HistoryStoreWrapper
	Gateway Messenger
}

// HistoryStoreWrapper is needed because SimpleBrain uses the interface from brain.go
// but we need the specific methods we added to store.HistoryStore.
type TaskStore interface {
	GetPendingTasks() ([]map[string]any, error)
	UpdateTaskLastRun(id int) error
	ListTasks(chatID string) ([]map[string]any, error)
	DeleteTask(chatID string, taskID int) error
}

func NewScheduler(brain Brain, store TaskStore, gateway Messenger) *Scheduler {
	return &Scheduler{
		Brain:   brain,
		Store:   &HistoryStoreWrapper{store},
		Gateway: gateway,
	}
}

type HistoryStoreWrapper struct {
	TaskStore
}

func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Println("Task scheduler started...")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollAndExecute(ctx)
		}
	}
}

func (s *Scheduler) pollAndExecute(ctx context.Context) {
	tasks, err := s.Store.GetPendingTasks()
	if err != nil {
		log.Printf("Error polling tasks: %v", err)
		return
	}

	for _, t := range tasks {
		id := t["id"].(int)
		chatID := t["chat_id"].(string)
		desc := t["task_description"].(string)

		log.Printf("Executing scheduled task %d for chat %s: %s", id, chatID, desc)

		// Execute the task using the Brain
		response, err := s.Brain.Think(ctx, chatID, fmt.Sprintf("[SYSTEM: This is the execution of a previously scheduled task: \"%s\". Please provide the output/reminder for the user. DO NOT schedule it again.]", desc))
		if err != nil {
			log.Printf("Error executing scheduled task %d: %v", id, err)
			continue
		}

		// Update last run time
		if err := s.Store.UpdateTaskLastRun(id); err != nil {
			log.Printf("Error updating last run for task %d: %v", id, err)
		}

		// If it's a one-time task (interval = 0), delete it
		if t["interval_seconds"].(int) == 0 {
			if err := s.Store.DeleteTask(chatID, id); err != nil {
				log.Printf("Error deleting one-time task %d: %v", id, err)
			}
		}

		// Notify the user via the gateway
		if s.Gateway != nil {
			s.Gateway.Send(chatID, "⏰ *Scheduled Task Output*\n\n"+response)
		}
	}
}
