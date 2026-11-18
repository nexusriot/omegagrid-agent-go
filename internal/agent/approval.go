package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

// Approval gate.
//
// Some skills act on the world — run a shell command, write a file, send a
// message — and a model that misread the request (or a prompt-injected web
// page it fetched) should not be able to trigger them alone. A skill that
// requires approval pauses the run: the agent registers an ApprovalRequest,
// tells a streaming client with an "approval_required" event, and waits for a
// decision from the web UI, the Telegram bot, the CLI or POST
// /api/approvals/{id}. No decision before the timeout is a denial.

// ApprovalRequest is one paused tool call waiting for a human decision.
type ApprovalRequest struct {
	ID             string         `json:"id"`
	SessionID      int            `json:"session_id"`
	Step           int            `json:"step"`
	Tool           string         `json:"tool"`
	Args           map[string]any `json:"args"`
	Why            string         `json:"why,omitempty"`
	Namespace      string         `json:"namespace,omitempty"`
	TelegramChatID *int64         `json:"telegram_chat_id,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
}

// ApprovalDecision is the answer to an ApprovalRequest.
type ApprovalDecision struct {
	Approved bool   `json:"approved"`
	By       string `json:"by,omitempty"`     // "web", "telegram:<id>", "cli", "timeout", "policy"
	Reason   string `json:"reason,omitempty"` // optional note passed to the model on denial
	// Scope "session" on an approval also approves later calls to the same
	// tool in the same session (see Approvals.GrantTTL); "" / "once" does not.
	Scope string `json:"scope,omitempty"`
}

// ScopeSession is the ApprovalDecision.Scope that grants the whole session.
const ScopeSession = "session"

// ErrApprovalNotFound means the id is unknown, already decided or expired.
var ErrApprovalNotFound = errors.New("approval not found (already decided or expired)")

// DefaultApprovalTimeout is how long a run waits for a decision.
const DefaultApprovalTimeout = 5 * time.Minute

// Approvals is the in-process broker between paused runs and approvers.
type Approvals struct {
	// Timeout bounds the wait; <= 0 means DefaultApprovalTimeout.
	Timeout time.Duration
	// Prompt, when set, decides synchronously instead of waiting for an
	// external Resolve — the CLI's local mode asks on its terminal. ok=false
	// falls back to waiting.
	Prompt func(ctx context.Context, req ApprovalRequest) (d ApprovalDecision, ok bool)
	// Rules pre-approve calls whose arguments match (APPROVAL_ALLOW).
	Rules []AllowRule
	// GrantTTL bounds an "approve for this session" grant; <= 0 means
	// DefaultGrantTTL.
	GrantTTL time.Duration

	mu      sync.Mutex
	pending map[string]*pendingApproval
	grants  map[grantKey]time.Time
}

type pendingApproval struct {
	req ApprovalRequest
	ch  chan ApprovalDecision // buffered(1); written once under mu
}

func (a *Approvals) timeout() time.Duration {
	if a.Timeout > 0 {
		return a.Timeout
	}
	return DefaultApprovalTimeout
}

func newApprovalID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Open registers req (filling ID, CreatedAt, ExpiresAt) and returns it with a
// wait function that blocks for the decision. The split lets the caller tell
// clients about the request between registering it and blocking on it.
func (a *Approvals) Open(req ApprovalRequest) (ApprovalRequest, func(ctx context.Context) ApprovalDecision) {
	req.ID = newApprovalID()
	req.CreatedAt = time.Now().UTC()
	req.ExpiresAt = req.CreatedAt.Add(a.timeout())
	p := &pendingApproval{req: req, ch: make(chan ApprovalDecision, 1)}
	a.mu.Lock()
	if a.pending == nil {
		a.pending = map[string]*pendingApproval{}
	}
	a.pending[req.ID] = p
	a.mu.Unlock()

	wait := func(ctx context.Context) (d ApprovalDecision) {
		defer a.remove(req.ID)
		defer func() {
			if d.Approved && d.Scope == ScopeSession {
				a.grant(req)
			}
		}()
		if a.Prompt != nil {
			if d, ok := a.Prompt(ctx, req); ok {
				return d
			}
		}
		t := time.NewTimer(time.Until(req.ExpiresAt))
		defer t.Stop()
		select {
		case d := <-p.ch:
			return d
		case <-t.C:
			return ApprovalDecision{Approved: false, By: "timeout", Reason: "no decision before the approval timed out"}
		case <-ctx.Done():
			return ApprovalDecision{Approved: false, By: "cancelled", Reason: "the run was cancelled"}
		}
	}
	return req, wait
}

func (a *Approvals) remove(id string) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
}

// Resolve delivers a decision to a waiting run.
func (a *Approvals) Resolve(id string, d ApprovalDecision) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.pending[id]
	if !ok {
		return ErrApprovalNotFound
	}
	delete(a.pending, id)
	p.ch <- d
	return nil
}

// Get returns one pending request.
func (a *Approvals) Get(id string) (ApprovalRequest, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.pending[id]
	if !ok {
		return ApprovalRequest{}, false
	}
	return p.req, true
}

// List returns the pending requests, oldest first.
func (a *Approvals) List() []ApprovalRequest {
	a.mu.Lock()
	out := make([]ApprovalRequest, 0, len(a.pending))
	for _, p := range a.pending {
		out = append(out, p.req)
	}
	a.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// needsApproval reports whether calling name must be approved first.
func (s *Service) needsApproval(name string, entry Skill, args map[string]any) bool {
	if s.Approvals == nil {
		return false
	}
	if entry.Schema.RequiresApproval {
		return true
	}
	// Native skills are not in the registry, so the registry's pattern list
	// has not annotated their schema; argument-dependent skills (openapi)
	// decide per call.
	return s.Skills != nil && s.Skills.NeedsApprovalFor(name, args)
}

// awaitApproval pauses the call until it is approved or denied. Calls an
// allow-rule or a session grant covers go through without asking; unattended
// runs follow APPROVAL_UNATTENDED. Every decision lands in the approval log.
func (s *Service) awaitApproval(state *runState, c batchCall) ApprovalDecision {
	args := eventArgs(c.Name, c.Args)
	t0 := time.Now()
	d, ok := s.Approvals.preapproved(state.sid, c.Name, c.Args)
	switch {
	case ok:
		state.addDebug(fmt.Sprintf("[approval] %s pre-approved (%s: %s)", c.Name, d.By, d.Reason))
	case state.unattended && s.UnattendedApprove:
		d = ApprovalDecision{Approved: true, By: "policy", Reason: "APPROVAL_UNATTENDED=allow"}
	case state.unattended:
		d = ApprovalDecision{Approved: false, By: "policy",
			Reason: "this run has no human to approve it (scheduled task); APPROVAL_UNATTENDED=deny"}
	default:
		req, wait := s.Approvals.Open(ApprovalRequest{
			SessionID:      state.sid,
			Step:           c.Step,
			Tool:           c.Name,
			Args:           args,
			Why:            c.Why,
			Namespace:      state.namespace,
			TelegramChatID: state.telegramChatID,
		})
		if state.emit != nil {
			r := req
			state.emit(Event{Event: "approval_required", Step: c.Step, Tool: c.Name, Args: req.Args, Why: c.Why, Approval: &r})
		}
		d = wait(state.ctx)
		if state.emit != nil {
			state.emit(Event{Event: "approval_resolved", Step: c.Step, Tool: c.Name,
				Approval: &req, Decision: &d})
		}
	}
	waited := time.Since(t0)
	state.addApprovalWait(waited)
	if s.Memory != nil {
		_ = s.Memory.AddApproval(memory.ApprovalRecord{
			SessionID: state.sid,
			Step:      c.Step,
			Tool:      c.Name,
			Args:      args,
			Why:       c.Why,
			Approved:  d.Approved,
			By:        d.By,
			Reason:    d.Reason,
			Scope:     d.Scope,
			WaitMS:    waited.Milliseconds(),
		})
	}
	return d
}

// deniedResult is what the model sees for a call that was not approved.
func deniedResult(name string, d ApprovalDecision) map[string]any {
	msg := "The user did not approve running " + name + "."
	if d.By == "timeout" || d.By == "policy" || d.By == "cancelled" {
		msg = "Running " + name + " was not approved: " + d.Reason + "."
	} else if r := strings.TrimSpace(d.Reason); r != "" {
		msg += " Their note: " + strings.TrimRight(r, ".") + "."
	}
	return map[string]any{
		"error":    msg + " Do not retry it; tell the user it was not run and ask how to proceed.",
		"approval": "denied",
	}
}
