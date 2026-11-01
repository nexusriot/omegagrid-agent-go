package builtin

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
)

func NotifySchema() Skill {
	return Skill{Name: "notify", Description: "Send a message to the operator through a configured channel: telegram, email or a named webhook. " +
		"Recipients are limited to the configured allowlists; call with channel='list' to see what is available.",
		Parameters: map[string]Param{
			"channel": {Type: "string", Description: "telegram | email | webhook | list", Required: true},
			"message": {Type: "string", Description: "Message text (plain text, up to 4000 characters)", Required: false},
			"title":   {Type: "string", Description: "Optional title / email subject", Required: false},
			"to":      {Type: "string", Description: "telegram: chat id (default: the configured default chat); email: address; webhook: webhook name", Required: false},
		}}
}

// NotifyConfig is the operator's side of the notify skill. Every channel is
// off until configured, and the model can only choose among the allowlisted
// recipients — never an arbitrary chat, address or URL.
type NotifyConfig struct {
	TelegramToken       string
	TelegramChats       []string
	TelegramDefaultChat string
	EmailTo             []string // exact addresses, or "@example.com" for a whole domain
	SMTPHost            string
	SMTPPort            int
	SMTPUser            string
	SMTPPassword        string
	SMTPFrom            string
	Webhooks            map[string]string // name → URL
}

// ParseWebhooks turns "name=url" entries into a map, skipping malformed ones.
func ParseWebhooks(entries []string) map[string]string {
	out := map[string]string{}
	for _, e := range entries {
		name, u, ok := strings.Cut(e, "=")
		name, u = strings.TrimSpace(name), strings.TrimSpace(u)
		if ok && name != "" && checkHTTPURL(u) == nil {
			out[strings.ToLower(name)] = u
		}
	}
	return out
}

// telegramAPIBase is the Bot API root, a seam for tests.
var telegramAPIBase = "https://api.telegram.org"

const maxNotifyChars = 4000

// Notify is NotifyContext without cancellation.
func Notify(cfg NotifyConfig) Executor { return withoutContext(NotifyContext(cfg)) }

func NotifyContext(cfg NotifyConfig) ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		channel := strings.ToLower(strings.TrimSpace(str(args, "channel")))
		if channel == "list" || channel == "" {
			return cfg.channels(), nil
		}
		msg := strings.TrimSpace(str(args, "message"))
		if msg == "" {
			return map[string]any{"error": "message is required"}, nil
		}
		if r := []rune(msg); len(r) > maxNotifyChars {
			msg = string(r[:maxNotifyChars]) + "…"
		}
		title := strings.TrimSpace(str(args, "title"))
		to := strings.TrimSpace(str(args, "to"))
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		var dest string
		var err error
		switch channel {
		case "telegram":
			dest, err = cfg.sendTelegram(ctx, to, title, msg)
		case "email":
			dest, err = cfg.sendEmail(ctx, to, title, msg)
		case "webhook":
			dest, err = cfg.sendWebhook(ctx, to, title, msg)
		default:
			return map[string]any{"error": fmt.Sprintf("unknown channel %q (use telegram, email, webhook or list)", channel)}, nil
		}
		if err != nil {
			return map[string]any{"channel": channel, "sent": false, "error": err.Error()}, nil
		}
		return map[string]any{"channel": channel, "sent": true, "to": dest}, nil
	}
}

// channels describes what is configured, without secrets.
func (cfg NotifyConfig) channels() map[string]any {
	out := map[string]any{}
	if cfg.TelegramToken != "" && (len(cfg.TelegramChats) > 0 || cfg.TelegramDefaultChat != "") {
		out["telegram"] = map[string]any{"chats": cfg.telegramAllowed(), "default": cfg.TelegramDefaultChat}
	}
	if cfg.SMTPHost != "" && len(cfg.EmailTo) > 0 {
		out["email"] = map[string]any{"allowed": cfg.EmailTo}
	}
	if len(cfg.Webhooks) > 0 {
		names := make([]string, 0, len(cfg.Webhooks))
		for n := range cfg.Webhooks {
			names = append(names, n)
		}
		slices.Sort(names)
		out["webhook"] = map[string]any{"names": names}
	}
	if len(out) == 0 {
		return map[string]any{"channels": out, "hint": "no notification channel is configured (NOTIFY_* settings)"}
	}
	return map[string]any{"channels": out}
}

func (cfg NotifyConfig) telegramAllowed() []string {
	allowed := slices.Clone(cfg.TelegramChats)
	if cfg.TelegramDefaultChat != "" && !slices.Contains(allowed, cfg.TelegramDefaultChat) {
		allowed = append(allowed, cfg.TelegramDefaultChat)
	}
	return allowed
}

