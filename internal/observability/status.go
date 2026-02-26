package observability

import (
	"sync"
	"time"
)

type Role string

const (
	RoleIdle   Role = "IDLE"
	RoleMaster Role = "MASTER"
	RoleSlave  Role = "SLAVE"
)

type SystemStatus struct {
	mu            sync.RWMutex
	CurrentRole   Role
	ActiveTask    string
	LastHeartbeat time.Time
}

var globalStatus = &SystemStatus{
	CurrentRole:   RoleIdle,
	LastHeartbeat: time.Now(),
}

// SetStatus updates the global system status.
func SetStatus(role Role, task string) {
	globalStatus.mu.Lock()
	defer globalStatus.mu.Unlock()
	globalStatus.CurrentRole = role
	globalStatus.ActiveTask = task
}

// GetStatus retrieves a copy of the global system status.
func GetStatus() (Role, string, time.Time) {
	globalStatus.mu.RLock()
	defer globalStatus.mu.RUnlock()
	return globalStatus.CurrentRole, globalStatus.ActiveTask, globalStatus.LastHeartbeat
}

// Heartbeat updates the last heartbeat time.
func Heartbeat() {
	globalStatus.mu.Lock()
	defer globalStatus.mu.Unlock()
	globalStatus.LastHeartbeat = time.Now()
}
