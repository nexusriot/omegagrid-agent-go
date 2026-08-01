package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// fakeTelegram stands in for api.telegram.org: it answers getMe so the client
// constructs, and records every outgoing message.
type fakeTelegram struct {
	mu   sync.Mutex
	sent []string
	srv  *httptest.Server
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		_ = r.ParseForm()

		switch method {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"username":"testbot"}}`))
			return
		case "sendMessage", "editMessageText":
			f.mu.Lock()
			f.sent = append(f.sent, r.FormValue("text"))
			f.mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"},"text":"x"}}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTelegram) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *fakeTelegram) lastMessage(t *testing.T) string {
	t.Helper()
	msgs := f.messages()
	if len(msgs) == 0 {
		t.Fatal("the bot sent no message")
	}
	return msgs[len(msgs)-1]
}

// newTestBot wires a Bot against the fake Telegram and the given gateway URL.
func newTestBot(t *testing.T, f *fakeTelegram, auth *AuthStore, gatewayURL string) *Bot {
	t.Helper()
	api, err := tgbotapi.NewBotAPIWithAPIEndpoint("test-token", f.srv.URL+"/bot%s/%s")
	if err != nil {
		t.Fatalf("NewBotAPIWithAPIEndpoint: %v", err)
	}
	return &Bot{
		api:        api,
		gatewayURL: strings.TrimRight(gatewayURL, "/"),
		auth:       auth,
		agent:      NewAgentClient(gatewayURL),
		sessions:   map[int64]int{},
	}
}

// command builds a /name message the way Telegram delivers it, with the entity
// offsets tgbotapi needs to recognise it as a command.
func command(chatID int64, name, args string) *tgbotapi.Message {
	text := "/" + name
	if args != "" {
		text += " " + args
	}
	return &tgbotapi.Message{
		MessageID: 1,
		Chat:      &tgbotapi.Chat{ID: chatID, Type: "private"},
		Text:      text,
		Entities: []tgbotapi.MessageEntity{
			{Type: "bot_command", Offset: 0, Length: len(name) + 1},
		},
	}
}

func TestHandleStartShowsHelp(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, "http://unused")
	b.setSession(1, 42)

	b.handle(command(1, "start", ""))

	got := f.lastMessage(t)
	for _, want := range []string{"/ask", "/new", "/skills", "/auth_add", "/auth_list"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help text is missing %q:\n%s", want, got)
		}
	}
	// /start also resets the conversation.
	if sid := b.getSession(1); sid != 0 {
		t.Fatalf("session after /start = %d, want 0", sid)
	}
	// With auth disabled the user's own ID is not advertised.
	if strings.Contains(got, "Your Telegram ID") {
		t.Fatalf("ID hint shown while auth is disabled:\n%s", got)
	}
}

func TestHandleStartShowsIDWhenAuthEnabled(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, newAuthStore(t, 999), "http://unused")

	b.handle(command(4242, "start", ""))

	if got := f.lastMessage(t); !strings.Contains(got, "4242") {
		t.Fatalf("help does not show the caller's ID so they can request access:\n%s", got)
	}
}

// Every user-facing command must go through the allowlist.
func TestUnauthorizedUserIsRefused(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, newAuthStore(t, 999), "http://unused")

	for _, msg := range []*tgbotapi.Message{
		command(1234, "ask", "what is the time?"),
		command(1234, "new", ""),
		command(1234, "skills", ""),
		{MessageID: 1, Chat: &tgbotapi.Chat{ID: 1234, Type: "private"}, Text: "plain text"},
	} {
		before := len(f.messages())
		b.handle(msg)
		sent := f.messages()[before:]
		if len(sent) != 1 {
			t.Fatalf("%q produced %d messages, want exactly the refusal", msg.Text, len(sent))
		}
		if !strings.Contains(sent[0], "Access denied") {
			t.Fatalf("%q was not refused: %q", msg.Text, sent[0])
		}
	}
}

func TestAuthorizedUserReachesTheAgent(t *testing.T) {
	var gotQuery string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotQuery, _ = req["query"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: final\ndata: " +
			`{"event":"final","session_id":7,"answer":"It is noon.","meta":{"model":"m","step_count":1}}` + "\n\n"))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	auth := newAuthStore(t, 999)
	if err := auth.AddUser(1234); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	b := newTestBot(t, f, auth, gw.URL)

	b.handle(command(1234, "ask", "what is the time?"))

	if gotQuery != "what is the time?" {
		t.Fatalf("gateway received %q", gotQuery)
	}
	got := f.lastMessage(t)
	if !strings.Contains(got, "It is noon.") {
		t.Fatalf("answer not delivered: %q", got)
	}
	// /ask appends the model + step count.
	if !strings.Contains(got, "model: m") || !strings.Contains(got, "steps: 1") {
		t.Fatalf("/ask did not append the run metadata: %q", got)
	}
	// The session id from the answer is remembered for the next turn.
	if sid := b.getSession(1234); sid != 7 {
		t.Fatalf("session = %d, want 7", sid)
	}
}

// A plain (non-command) message is treated as a question, without the /ask
// metadata suffix.
func TestPlainTextIsForwardedToTheAgent(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: final\ndata: " +
			`{"event":"final","session_id":3,"answer":"plain answer","meta":{"model":"m","step_count":2}}` + "\n\n"))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, gw.URL)

	b.handle(&tgbotapi.Message{
		MessageID: 1, Chat: &tgbotapi.Chat{ID: 5, Type: "private"}, Text: "hello there",
	})

	got := f.lastMessage(t)
	if !strings.Contains(got, "plain answer") {
		t.Fatalf("answer = %q", got)
	}
	if strings.Contains(got, "model:") {
		t.Fatalf("plain messages must not carry the /ask metadata suffix: %q", got)
	}
}

func TestAskWithoutArgumentsShowsUsage(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, "http://unused")

	b.handle(command(1, "ask", ""))
	if got := f.lastMessage(t); !strings.Contains(got, "Usage: /ask") {
		t.Fatalf("message = %q", got)
	}
}

func TestHandleNewResetsSession(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, "http://unused")
	b.setSession(1, 99)

	b.handle(command(1, "new", ""))

	if sid := b.getSession(1); sid != 0 {
		t.Fatalf("session = %d, want 0", sid)
	}
	if got := f.lastMessage(t); !strings.Contains(got, "Session reset") {
		t.Fatalf("message = %q", got)
	}
}

func TestHandleSkillsListsFromGateway(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/skills" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"skills":[{"name":"weather","description":"Current weather"}]}`))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, gw.URL)

	b.handle(command(1, "skills", ""))

	got := f.lastMessage(t)
	if !strings.Contains(got, "weather") || !strings.Contains(got, "Current weather") {
		t.Fatalf("skills message = %q", got)
	}
}

