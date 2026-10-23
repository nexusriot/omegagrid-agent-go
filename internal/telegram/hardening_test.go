package telegram

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// In a group the chat ID belongs to the group, not the sender. Authorizing on
// it let any member of an allowlisted group (or of a group whose ID happened
// to equal the admin's) drive the agent and the admin commands.
func TestAuthIsKeyedOnSenderNotChat(t *testing.T) {
	const admin = 999
	f := newFakeTelegram(t)
	b := newTestBot(t, f, newAuthStore(t, admin), "http://unused")

	// Stranger posting in a chat whose ID equals the admin's.
	msg := command(admin, "auth_add", "5555")
	msg.From = &tgbotapi.User{ID: 1234}
	b.handle(msg)
	if got := f.lastMessage(t); !strings.Contains(got, "Only the admin") {
		t.Fatalf("a non-admin sender in the admin's chat ran /auth_add: %q", got)
	}
	if b.auth.IsAuthorized(5555) {
		t.Fatal("the stranger's /auth_add took effect")
	}

	msg = command(admin, "new", "")
	msg.From = &tgbotapi.User{ID: 1234}
	b.handle(msg)
	if got := f.lastMessage(t); !strings.Contains(got, "Access denied") {
		t.Fatalf("a non-allowlisted sender was let through: %q", got)
	}

	// No sender at all (channel post) must fail closed while auth is on.
	msg = command(admin, "new", "")
	msg.From = nil
	b.handle(msg)
	if got := f.lastMessage(t); !strings.Contains(got, "Access denied") {
		t.Fatalf("a sender-less message was let through: %q", got)
	}

	// The admin in a group chat (different chat ID) is still the admin.
	msg = command(-100500, "auth_add", "5555")
	msg.Chat.Type = "group"
	msg.From = &tgbotapi.User{ID: admin}
	b.handle(msg)
	if !b.auth.IsAuthorized(5555) {
		t.Fatalf("the admin's /auth_add from a group was refused: %q", f.lastMessage(t))
	}
}

// A stream that already delivered progress events and then broke means the
// agent was running server-side; falling back to /api/query ran it again.
func TestPartialStreamDoesNotRerunTheAgent(t *testing.T) {
	var syncCalls atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: thinking\ndata: {\"event\":\"thinking\",\"step\":1}\n\n"))
			return // connection closes without a final event
		}
		syncCalls.Add(1)
		_, _ = w.Write([]byte(`{"session_id":1,"answer":"second run"}`))
	}))
	defer gw.Close()

	f := newFakeTelegram(t)
	b := newTestBot(t, f, &AuthStore{}, gw.URL)
	b.handle(&tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 7, Type: "private"}, Text: "hi"})

	if n := syncCalls.Load(); n != 0 {
		t.Fatalf("sync endpoint called %d times after a partial stream, want 0", n)
	}
	if got := f.lastMessage(t); !strings.Contains(got, "Stream interrupted") {
		t.Fatalf("interruption not reported: %q", got)
	}
}

// The final event inlines base64 attachments; anything over the old 1 MiB
// scanner cap was dropped as "token too long".
func TestQueryStreamAcceptsLargeEvents(t *testing.T) {
	big := strings.Repeat("A", 3<<20)
	payload, _ := json.Marshal(map[string]any{"event": "final", "session_id": 1, "answer": big})
	srv, _ := sseServer(t, "event: final\ndata: "+string(payload)+"\n\n")
	defer srv.Close()

	events := make(chan Event, 4)
	errCh := make(chan error, 1)
	go func() { errCh <- NewAgentClient(srv.URL, "").QueryStream("q", 1, 0, events) }()
	evs := drain(events)
	if err := <-errCh; err != nil {
		t.Fatalf("QueryStream: %v", err)
	}
	if len(evs) != 1 || len(evs[0].Answer) != len(big) {
		t.Fatalf("large final event lost: got %d event(s)", len(evs))
	}
}

