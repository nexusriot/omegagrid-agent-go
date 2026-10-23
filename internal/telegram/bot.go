package telegram

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const editIntervalMs = 1500

// maxConcurrentHandlers caps how many updates are processed at once across all
// chats. Each one can hold an agent run open for minutes, and spawning one per
// update without a bound let a burst of messages fan out into as many parallel
// agent runs against the gateway.
const maxConcurrentHandlers = 8

// Bot is the main long-running entity.  It owns one Telegram client, the
// optional auth store, and a per-chat session map (chat_id -> session_id).
type Bot struct {
	api        *tgbotapi.BotAPI
	gatewayURL string
	auth       *AuthStore
	agent      *AgentClient

	mu       sync.Mutex
	sessions map[int64]int // chat_id -> agent session_id

	// sem bounds concurrent handlers; chatLocks serialises updates within one
	// chat so two quick messages don't race on the same session (both reading
	// session 0 and each creating a new one, or answers arriving out of order).
	sem       chan struct{}
	locksMu   sync.Mutex
	chatLocks map[int64]*chatLock
}

// chatLock is a per-chat mutex, reference-counted so idle chats don't
// accumulate entries forever.
type chatLock struct {
	mu   sync.Mutex
	refs int
}

// New connects to Telegram. apiToken is the gateway's GATEWAY_API_TOKEN (may
// be empty when the gateway runs without auth).
func New(token, gatewayURL, apiToken string, auth *AuthStore) (*Bot, error) {
	// tgbotapi logs request errors (*url.Error, whose URL embeds the bot
	// token) through its own logger; route those through the redactor.
	_ = tgbotapi.SetLogger(log.New(RedactingWriter(log.Writer(), token), "", log.LstdFlags))
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, errors.New(redact(err.Error(), token))
	}
	return &Bot{
		api:        api,
		gatewayURL: strings.TrimRight(gatewayURL, "/"),
		auth:       auth,
		agent:      NewAgentClient(gatewayURL, apiToken),
		sessions:   map[int64]int{},
		sem:        make(chan struct{}, maxConcurrentHandlers),
		chatLocks:  map[int64]*chatLock{},
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
		go b.dispatch(update.Message)
	}
}

// dispatch runs one update under its chat's lock and a global concurrency
// slot. The chat lock is taken first so messages queued behind a busy chat
// don't sit on slots other chats could use.
func (b *Bot) dispatch(msg *tgbotapi.Message) {
	var chatID int64
	if msg.Chat != nil {
		chatID = msg.Chat.ID
	}
	unlock := b.lockChat(chatID)
	defer unlock()
	b.sem <- struct{}{}
	defer func() { <-b.sem }()
	b.handle(msg)
}

func (b *Bot) lockChat(chatID int64) func() {
	b.locksMu.Lock()
	l := b.chatLocks[chatID]
	if l == nil {
		l = &chatLock{}
		b.chatLocks[chatID] = l
	}
	l.refs++
	b.locksMu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		b.locksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(b.chatLocks, chatID)
		}
		b.locksMu.Unlock()
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

// senderID returns the Telegram user who sent msg. Authorization is keyed on
// the sender, not the chat: in a group the chat ID is shared by every member,
// so allowlisting (or being admin of) a group let anyone in it drive the
// agent. Messages with no sender (channel posts) report ok=false.
func senderID(msg *tgbotapi.Message) (int64, bool) {
	if msg.From == nil {
		return 0, false
	}
	return msg.From.ID, true
}

func (b *Bot) ensureAuthorized(msg *tgbotapi.Message) bool {
	id, ok := senderID(msg)
	if (b.auth.IsEnabled() && !ok) || !b.auth.IsAuthorized(id) {
		b.send(msg.Chat.ID, "Access denied. Ask the bot admin to allow your Telegram ID.")
		return false
	}
	b.auth.Touch(id)
	return true
}

// isAdmin reports whether msg was sent by the configured admin user.
func (b *Bot) isAdmin(msg *tgbotapi.Message) bool {
	id, ok := senderID(msg)
	return ok && b.auth.IsAdmin(id)
}

