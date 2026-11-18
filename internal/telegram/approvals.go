package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ApprovalInfo mirrors agent.ApprovalRequest for the fields the bot shows.
type ApprovalInfo struct {
	ID             string         `json:"id"`
	Tool           string         `json:"tool"`
	Args           map[string]any `json:"args"`
	Why            string         `json:"why"`
	TelegramChatID *int64         `json:"telegram_chat_id"`
	ExpiresAt      time.Time      `json:"expires_at"`
}

// ApprovalDecision mirrors agent.ApprovalDecision.
type ApprovalDecision struct {
	Approved bool   `json:"approved"`
	By       string `json:"by"`
	Reason   string `json:"reason"`
	Scope    string `json:"scope,omitempty"`
}

// Callback data is "ap:<y|s|n>:<id>" (s = approve for the session) — well
// inside Telegram's 64-byte limit.
const approvalCallbackPrefix = "ap:"

const approvalTimeout = 20 * time.Second

// GetApproval fetches a pending approval from the gateway.
func (c *AgentClient) GetApproval(id string) (*ApprovalInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), approvalTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/api/approvals/"+id, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("gateway %d: %s", resp.StatusCode, string(raw))
	}
	var out ApprovalInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveApproval posts a decision. found=false: already decided or expired.
func (c *AgentClient) ResolveApproval(id string, approved bool, by, scope string) (found bool, err error) {
	m := map[string]any{"approved": approved, "by": by}
	if scope != "" {
		m["scope"] = scope
	}
	body, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), approvalTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodPost, "/api/approvals/"+id, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("gateway %d: %s", resp.StatusCode, string(raw))
	}
	return true, nil
}

// approvalText renders a request for the chat.
func approvalText(a *ApprovalInfo) string {
	args, _ := json.MarshalIndent(a.Args, "", "  ")
	s := fmt.Sprintf("🔐 Approval needed: %s\n\n%s", a.Tool, truncateRunes(string(args), 2500))
	if strings.TrimSpace(a.Why) != "" {
		s += "\n\nWhy: " + truncateRunes(a.Why, 300)
	}
	if !a.ExpiresAt.IsZero() {
		s += fmt.Sprintf("\n\nExpires in %s.", time.Until(a.ExpiresAt).Round(time.Second))
	}
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// sendApprovalPrompt posts the request with Approve / Deny buttons and
// returns the message id (0 on failure).
func (b *Bot) sendApprovalPrompt(chatID int64, a *ApprovalInfo) int {
	msg := tgbotapi.NewMessage(chatID, approvalText(a))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Approve", approvalCallbackPrefix+"y:"+a.ID),
			tgbotapi.NewInlineKeyboardButtonData("❌ Deny", approvalCallbackPrefix+"n:"+a.ID),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Approve for this session", approvalCallbackPrefix+"s:"+a.ID),
		),
	)
	sent, err := b.api.Send(msg)
	if err != nil {
		log.Printf("send approval prompt: %v", err)
		return 0
	}
	return sent.MessageID
}

// markApprovalResolved replaces the buttons with the outcome.
func (b *Bot) markApprovalResolved(chatID int64, msgID int, a *ApprovalInfo, d *ApprovalDecision) {
	if msgID == 0 || a == nil {
		return
	}
	outcome := "❌ Denied"
	if d != nil && d.Approved {
		outcome = "✅ Approved"
		if d.Scope == "session" {
			outcome = "✅ Approved for this session"
		}
	}
	if d != nil && d.By != "" {
		outcome += " (" + d.By + ")"
	}
	b.editStatus(chatID, msgID, fmt.Sprintf("🔐 %s: %s", a.Tool, outcome))
}

// handleCallback answers an inline-button press. It runs outside the chat
// lock: the agent run that asked for approval holds that lock while it waits.
func (b *Bot) handleCallback(cq *tgbotapi.CallbackQuery) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("callback panic: %v", r)
		}
	}()
	answer := func(text string) {
		if _, err := b.api.Request(tgbotapi.NewCallback(cq.ID, text)); err != nil {
			log.Printf("answer callback: %v", err)
		}
	}
	data := cq.Data
	if !strings.HasPrefix(data, approvalCallbackPrefix) || len(data) < len(approvalCallbackPrefix)+3 {
		answer("")
		return
	}
	rest := data[len(approvalCallbackPrefix):]
	verdict, id := rest[0], rest[2:]
	if cq.From == nil || !b.auth.IsAuthorized(cq.From.ID) {
		answer("You are not allowed to approve actions.")
		return
	}
	if cq.Message == nil || cq.Message.Chat == nil {
		answer("")
		return
	}
	chatID := cq.Message.Chat.ID
	// An approval belongs to the chat whose run asked for it: a button
	// forwarded (or guessed) into another chat must not decide it.
	info, err := b.agent.GetApproval(id)
	if err != nil {
		answer("Gateway error: " + err.Error())
		return
	}
	if info == nil {
		answer("Already decided or expired.")
		b.editStatus(chatID, cq.Message.MessageID, "🔐 Approval already decided or expired.")
		return
	}
	if info.TelegramChatID == nil || *info.TelegramChatID != chatID {
		answer("This approval belongs to another chat.")
		return
	}
	approved := verdict == 'y' || verdict == 's'
	scope := ""
	if verdict == 's' {
		scope = "session"
	}
	by := fmt.Sprintf("telegram:%d", cq.From.ID)
	found, err := b.agent.ResolveApproval(id, approved, by, scope)
	switch {
	case err != nil:
		answer("Gateway error: " + err.Error())
	case !found:
		answer("Already decided or expired.")
	default:
		b.auth.Touch(cq.From.ID)
		if approved {
			answer("Approved")
		} else {
			answer("Denied")
		}
		b.markApprovalResolved(chatID, cq.Message.MessageID, info, &ApprovalDecision{Approved: approved, By: by, Scope: scope})
	}
}
