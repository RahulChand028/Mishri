package gateway

import (
	"context"
	"fmt"
	"log"

	"github.com/bwmarrin/discordgo"
	"github.com/rahul/mishri/internal/agent"
)

// DiscordGateway implements the Messenger interface for Discord
type DiscordGateway struct {
	Session *discordgo.Session
	Brain   agent.Brain
	Output  chan Message
}

// NewDiscordGateway creates a new connected Discord gateway
func NewDiscordGateway(token string, brain agent.Brain) (*DiscordGateway, error) {
	// Create a new Discord session using the provided bot token.
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("error creating Discord session: %w", err)
	}

	return &DiscordGateway{
		Session: dg,
		Brain:   brain,
		Output:  make(chan Message),
	}, nil
}

// Start opens the websocket and begins listening for messages
func (dg *DiscordGateway) Start() error {
	// Register the messageCreate func as a callback for MessageCreate events.
	dg.Session.AddHandler(dg.messageCreate)

	// In this example, we only care about receiving message events.
	dg.Session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	// Open a websocket connection to Discord and begin listening.
	err := dg.Session.Open()
	if err != nil {
		return fmt.Errorf("error opening connection: %w", err)
	}

	log.Println("Discord Gateway is now running.")
	return nil
}

// Send implements the Messenger interface for sending a reply
func (dg *DiscordGateway) Send(chatID string, text string) error {
	// Split long messages if necessary, discord limit is 2000 chars
	// For now, we'll just send directly and trust the bot/llm pagination or limit.
	if len(text) > 2000 {
		text = text[:1996] + "..."
	}

	_, err := dg.Session.ChannelMessageSend(chatID, text)
	return err
}

// Stop gracefully stops the discord session
func (dg *DiscordGateway) Stop() error {
	return dg.Session.Close()
}

// messageCreate is the callback for new messages in Discord
func (dg *DiscordGateway) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}

	// Only respond to DMs or messages where the bot is mentioned
	isMentioned := false
	for _, user := range m.Mentions {
		if user.ID == s.State.User.ID {
			isMentioned = true
			break
		}
	}

	isDM := m.GuildID == ""

	if !isMentioned && !isDM {
		return
	}

	log.Printf("[Discord %s] %s", m.Author.Username, m.Content)

	// Show a typing indicator while thinking
	s.ChannelTyping(m.ChannelID)

	ctx := context.Background()

	// Strip the bot mention from the text if it was a mention
	text := m.Content

	response, err := dg.Brain.Think(ctx, m.ChannelID, text)
	if err != nil {
		log.Printf("Error thinking (Discord): %v", err)
		response = "I'm having trouble thinking right now..."
	}

	dg.Send(m.ChannelID, response)
}
