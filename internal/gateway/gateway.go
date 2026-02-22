package gateway

// Messenger defines the interface for communication gateways (Telegram, Discord, etc.)
type Messenger interface {
	// Start begins the message listening loop
	Start() error
	// Send sends a message to a specific chat
	Send(chatID string, text string) error
	// Stop gracefully shuts down the gateway
	Stop() error
}
