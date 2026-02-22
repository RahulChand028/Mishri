package gateway

import (
	"context"
	"fmt"
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/rahul/mishri/internal/agent"
)

type TelegramGateway struct {
	Bot    *tgbotapi.BotAPI
	Brain  agent.Brain
	Output chan Message
}

type Message struct {
	ChatID int64
	Text   string
}

func NewTelegramGateway(token string, brain agent.Brain) (*TelegramGateway, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}

	log.Printf("Authorized on account %s", bot.Self.UserName)

	return &TelegramGateway{
		Bot:    bot,
		Brain:  brain,
		Output: make(chan Message),
	}, nil
}

func (tg *TelegramGateway) Start() error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := tg.Bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)

		// Ask the brain what to do
		ctx := context.Background()
		chatID := fmt.Sprintf("%d", update.Message.Chat.ID)
		response, err := tg.Brain.Think(ctx, chatID, update.Message.Text)
		if err != nil {
			log.Printf("Error thinking: %v", err)
			response = "I'm having trouble thinking right now..."
		}

		msg := tgbotapi.NewMessage(update.Message.Chat.ID, response)
		tg.Bot.Send(msg)
	}
	return nil
}

func (tg *TelegramGateway) Send(chatID string, text string) error {
	id := 0
	fmt.Sscanf(chatID, "%d", &id)
	if id == 0 {
		return fmt.Errorf("invalid chat ID: %s", chatID)
	}

	msg := tgbotapi.NewMessage(int64(id), text)
	msg.ParseMode = "Markdown" // Enable markdown for better alerts
	_, err := tg.Bot.Send(msg)
	return err
}

func (tg *TelegramGateway) Stop() error {
	tg.Bot.StopReceivingUpdates()
	return nil
}