func TestHandleSkillsReportsGatewayFailure(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, gw.URL)

	b.handle(command(1, "skills", ""))
	if got := f.lastMessage(t); !strings.Contains(got, "Error") {
		t.Fatalf("a broken gateway response was not reported: %q", got)
	}
}

func TestHandleSkillsEmptyList(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"skills":[]}`))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, gw.URL)

	b.handle(command(1, "skills", ""))
	if got := f.lastMessage(t); !strings.Contains(got, "No skills") {
		t.Fatalf("message = %q", got)
	}
}

func TestAuthCommandsAreAdminOnly(t *testing.T) {
	f := newFakeTelegram(t)
	auth := newAuthStore(t, 999)
	b := newTestBot(t, f, auth, "http://unused")

	// A non-admin cannot add users or list them.
	for _, cmd := range []string{"auth_add", "auth_list"} {
		b.handle(command(1234, cmd, "5555"))
		if got := f.lastMessage(t); !strings.Contains(got, "Only the admin") {
			t.Fatalf("/%s from a non-admin: %q", cmd, got)
		}
	}

	// The admin can.
	b.handle(command(999, "auth_add", "5555"))
	if got := f.lastMessage(t); !strings.Contains(got, "5555") {
		t.Fatalf("/auth_add: %q", got)
	}
	if !auth.IsAuthorized(5555) {
		t.Fatal("/auth_add did not allowlist the user")
	}

	b.handle(command(999, "auth_list", ""))
	got := f.lastMessage(t)
	if !strings.Contains(got, "5555") || !strings.Contains(got, "Admin: 999") {
		t.Fatalf("/auth_list: %q", got)
	}
}

func TestAuthAddValidatesArgument(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, newAuthStore(t, 999), "http://unused")

	b.handle(command(999, "auth_add", ""))
	if got := f.lastMessage(t); !strings.Contains(got, "Usage: /auth_add") {
		t.Fatalf("message = %q", got)
	}

	b.handle(command(999, "auth_add", "not-a-number"))
	if got := f.lastMessage(t); !strings.Contains(got, "must be an integer") {
		t.Fatalf("message = %q", got)
	}
}

func TestAuthCommandsWhenAuthDisabled(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, "http://unused")

	for _, cmd := range []string{"auth_add", "auth_list"} {
		b.handle(command(1, cmd, "5"))
		if got := f.lastMessage(t); !strings.Contains(got, "Auth is disabled") {
			t.Fatalf("/%s: %q", cmd, got)
		}
	}
}

// An unknown command is ignored rather than answered — the bot shares chats
// with other bots.
func TestUnknownCommandIsIgnored(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, "http://unused")

	b.handle(command(1, "somethingelse", ""))
	if msgs := f.messages(); len(msgs) != 0 {
		t.Fatalf("unknown command produced %v", msgs)
	}
}

func TestEmptyTextIsIgnored(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, "http://unused")

	b.handle(&tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 1, Type: "private"}, Text: "   "})
	if msgs := f.messages(); len(msgs) != 0 {
		t.Fatalf("whitespace-only message produced %v", msgs)
	}
}

// A handler panic must not take the bot down — each update runs on its own
// goroutine, so an unrecovered panic would kill the process.
func TestHandleRecoversFromPanic(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, nil, "http://unused") // nil auth → ensureAuthorized panics

	b.handle(&tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 1, Type: "private"}, Text: "hi"})
}

// When streaming fails the bot retries against the synchronous endpoint so the
// user always gets an answer.
func TestStreamFailureFallsBackToSyncEndpoint(t *testing.T) {
	var syncCalls int
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stream") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		syncCalls++
		_, _ = w.Write([]byte(`{"session_id":11,"answer":"fallback answer","meta":{"model":"m","step_count":1}}`))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, gw.URL)

	b.handle(&tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 2, Type: "private"}, Text: "hi"})

	if syncCalls != 1 {
		t.Fatalf("sync endpoint called %d times, want 1", syncCalls)
	}
	if got := f.lastMessage(t); !strings.Contains(got, "fallback answer") {
		t.Fatalf("fallback answer not delivered: %q", got)
	}
	// The user is told the stream broke rather than staring at a stale status.
	var sawRetryNotice bool
	for _, m := range f.messages() {
		if strings.Contains(m, "Stream interrupted") {
			sawRetryNotice = true
		}
	}
	if !sawRetryNotice {
		t.Fatalf("no interruption notice among %v", f.messages())
	}
	if sid := b.getSession(2); sid != 11 {
		t.Fatalf("session = %d, want 11 from the fallback response", sid)
	}
}

func TestBothEndpointsFailingReportsTheError(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"gateway is down"}`))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, gw.URL)

	b.handle(&tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 3, Type: "private"}, Text: "hi"})

	if got := f.lastMessage(t); !strings.Contains(got, "Error") {
		t.Fatalf("total failure was not reported to the user: %q", got)
	}
}