func (cfg NotifyConfig) sendTelegram(ctx context.Context, to, title, msg string) (string, error) {
	if cfg.TelegramToken == "" {
		return "", errors.New("telegram is not configured (TELEGRAM_BOT_TOKEN)")
	}
	if to == "" {
		to = cfg.TelegramDefaultChat
	}
	if to == "" {
		return "", errors.New("no chat id given and NOTIFY_TELEGRAM_DEFAULT_CHAT is not set")
	}
	if !slices.Contains(cfg.telegramAllowed(), to) {
		return "", fmt.Errorf("chat %s is not in NOTIFY_TELEGRAM_CHATS", to)
	}
	if _, err := strconv.ParseInt(to, 10, 64); err != nil {
		return "", fmt.Errorf("invalid chat id %q", to)
	}
	text := msg
	if title != "" {
		text = title + "\n\n" + msg
	}
	form := url.Values{"chat_id": {to}, "text": {text}}
	endpoint := telegramAPIBase + "/bot" + cfg.TelegramToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("build telegram request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := netguard.Client(30 * time.Second).Do(req)
	if err != nil {
		// The URL carries the bot token; never echo the transport error's URL.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", fmt.Errorf("telegram: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
	if !out.OK {
		return "", fmt.Errorf("telegram: %s (HTTP %d)", nonEmpty(out.Description, "send failed"), resp.StatusCode)
	}
	return to, nil
}

// emailAllowed reports whether addr matches an allowlist entry (an exact
// address or "@domain"), case-insensitively.
func emailAllowed(addr string, allow []string) bool {
	addr = strings.ToLower(addr)
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == addr || (strings.HasPrefix(a, "@") && strings.HasSuffix(addr, a)) {
			return true
		}
	}
	return false
}

func (cfg NotifyConfig) sendEmail(ctx context.Context, to, title, msg string) (string, error) {
	if cfg.SMTPHost == "" || cfg.SMTPFrom == "" {
		return "", errors.New("email is not configured (NOTIFY_SMTP_HOST, NOTIFY_SMTP_FROM)")
	}
	if to == "" && len(cfg.EmailTo) > 0 && !strings.HasPrefix(cfg.EmailTo[0], "@") {
		to = cfg.EmailTo[0]
	}
	if to == "" || strings.ContainsAny(to, "\r\n,;<> ") || !strings.Contains(to, "@") {
		return "", fmt.Errorf("invalid recipient %q", to)
	}
	if !emailAllowed(to, cfg.EmailTo) {
		return "", fmt.Errorf("%s is not in NOTIFY_EMAIL_TO", to)
	}
	subject := title
	if subject == "" {
		subject = "OmegaGrid notification"
	}
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	body := "From: " + cfg.SMTPFrom + "\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nDate: " + time.Now().Format(time.RFC1123Z) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		strings.ReplaceAll(strings.ReplaceAll(msg, "\r\n", "\n"), "\n", "\r\n") + "\r\n"

	port := cfg.SMTPPort
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(port))
	conn, err := netguard.Dialer(15*time.Second).DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("smtp dial: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12}
	if port == 465 {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return "", fmt.Errorf("smtp tls: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("smtp: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok && port != 465 {
		if err := c.StartTLS(tlsCfg); err != nil {
			return "", fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if cfg.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)); err != nil {
			return "", fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(cfg.SMTPFrom); err != nil {
		return "", fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return "", fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return "", fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := io.WriteString(w, body); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("smtp DATA: %w", err)
	}
	_ = c.Quit()
	return to, nil
}

func (cfg NotifyConfig) sendWebhook(ctx context.Context, to, title, msg string) (string, error) {
	name := strings.ToLower(to)
	if name == "" && len(cfg.Webhooks) == 1 {
		for n := range cfg.Webhooks {
			name = n
		}
	}
	target, ok := cfg.Webhooks[name]
	if !ok {
		return "", fmt.Errorf("unknown webhook %q (configured in NOTIFY_WEBHOOKS)", to)
	}
	// "text" suits Slack, Mattermost and most generic receivers, "content"
	// suits Discord; sending both covers them without per-service config.
	payload, _ := json.Marshal(map[string]any{"title": title, "text": joinTitle(title, msg), "content": joinTitle(title, msg), "source": "omegagrid"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := netguard.Client(30 * time.Second).Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the webhook URL may embed a secret
		}
		return "", fmt.Errorf("webhook %s: %v", name, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("webhook %s answered HTTP %d", name, resp.StatusCode)
	}
	return name, nil
}

func joinTitle(title, msg string) string {
	if title == "" {
		return msg
	}
	return title + "\n\n" + msg
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
