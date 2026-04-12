package telegram

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const editIntervalMs = 1500

// Bot is the main long-running entity.  It owns one Telegram client, the
// optional auth store, and a per-chat session map (chat_id -> session_id).
type Bot struct {
	api        *tgbotapi.BotAPI
	gatewayURL string
	auth       *AuthStore
	agent      *AgentClient

	mu       sync.Mutex
	sessions map[int64]int // chat_id -> agent session_id
}

func New(token, gatewayURL string, auth *AuthStore) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}
	return &Bot{
		api:        api,
		gatewayURL: strings.TrimRight(gatewayURL, "/"),
		auth:       auth,
		agent:      NewAgentClient(gatewayURL),
		sessions:   map[int64]int{},
	}, nil
}

// Run polls Telegram for updates and dispatches them to handlers.  Blocks
// until the parent process closes.
func (b *Bot) Run() {
	log.Printf("telegram bot starting (gateway=%s, account=@%s)", b.gatewayURL, b.api.Self.UserName)
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	for update := range b.api.GetUpdatesChan(u) {
		if update.Message == nil {
			continue
		}
		go b.handle(update.Message)
	}
}

func (b *Bot) handle(msg *tgbotapi.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("handler panic: %v", r)
		}
	}()

	if msg.IsCommand() {
		switch msg.Command() {
		case "start":
			b.handleStart(msg)
		case "ask":
			b.handleAsk(msg)
		case "new":
			b.handleNew(msg)
		case "skills":
			b.handleSkills(msg)
		case "auth_add":
			b.handleAuthAdd(msg)
		case "auth_list":
			b.handleAuthList(msg)
		}
		return
	}

	b.handleText(msg)
}

func (b *Bot) getSession(chatID int64) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[chatID]
}

func (b *Bot) setSession(chatID int64, sid int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sid == 0 {
		delete(b.sessions, chatID)
	} else {
		b.sessions[chatID] = sid
	}
}

func (b *Bot) ensureAuthorized(msg *tgbotapi.Message) bool {
	if !b.auth.IsAuthorized(msg.Chat.ID) {
		b.send(msg.Chat.ID, "Access denied. Ask the bot admin to allow your Telegram ID.")
		return false
	}
	b.auth.Touch(msg.Chat.ID)
	return true
}

func (b *Bot) handleStart(msg *tgbotapi.Message) {
	b.setSession(msg.Chat.ID, 0)
	extra := ""
	if b.auth.IsEnabled() {
		extra = fmt.Sprintf("\n\nYour Telegram ID: `%d`", msg.Chat.ID)
	}
	b.send(msg.Chat.ID,
		"Hello! I'm the OmegaGrid Agent bot.\n\n"+
			"Just send me any message and I'll process it through the agent.\n\n"+
			"Commands:\n"+
			"/start - Reset session & show this help\n"+
			"/ask <question> - Ask the agent explicitly\n"+
			"/new - Start a new session\n"+
			"/skills - List available skills\n"+
			"/auth_add <telegram_id> - Admin only, allow a user\n"+
			"/auth_list - Admin only, list allowed users"+extra)
}

func (b *Bot) handleAsk(msg *tgbotapi.Message) {
	if !b.ensureAuthorized(msg) {
		return
	}
	text := strings.TrimSpace(msg.CommandArguments())
	if text == "" {
		b.send(msg.Chat.ID, "Usage: /ask <your question>")
		return
	}
	b.processAgentText(msg.Chat.ID, text, true)
}

func (b *Bot) handleNew(msg *tgbotapi.Message) {
	if !b.ensureAuthorized(msg) {
		return
	}
	b.setSession(msg.Chat.ID, 0)
	b.send(msg.Chat.ID, "Session reset. Next message starts a fresh conversation.")
}

func (b *Bot) handleSkills(msg *tgbotapi.Message) {
	if !b.ensureAuthorized(msg) {
		return
	}
	resp, err := http.Get(b.gatewayURL + "/api/skills")
	if err != nil {
		b.send(msg.Chat.ID, "Error listing skills: "+err.Error())
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		b.send(msg.Chat.ID, "Error parsing skills: "+err.Error())
		return
	}
	if len(out.Skills) == 0 {
		b.send(msg.Chat.ID, "No skills loaded.")
		return
	}
	var sb strings.Builder
	sb.WriteString("Available skills:\n")
	for _, s := range out.Skills {
		fmt.Fprintf(&sb, "- %s: %s\n", s.Name, s.Description)
	}
	b.send(msg.Chat.ID, sb.String())
}

func (b *Bot) handleAuthAdd(msg *tgbotapi.Message) {
	if !b.auth.IsEnabled() {
		b.send(msg.Chat.ID, "Auth is disabled.")
		return
	}
	if !b.auth.IsAdmin(msg.Chat.ID) {
		b.send(msg.Chat.ID, "Only the admin can add users.")
		return
	}
	args := strings.TrimSpace(msg.CommandArguments())
	if args == "" {
		b.send(msg.Chat.ID, "Usage: /auth_add <telegram_id>")
		return
	}
	id, err := strconv.ParseInt(args, 10, 64)
	if err != nil {
		b.send(msg.Chat.ID, "telegram_id must be an integer.")
		return
	}
	if err := b.auth.AddUser(id); err != nil {
		b.send(msg.Chat.ID, "Failed to add user: "+err.Error())
		return
	}
	b.send(msg.Chat.ID, fmt.Sprintf("Authorized Telegram ID: %d", id))
}

