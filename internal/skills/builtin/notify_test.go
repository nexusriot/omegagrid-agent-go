package builtin

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestNotifyTelegram(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = map[string]string{"path": r.URL.Path, "chat": r.Form.Get("chat_id"), "text": r.Form.Get("text")}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	saved := telegramAPIBase
	telegramAPIBase = srv.URL
	defer func() { telegramAPIBase = saved }()

	n := Notify(NotifyConfig{TelegramToken: "TOK", TelegramChats: []string{"42"}, TelegramDefaultChat: "7"})
	m := runExec(t, n, map[string]any{"channel": "telegram", "message": "build green", "title": "CI"})
	if m["sent"] != true || got["path"] != "/botTOK/sendMessage" || got["chat"] != "7" || got["text"] != "CI\n\nbuild green" {
		t.Fatalf("result=%v request=%v", m, got)
	}
	if m := runExec(t, n, map[string]any{"channel": "telegram", "message": "x", "to": "42"}); m["sent"] != true {
		t.Fatalf("allowlisted chat refused: %v", m)
	}
	if m := runExec(t, n, map[string]any{"channel": "telegram", "message": "x", "to": "999"}); m["sent"] == true || !strings.Contains(m["error"].(string), "NOTIFY_TELEGRAM_CHATS") {
		t.Fatalf("non-allowlisted chat = %v", m)
	}
	if m := runExec(t, n, map[string]any{"channel": "telegram"}); m["error"] == nil {
		t.Fatal("empty message accepted")
	}
	if m := runExec(t, Notify(NotifyConfig{}), map[string]any{"channel": "telegram", "message": "x"}); m["sent"] == true {
		t.Fatal("unconfigured telegram sent")
	}
}

func TestNotifyWebhook(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(r.URL.Path, "fail") {
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	hooks := ParseWebhooks([]string{"Ops=" + srv.URL + "/hook", "broken=" + srv.URL + "/fail", "bad=ftp://x", "noequals"})
	if len(hooks) != 2 {
		t.Fatalf("ParseWebhooks = %v", hooks)
	}
	n := Notify(NotifyConfig{Webhooks: hooks})
	m := runExec(t, n, map[string]any{"channel": "webhook", "to": "ops", "message": "disk 91%", "title": "alert"})
	if m["sent"] != true || body["text"] != "alert\n\ndisk 91%" || body["content"] != body["text"] {
		t.Fatalf("result=%v body=%v", m, body)
	}
	if m := runExec(t, n, map[string]any{"channel": "webhook", "to": "broken", "message": "x"}); m["sent"] == true {
		t.Fatal("HTTP 500 reported as sent")
	}
	if m := runExec(t, n, map[string]any{"channel": "webhook", "to": "http://evil.example", "message": "x"}); m["sent"] == true {
		t.Fatal("an arbitrary URL was used as a webhook")
	}
	list := runExec(t, n, map[string]any{"channel": "list"})
	if !strings.Contains(jsonOf(t, list), `"broken","ops"`) {
		t.Fatalf("list = %v", list)
	}
}

// fakeSMTP accepts one message and records the envelope and data.
type fakeSMTP struct {
	mu   sync.Mutex
	from string
	rcpt string
	data string
	auth bool
}

func startFakeSMTP(t *testing.T) (*fakeSMTP, string, int) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSMTP{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return f, host, p
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	tp := textproto.NewConn(c)
	_ = tp.PrintfLine("220 fake ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			_ = tp.PrintfLine("250-fake\r\n250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH"):
			f.mu.Lock()
			f.auth = true
			f.mu.Unlock()
			_ = tp.PrintfLine("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			f.mu.Lock()
			f.from = line[10:]
			f.mu.Unlock()
			_ = tp.PrintfLine("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			f.mu.Lock()
			f.rcpt = line[8:]
			f.mu.Unlock()
			_ = tp.PrintfLine("250 ok")
		case cmd == "DATA":
			_ = tp.PrintfLine("354 go")
			b, _ := tp.ReadDotBytes()
			f.mu.Lock()
			f.data = string(b)
			f.mu.Unlock()
			_ = tp.PrintfLine("250 queued")
		case cmd == "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("502 no")
		}
	}
}

func TestNotifyEmail(t *testing.T) {
	f, host, port := startFakeSMTP(t)
	n := Notify(NotifyConfig{SMTPHost: host, SMTPPort: port, SMTPFrom: "agent@example.org", EmailTo: []string{"ops@example.org", "@team.example"}})
	m := runExec(t, n, map[string]any{"channel": "email", "to": "Ann@Team.Example", "title": "Weekly\r\nBcc: x@y", "message": "line1\nline2"})
	if m["sent"] != true {
		t.Fatalf("email = %v", m)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.from != "<agent@example.org>" || f.rcpt != "<Ann@Team.Example>" {
		t.Fatalf("envelope from=%q rcpt=%q", f.from, f.rcpt)
	}
	if !strings.Contains(f.data, "Subject: Weekly  Bcc: x@y\n") || strings.Contains(f.data, "\nBcc:") || !strings.Contains(f.data, "line1\nline2") {
		t.Fatalf("data = %q", f.data)
	}
	for _, bad := range []string{"eve@evil.example", "a@b.c, ops@example.org", "ops@example.org\r\nRCPT TO:<x@y>"} {
		if m := runExec(t, n, map[string]any{"channel": "email", "to": bad, "message": "x"}); m["sent"] == true {
			t.Errorf("recipient %q accepted", bad)
		}
	}
	if !emailAllowed("ops@example.org", []string{"OPS@example.org"}) || emailAllowed("ops@example.org.evil", []string{"@example.org"}) {
		t.Fatal("emailAllowed matching")
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