func (b *Bot) handleStart(msg *tgbotapi.Message) {
	b.setSession(msg.Chat.ID, 0)
	extra := ""
	if id, ok := senderID(msg); ok && b.auth.IsEnabled() {
		extra = fmt.Sprintf("\n\nYour Telegram ID: `%d`", id)
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
	raw, err := b.agent.ListSkillsRaw()
	if err != nil {
		b.send(msg.Chat.ID, "Error listing skills: "+err.Error())
		return
	}
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
	if !b.isAdmin(msg) {
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
	if !b.isAdmin(msg) {
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
		received   int
	)
	for ev := range events {
		received++
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

	// A stream that delivered events and then broke means the gateway was
	// already running the agent: re-sending the query to /api/query would run
	// it a second time (duplicate tool calls, side effects, double cost), so
	// report the interruption instead.
	if final == nil && received > 0 {
		log.Printf("stream interrupted after %d event(s): %v", received, streamErr)
		reason := "connection closed"
		if streamErr != nil {
			reason = streamErr.Error()
		}
		b.editStatus(chatID, sent.MessageID, "⚠️ Stream interrupted before the answer arrived ("+reason+"). Please try again.")
		return
	}

	// Streaming fallback: if the stream never got going (no event at all —
	// e.g. a proxy rejecting SSE), fall back to the synchronous endpoint so the
	// user still gets an answer.
	if final == nil {
		if streamErr != nil {
			log.Printf("stream failed, falling back: %v", streamErr)
		}
		// The sync fallback runs the whole agent and can take minutes;
		// without this edit the message keeps showing a stale "Thinking
		// (step N)..." and looks frozen.
		b.editStatus(chatID, sent.MessageID, "⚠️ Stream interrupted — retrying...")
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

	// Send binary attachments (images, etc.) as separate Telegram messages
	// before the text answer so the user sees the media first.
	b.sendAttachments(chatID, final.Attachments)

	out := final.Answer
	if askMode {
		model, _ := final.Meta["model"].(string)
		var steps any = "?"
		if v, ok := final.Meta["step_count"]; ok {
			steps = v
		}
		out = fmt.Sprintf("%s\n\n_model: %s | steps: %v_", out, model, steps)
	}
	// Telegram rejects messages over telegramMaxLen with a 400, which
	// editStatus silently swallows — leaving the user staring at a stale
	// "Processing..." status. Split the answer so every part fits: the first
	// chunk replaces the status message, the rest are sent as follow-ups.
	chunks := splitForTelegram(out)
	if len(chunks) == 0 {
		chunks = []string{"(empty answer)"}
	}
	b.editStatus(chatID, sent.MessageID, chunks[0])
	for _, c := range chunks[1:] {
		b.send(chatID, c)
	}
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
	// On long multi-step runs the accumulated status can exceed Telegram's
	// message limit, after which every edit fails. Keep only the most recent
	// lines so the status stays well under the cap.
	const maxStatusLines = 25
	if len(lines) > maxStatusLines {
		lines = append([]string{"…"}, lines[len(lines)-maxStatusLines:]...)
	}
	return strings.Join(lines, "\n")
}

// telegramMaxLen is Telegram's per-message hard limit. We chunk on a slightly
// smaller bound to stay clear of the UTF-16 code-unit counting Telegram uses.
const telegramMaxLen = 4000

// splitForTelegram breaks text into pieces that each fit within telegramMaxLen,
// preferring to split on line boundaries and never splitting a multi-byte rune.
// A single line longer than the limit is hard-split by runes.
func splitForTelegram(text string) []string {
	if text == "" {
		return nil
	}
	if len([]rune(text)) <= telegramMaxLen {
		return []string{text}
	}

	var chunks []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if curLen > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
			curLen = 0
		}
	}

	for _, line := range strings.Split(text, "\n") {
		lineLen := len([]rune(line))
		// A line that is itself too long must be hard-split by runes.
		if lineLen > telegramMaxLen {
			flush()
			runes := []rune(line)
			for len(runes) > 0 {
				n := telegramMaxLen
				if n > len(runes) {
					n = len(runes)
				}
				chunks = append(chunks, string(runes[:n]))
				runes = runes[n:]
			}
			continue
		}
		// +1 accounts for the newline that rejoins this line to the previous.
		if curLen > 0 && curLen+1+lineLen > telegramMaxLen {
			flush()
		}
		if curLen > 0 {
			cur.WriteByte('\n')
			curLen++
		}
		cur.WriteString(line)
		curLen += lineLen
	}
	flush()
	return chunks
}

func (b *Bot) send(chatID int64, text string) {
	if _, err := b.api.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		log.Printf("send: %v", err)
	}
}

// sendAttachments decodes base64 binary artifacts and sends them as Telegram
// photos (or documents for non-image types).
func (b *Bot) sendAttachments(chatID int64, attachments []Attachment) {
	for _, att := range attachments {
		raw, err := base64.StdEncoding.DecodeString(att.Base64)
		if err != nil {
			log.Printf("attachment decode error (%s): %v", att.Filename, err)
			continue
		}
		fb := tgbotapi.FileBytes{Name: att.Filename, Bytes: raw}

		if att.Type == "image" {
			photo := tgbotapi.NewPhoto(chatID, fb)
			if _, err := b.api.Send(photo); err != nil {
				log.Printf("send photo (%s): %v", att.Filename, err)
				// Fall back to sending as document if photo fails.
				doc := tgbotapi.NewDocument(chatID, fb)
				if _, err := b.api.Send(doc); err != nil {
					log.Printf("send document fallback (%s): %v", att.Filename, err)
				}
			}
		} else {
			doc := tgbotapi.NewDocument(chatID, fb)
			if _, err := b.api.Send(doc); err != nil {
				log.Printf("send document (%s): %v", att.Filename, err)
			}
		}
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

// redact replaces every occurrence of secret in s. Telegram puts the bot token
// in the request path, so any transport error (*url.Error prints its URL)
// carries it verbatim into the logs.
func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "<redacted>")
}

// RedactingWriter wraps w so every write has the given secrets masked. The bot
// binary points the standard logger at it, which covers our own log lines that
// print tgbotapi errors as well as the library's internal ones.
func RedactingWriter(w io.Writer, secrets ...string) io.Writer {
	var keep []string
	for _, s := range secrets {
		if s != "" {
			keep = append(keep, s)
		}
	}
	return &redactingWriter{w: w, secrets: keep}
}

type redactingWriter struct {
	w       io.Writer
	secrets []string
}

func (r *redactingWriter) Write(p []byte) (int, error) {
	s := string(p)
	for _, sec := range r.secrets {
		s = redact(s, sec)
	}
	if _, err := io.WriteString(r.w, s); err != nil {
		return 0, err
	}
	// Report the caller's length: log.Logger treats a short count as failure.
	return len(p), nil
}