func (b *Bot) handleAuthList(msg *tgbotapi.Message) {
	if !b.auth.IsEnabled() {
		b.send(msg.Chat.ID, "Auth is disabled.")
		return
	}
	if !b.auth.IsAdmin(msg.Chat.ID) {
		b.send(msg.Chat.ID, "Only the admin can list users.")
		return
	}
	users, err := b.auth.ListUsers(100)
	if err != nil {
		b.send(msg.Chat.ID, "Error: "+err.Error())
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Admin: %d\n", b.auth.AdminID())
	if len(users) == 0 {
		sb.WriteString("No authorized users yet.")
	} else {
		sb.WriteString("Authorized users:\n")
		for _, u := range users {
			fmt.Fprintf(&sb, "- %d | created=%s | last=%s\n", u.TelegramID, u.CreatedAt, u.LastActivity)
		}
	}
	b.send(msg.Chat.ID, sb.String())
}

func (b *Bot) handleText(msg *tgbotapi.Message) {
	if !b.ensureAuthorized(msg) {
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	b.processAgentText(msg.Chat.ID, text, false)
}

// processAgentText runs an agent query with progressive status updates.  When
// askMode is true, the final message is suffixed with the model + step count
// (matching /ask in the Python bot).
func (b *Bot) processAgentText(chatID int64, text string, askMode bool) {
	statusMsg := tgbotapi.NewMessage(chatID, "⚙️ Processing...")
	sent, err := b.api.Send(statusMsg)
	if err != nil {
		log.Printf("send status: %v", err)
		return
	}

	events := make(chan Event, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- b.agent.QueryStream(text, chatID, b.getSession(chatID), events)
	}()

	var (
		acc        []Event
		lastEditMs int64
		final      *Event
	)
	for ev := range events {
		evCopy := ev
		switch ev.Event {
		case "final":
			final = &evCopy
		case "error":
			final = &evCopy
		default:
			acc = append(acc, ev)
			now := time.Now().UnixMilli()
			if now-lastEditMs >= editIntervalMs {
				b.editStatus(chatID, sent.MessageID, renderStatus(acc))
				lastEditMs = now
			}
		}
	}
	streamErr := <-errCh

	// Streaming fallback: if streaming broke and we have no final, fall back
	// to the synchronous endpoint so the user always gets an answer.
	if final == nil {
		if streamErr != nil {
			log.Printf("stream failed, falling back: %v", streamErr)
		}
		resp, err := b.agent.Query(text, chatID, b.getSession(chatID))
		if err != nil {
			b.editStatus(chatID, sent.MessageID, "Error: "+err.Error())
			return
		}
		final = &Event{
			Event:     "final",
			SessionID: resp.SessionID,
			Answer:    resp.Answer,
			Meta:      resp.Meta,
		}
	}

	if final.Event == "error" {
		b.editStatus(chatID, sent.MessageID, "Error: "+final.Error)
		return
	}

	b.setSession(chatID, final.SessionID)
	out := final.Answer
	if askMode {
		model, _ := final.Meta["model"].(string)
		var steps any = "?"
		if v, ok := final.Meta["step_count"]; ok {
			steps = v
		}
		out = fmt.Sprintf("%s\n\n_model: %s | steps: %v_", out, model, steps)
	}
	b.editStatus(chatID, sent.MessageID, out)
}

// renderStatus produces the multi-line "Thinking / Calling X / X done" status
// shown while the agent is running.  Mirrors the Python implementation.
func renderStatus(events []Event) string {
	var lines []string
	for _, ev := range events {
		switch ev.Event {
		case "thinking":
			lines = append(lines, fmt.Sprintf("⚙️ Thinking (step %d)...", ev.Step))
		case "tool_call":
			brief := ""
			if ev.Why != "" {
				brief = " — " + ev.Why
			}
			lines = append(lines, fmt.Sprintf("🛠️ Calling %s%s", ev.Tool, brief))
		case "tool_result":
			lines = append(lines, fmt.Sprintf("✅ %s done (%.1fs)", ev.Tool, ev.ElapsedS))
		}
	}
	if len(lines) == 0 {
		return "Processing..."
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) send(chatID int64, text string) {
	if _, err := b.api.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		log.Printf("send: %v", err)
	}
}

func (b *Bot) editStatus(chatID int64, msgID int, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	if _, err := b.api.Send(edit); err != nil {
		// Telegram returns "message is not modified" as a 400 — ignore.
		if !strings.Contains(strings.ToLower(err.Error()), "not modified") {
			log.Printf("edit: %v", err)
		}
	}
}
