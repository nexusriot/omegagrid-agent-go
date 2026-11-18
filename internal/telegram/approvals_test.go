package telegram

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// fakeApprovalGateway serves GET/POST /api/approvals/{id} for one pending
// request that belongs to chatID.
type fakeApprovalGateway struct {
	mu       sync.Mutex
	pending  bool
	chatID   int64
	resolved map[string]any
	srv      *httptest.Server
}

func newFakeApprovalGateway(t *testing.T, chatID int64) *fakeApprovalGateway {
	t.Helper()
	g := &fakeApprovalGateway{pending: true, chatID: chatID}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/approvals/ab12" {
			http.NotFound(w, r)
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if !g.pending {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"approval not found"}`)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "ab12", "tool": "shell_command", "args": map[string]any{"command": "ls"},
				"telegram_chat_id": g.chatID, "expires_at": time.Now().Add(time.Minute),
			})
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&g.resolved)
			g.pending = false
			_, _ = io.WriteString(w, `{"id":"ab12"}`)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeApprovalGateway) decision() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.resolved
}

func callback(fromID, chatID int64, data string) *tgbotapi.CallbackQuery {
	return &tgbotapi.CallbackQuery{
		ID:      "cb1",
		From:    &tgbotapi.User{ID: fromID},
		Message: &tgbotapi.Message{MessageID: 5, Chat: &tgbotapi.Chat{ID: chatID}},
		Data:    data,
	}
}

func lastCall(t *testing.T, f *fakeTelegram, method string) string {
	t.Helper()
	calls := f.callLog()
	for i := len(calls) - 1; i >= 0; i-- {
		if strings.HasPrefix(calls[i], method+" ") {
			return calls[i]
		}
	}
	t.Fatalf("no %s call in %v", method, calls)
	return ""
}

func TestApprovalCallbackApprovesForSession(t *testing.T) {
	gw := newFakeApprovalGateway(t, 1234)
	f := newFakeTelegram(t)
	auth := newAuthStore(t, 999)
	_ = auth.AddUser(1234)
	b := newTestBot(t, f, auth, gw.srv.URL)

	b.handleCallback(callback(1234, 1234, "ap:s:ab12"))

	d := gw.decision()
	if d["approved"] != true || d["scope"] != "session" || d["by"] != "telegram:1234" {
		t.Fatalf("decision %v", d)
	}
	if got := lastCall(t, f, "editMessageText"); !strings.Contains(got, "Approved for this session") {
		t.Fatalf("message not updated: %q", got)
	}
}

func TestApprovalCallbackDeny(t *testing.T) {
	gw := newFakeApprovalGateway(t, 1234)
	f := newFakeTelegram(t)
	auth := newAuthStore(t, 999)
	_ = auth.AddUser(1234)
	b := newTestBot(t, f, auth, gw.srv.URL)

	b.handleCallback(callback(1234, 1234, "ap:n:ab12"))
	if d := gw.decision(); d["approved"] != false || d["scope"] != nil {
		t.Fatalf("decision %v", d)
	}
	if got := lastCall(t, f, "editMessageText"); !strings.Contains(got, "Denied") {
		t.Fatalf("message not updated: %q", got)
	}
	// A second press finds it decided.
	b.handleCallback(callback(1234, 1234, "ap:y:ab12"))
	if got := lastCall(t, f, "editMessageText"); !strings.Contains(got, "already decided") {
		t.Fatalf("second press: %q", got)
	}
}

func TestApprovalCallbackRefusesStrangersAndOtherChats(t *testing.T) {
	gw := newFakeApprovalGateway(t, 1234)
	f := newFakeTelegram(t)
	auth := newAuthStore(t, 999)
	_ = auth.AddUser(1234)
	_ = auth.AddUser(5555)
	b := newTestBot(t, f, auth, gw.srv.URL)

	// Not an authorized user.
	b.handleCallback(callback(7777, 1234, "ap:y:ab12"))
	// Authorized, but the button was forwarded into another chat.
	b.handleCallback(callback(5555, 5555, "ap:y:ab12"))
	// Malformed data.
	b.handleCallback(callback(1234, 1234, "zz"))
	b.handleCallback(&tgbotapi.CallbackQuery{ID: "x", From: &tgbotapi.User{ID: 1234}, Data: "ap:y:ab12"}) // no message

	if d := gw.decision(); d != nil {
		t.Fatalf("approval decided by a refused press: %v", d)
	}
	var answers []string
	for _, c := range f.callLog() {
		if strings.HasPrefix(c, "answerCallbackQuery") {
			answers = append(answers, c)
		}
	}
	if len(answers) != 4 {
		t.Fatalf("every press must be answered: %v", answers)
	}
}

func TestApprovalPromptButtonsAndText(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, newAuthStore(t, 999), "http://127.0.0.1:1")
	id := b.sendApprovalPrompt(1234, &ApprovalInfo{ID: "ab12", Tool: "file_write",
		Args: map[string]any{"path": "x.md"}, Why: "save notes", ExpiresAt: time.Now().Add(time.Minute)})
	if id == 0 {
		t.Fatal("no message id")
	}
	got := lastCall(t, f, "sendMessage")
	for _, want := range []string{"Approval needed: file_write", "x.md", "Why: save notes", "Expires in", "ap:y:ab12", "ap:n:ab12", "ap:s:ab12"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q: %s", want, got)
		}
	}
	b.markApprovalResolved(1234, 0, nil, nil) // no message: no-op
	b.markApprovalResolved(1234, id, &ApprovalInfo{Tool: "file_write"}, &ApprovalDecision{Approved: false, By: "timeout"})
	if got := lastCall(t, f, "editMessageText"); !strings.Contains(got, "Denied (timeout)") {
		t.Fatalf("resolved text %q", got)
	}
	if s := truncateRunes("héllo wörld", 5); s != "héllo…" {
		t.Fatalf("truncateRunes = %q", s)
	}
}

// The streaming handler posts the approval card when the run pauses and
// updates it when the decision arrives.
func TestStreamApprovalEventsReachTheChat(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		ap := `{"id":"ab12","tool":"shell_command","args":{"command":"ls"},"telegram_chat_id":1234}`
		_, _ = io.WriteString(w,
			"event: approval_required\ndata: {\"event\":\"approval_required\",\"tool\":\"shell_command\",\"approval\":"+ap+"}\n\n"+
				"event: approval_resolved\ndata: {\"event\":\"approval_resolved\",\"tool\":\"shell_command\",\"approval\":"+ap+",\"decision\":{\"approved\":true,\"by\":\"web\",\"scope\":\"session\"}}\n\n"+
				"event: final\ndata: {\"event\":\"final\",\"session_id\":3,\"answer\":\"done\",\"meta\":{}}\n\n")
	}))
	defer gw.Close()
	f := newFakeTelegram(t)
	auth := newAuthStore(t, 999)
	_ = auth.AddUser(1234)
	b := newTestBot(t, f, auth, gw.URL)

	b.handle(command(1234, "ask", "list files"))

	calls := strings.Join(f.callLog(), "\n")
	if !strings.Contains(calls, "ap:s:ab12") {
		t.Fatalf("no approval prompt:\n%s", calls)
	}
	if !strings.Contains(calls, "Approved for this session (web)") {
		t.Fatalf("prompt not marked resolved:\n%s", calls)
	}
	if !strings.Contains(f.lastMessage(t), "done") {
		t.Fatalf("answer missing: %q", f.lastMessage(t))
	}
}

func TestSendAttachments(t *testing.T) {
	f := newFakeTelegram(t)
	b := newTestBot(t, f, newAuthStore(t, 999), "http://127.0.0.1:1")
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	b.sendAttachments(1, []Attachment{
		{Type: "image", Filename: "chart.png", MimeType: "image/png", Base64: png},
		{Type: "file", Filename: "report.csv", MimeType: "text/csv", Base64: base64.StdEncoding.EncodeToString([]byte("a,b"))},
		{Type: "image", Filename: "broken.png", Base64: "!!not base64"},
	})
	var methods []string
	for _, c := range f.callLog() {
		if m, _, _ := strings.Cut(c, " "); m == "sendPhoto" || m == "sendDocument" {
			methods = append(methods, m)
		}
	}
	if strings.Join(methods, ",") != "sendPhoto,sendDocument" {
		t.Fatalf("sent %v (broken base64 must be skipped)", methods)
	}
}