// Every gateway call must carry the bearer token once GATEWAY_API_TOKEN is set.
func TestAgentClientSendsBearerToken(t *testing.T) {
	var missing atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			missing.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/query/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"event\":\"final\",\"answer\":\"ok\"}\n\n"))
		case "/api/query":
			_, _ = w.Write([]byte(`{"answer":"ok"}`))
		case "/api/skills":
			_, _ = w.Write([]byte(`{"skills":[]}`))
		}
	}))
	defer gw.Close()

	c := NewAgentClient(gw.URL, "s3cret")
	events := make(chan Event, 4)
	if err := c.QueryStream("q", 1, 0, events); err != nil {
		t.Fatalf("QueryStream: %v", err)
	}
	if _, err := c.Query("q", 1, 0); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if _, err := c.ListSkillsRaw(); err != nil {
		t.Fatalf("ListSkillsRaw: %v", err)
	}
	if n := missing.Load(); n != 0 {
		t.Fatalf("%d request(s) went out without the bearer token", n)
	}
}

// A gateway that accepts the request and never answers must not pin the
// handler forever.
func TestAgentClientTimesOut(t *testing.T) {
	release := make(chan struct{})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer gw.Close()
	defer close(release)

	c := NewAgentClient(gw.URL, "")
	c.timeout = 100 * time.Millisecond
	done := make(chan error, 1)
	go func() { _, err := c.Query("q", 1, 0); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a hung gateway returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Query did not honour its timeout")
	}
}

// Telegram transport errors are *url.Error values whose URL embeds the token.
func TestRedactingWriterMasksTheBotToken(t *testing.T) {
	const token = "123456:ABC-secret"
	var buf bytes.Buffer
	l := log.New(RedactingWriter(&buf, token, ""), "", 0)
	err := &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + token + "/getUpdates", Err: errors.New("timeout")}
	l.Printf("send: %v", err)

	if strings.Contains(buf.String(), token) {
		t.Fatalf("token leaked into the log: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "<redacted>") || !strings.Contains(buf.String(), "timeout") {
		t.Fatalf("log line mangled: %q", buf.String())
	}
}

// Updates in one chat are serialised; other chats proceed independently, and
// the lock table does not grow with every chat ever seen.
func TestChatLocksSerialisePerChat(t *testing.T) {
	b := newTestBot(t, newFakeTelegram(t), &AuthStore{}, "http://unused")

	unlock1 := b.lockChat(1)
	acquired := make(chan struct{})
	go func() {
		u := b.lockChat(1)
		close(acquired)
		u()
	}()

	other := make(chan struct{})
	go func() { b.lockChat(2)(); close(other) }()
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("a busy chat blocked an unrelated chat")
	}

	select {
	case <-acquired:
		t.Fatal("a second update in the same chat ran concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	unlock1()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("the queued update never ran")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		b.locksMu.Lock()
		n := len(b.chatLocks)
		b.locksMu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d chat lock(s) leaked", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The global semaphore caps concurrent handlers across chats.
func TestDispatchBoundsConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\":\"final\",\"answer\":\"ok\"}\n\n"))
	}))
	defer gw.Close()

	b := newTestBot(t, newFakeTelegram(t), &AuthStore{}, gw.URL)
	const chats = maxConcurrentHandlers + 4
	done := make(chan struct{}, chats)
	for i := 0; i < chats; i++ {
		msg := &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: int64(100 + i), Type: "private"}, Text: fmt.Sprint("q", i)}
		go func() { b.dispatch(msg); done <- struct{}{} }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for inFlight.Load() < maxConcurrentHandlers && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // give any excess handler a chance to slip in
	close(release)
	for i := 0; i < chats; i++ {
		<-done
	}
	if p := peak.Load(); p > maxConcurrentHandlers {
		t.Fatalf("peak concurrent agent runs = %d, want <= %d", p, maxConcurrentHandlers)
	}
}