// A stream that ends with an error event reports it rather than falling back.
func TestErrorEventIsReported(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\ndata: " +
			`{"event":"error","error":"llm transport failed"}` + "\n\n"))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, gw.URL)

	b.handle(&tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 4, Type: "private"}, Text: "hi"})

	if got := f.lastMessage(t); !strings.Contains(got, "llm transport failed") {
		t.Fatalf("error event not surfaced: %q", got)
	}
}

// A long answer is split so no chunk exceeds Telegram's limit; the whole answer
// still reaches the user.
func TestLongAnswerIsChunked(t *testing.T) {
	long := strings.Repeat("word ", 2000) + "END"
	payload, _ := json.Marshal(map[string]any{
		"event": "final", "session_id": 1, "answer": long, "meta": map[string]any{},
	})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: final\ndata: " + string(payload) + "\n\n"))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, gw.URL)

	b.handle(&tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 6, Type: "private"}, Text: "hi"})

	msgs := f.messages()
	if len(msgs) < 3 { // status + at least two chunks
		t.Fatalf("long answer produced only %d messages", len(msgs))
	}
	var joined strings.Builder
	for _, m := range msgs {
		if len([]rune(m)) > telegramMaxLen {
			t.Fatalf("a chunk is %d chars, over the %d limit", len([]rune(m)), telegramMaxLen)
		}
		joined.WriteString(m)
	}
	if !strings.Contains(joined.String(), "END") {
		t.Fatal("the tail of the answer was dropped")
	}
}
