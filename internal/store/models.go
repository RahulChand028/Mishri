package store

// Step represents a single sub-task in a broader plan.
type Step struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
	Status      string `json:"status"` // pending, in_progress, completed, failed
	Result      string `json:"result"`
}

// Plan represents a sequence of steps to fulfill a user request.
type Plan struct {
	Steps []Step `json:"steps"`
}
