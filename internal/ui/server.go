package ui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rahul/mishri/internal/observability"
	"github.com/rahul/mishri/pkg/config"
)

//go:embed templates/index.html templates/config.html templates/chat.html templates/flow.html assets
var uiFS embed.FS

// Brain is the minimal interface the UI server needs from MasterBrain.
type Brain interface {
	Think(ctx context.Context, chatID string, input string) (string, error)
}

// HistoryClearer is implemented by store.HistoryStore.
type HistoryClearer interface {
	ClearAllHistory() error
}

type Server struct {
	configPath string
	brain      Brain
	history    HistoryClearer
	logger     *observability.Logger
	mu         sync.Mutex // protect config file writes
}

func NewServer(configPath string, brain Brain, history HistoryClearer, logger *observability.Logger) *Server {
	return &Server{
		configPath: configPath,
		brain:      brain,
		history:    history,
		logger:     logger,
	}
}

func (s *Server) Start(port int) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/config", s.handleConfig)
	mux.HandleFunc("/chat", s.handleChat)
	mux.HandleFunc("/flow", s.handleFlow)
	mux.HandleFunc("/ws/chat", s.handleChatWS)
	mux.HandleFunc("/ws/events", s.handleEventsWS)
	mux.HandleFunc("/api/config", s.handleSaveConfig)
	mux.HandleFunc("/api/history", s.handleClearHistory)

	// Static Assets
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("internal/ui/assets"))))

	addr := fmt.Sprintf(":%d", port)
	log.Printf("🌐 Web UI → http://localhost%s  |  Chat → http://localhost%s/chat  |  Flow → http://localhost%s/flow", addr, addr, addr)
	return http.ListenAndServe(addr, mux)
}

// ─── Home UI ─────────────────────────────────────────────────────────────────

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	tmpl, err := template.ParseFS(uiFS, "templates/index.html")
	if err != nil {
		log.Printf("Home template error: %v", err)
		http.Error(w, "Failed to load home template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	tmpl.Execute(w, nil)
}

// ─── Config UI ───────────────────────────────────────────────────────────────

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/config" {
		http.NotFound(w, r)
		return
	}

	cfg := config.LoadConfig(s.configPath)

	funcMap := template.FuncMap{
		"json": func(v any) (template.JS, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return "", err
			}
			return template.JS(b), nil
		},
	}

	tmpl, err := template.New("config.html").Funcs(funcMap).ParseFS(uiFS, "templates/config.html")
	if err != nil {
		log.Printf("Template Parse Error: %v", err)
		http.Error(w, "Failed to load template", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	if err := tmpl.Execute(w, cfg); err != nil {
		log.Printf("Template Execute Error: %v", err)
	}
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var newCfg config.Config
	if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := config.SaveConfig(&newCfg, s.configPath); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed — use DELETE", http.StatusMethodNotAllowed)
		return
	}

	if s.history == nil {
		http.Error(w, "History store not connected", http.StatusServiceUnavailable)
		return
	}

	if err := s.history.ClearAllHistory(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to clear history: %v", err), http.StatusInternalServerError)
		return
	}

	log.Println("[UI] All agent history cleared via web dashboard.")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})
}

// ─── Chat UI ─────────────────────────────────────────────────────────────────

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(uiFS, "templates/chat.html")
	if err != nil {
		log.Printf("Chat template error: %v", err)
		http.Error(w, "Failed to load chat template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	tmpl.Execute(w, nil)
}

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true }, // allow localhost
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// wsMsg is the JSON envelope sent over the WebSocket in both directions.
type wsMsg struct {
	Type    string `json:"type"`    // "user" | "assistant" | "error" | "thinking"
	Content string `json:"content"` // message text
}

func (s *Server) handleChatWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Each WebSocket connection gets its own synthetic chatID so it has its
	// own memory / scratchpad inside MasterBrain.
	chatID := fmt.Sprintf("webchat_%d", time.Now().UnixNano())
	log.Printf("[WebChat] New session: %s", chatID)

	var wsMu sync.Mutex
	send := func(msg wsMsg) {
		wsMu.Lock()
		defer wsMu.Unlock()
		conn.WriteJSON(msg)
	}

	for {
		var incoming wsMsg
		if err := conn.ReadJSON(&incoming); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[WebChat] Read error: %v", err)
			}
			break
		}

		if s.brain == nil {
			send(wsMsg{Type: "error", Content: "Agent brain is not connected."})
			continue
		}

		// Tell the frontend the agent is working
		send(wsMsg{Type: "thinking", Content: "🧠 Thinking..."})

		// Run MasterBrain in a goroutine so we don't block the read loop
		go func(userInput string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			response, err := s.brain.Think(ctx, chatID, userInput)
			if err != nil {
				send(wsMsg{Type: "error", Content: fmt.Sprintf("Error: %v", err)})
				return
			}
			send(wsMsg{Type: "assistant", Content: response})
		}(incoming.Content)
	}

	log.Printf("[WebChat] Session closed: %s", chatID)
}
func (s *Server) handleFlow(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(uiFS, "templates/flow.html")
	if err != nil {
		log.Printf("Flow template error: %v", err)
		http.Error(w, "Failed to load flow template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	tmpl.Execute(w, nil)
}

func (s *Server) handleEventsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[UI] Events WS upgrade error: %v", err)
		return
	}
	defer conn.Close()

	if s.logger == nil {
		log.Println("[UI] Events WS requested but logger is nil")
		return
	}

	// Subscribe to events from the backend logger
	ch := s.logger.Subscribe()
	defer s.logger.Unsubscribe(ch)

	log.Printf("[UI] New EventStream connection from %s", r.RemoteAddr)

	// Keep-alive or close check
	done := make(chan bool)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				done <- true
				return
			}
		}
	}()

	for {
		select {
		case evt := <-ch:
			if err := conn.WriteJSON(evt); err != nil {
				log.Printf("[UI] EventStream write error: %v", err)
				return
			}
		case <-done:
			log.Printf("[UI] EventStream connection closed by %s", r.RemoteAddr)
			return
		case <-time.After(30 * time.Second):
			// Ping to keep connection alive
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
