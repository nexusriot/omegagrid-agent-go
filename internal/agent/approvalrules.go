package agent

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Approval shortcuts: allow-rules and session grants.
//
// A gate that asks about every `df -h` gets switched off within a week. Two
// ways to approve without asking, both narrower than switching the gate off:
//
//   - Allow-rules (APPROVAL_ALLOW) pre-approve calls whose arguments match:
//     "shell_command: command=df( -h)?|uptime". Every named argument must
//     match its regex in full, and no argument may contain shell
//     metacharacters, so `uptime; rm -rf ~` never matches a rule written
//     for `uptime`. Rules also apply to unattended (scheduled) runs.
//   - A session grant: the approver picks "approve for this session", and
//     later calls to the same tool in the same session run without asking
//     until the grant expires (GrantTTL, default 12h).

// AllowRule pre-approves calls to Tool whose arguments match Args.
type AllowRule struct {
	Tool string // skill name, or a "prefix*" pattern
	Args []argMatcher
	Text string // the rule as written, for logs and the approval history
}

type argMatcher struct {
	Name string
	Re   *regexp.Regexp
}

// DefaultGrantTTL bounds how long "approve for this session" lasts.
const DefaultGrantTTL = 12 * time.Hour

// unsafeArgChars are refused in any argument a rule checks: they let a value
// that starts like an allowed command run something else in a shell.
const unsafeArgChars = ";&|`$<>\n\r\\(){}*?!~"

// ParseAllowRules reads APPROVAL_ALLOW: rules separated by ";" (write "\;"
// for a literal semicolon), each "TOOL: ARG=REGEX [&& ARG=REGEX ...]".
// Regexes are matched against the whole value, case-sensitively.
func ParseAllowRules(spec string) ([]AllowRule, error) {
	var out []AllowRule
	for _, raw := range splitUnescaped(spec, ';') {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		tool, cond, ok := strings.Cut(text, ":")
		tool = strings.TrimSpace(tool)
		if !ok || tool == "" || strings.ContainsAny(tool, " =") {
			return nil, fmt.Errorf("approval rule %q: want TOOL: ARG=REGEX", text)
		}
		r := AllowRule{Tool: tool, Text: text}
		for _, c := range strings.Split(cond, "&&") {
			name, re, ok := strings.Cut(strings.TrimSpace(c), "=")
			name = strings.TrimSpace(name)
			if !ok || name == "" {
				return nil, fmt.Errorf("approval rule %q: condition %q is not ARG=REGEX", text, strings.TrimSpace(c))
			}
			cre, err := regexp.Compile(`^(?:` + strings.TrimSpace(re) + `)$`)
			if err != nil {
				return nil, fmt.Errorf("approval rule %q: %v", text, err)
			}
			r.Args = append(r.Args, argMatcher{Name: name, Re: cre})
		}
		out = append(out, r)
	}
	return out, nil
}

func splitUnescaped(s string, sep byte) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == sep:
			cur.WriteByte(sep)
			i++
		case s[i] == sep:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(parts, cur.String())
}

func toolPatternMatch(pattern, name string) bool {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(name, p)
	}
	return pattern == name
}

// Match reports whether the rule pre-approves calling tool with args.
func (r AllowRule) Match(tool string, args map[string]any) bool {
	if !toolPatternMatch(r.Tool, tool) || len(r.Args) == 0 {
		return false
	}
	for _, m := range r.Args {
		v, ok := args[m.Name]
		if !ok || v == nil {
			return false
		}
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case float64, int, int64, bool:
			s = fmt.Sprint(x)
		default:
			return false // lists and objects never match a rule
		}
		if strings.ContainsAny(s, unsafeArgChars) || !m.Re.MatchString(s) {
			return false
		}
	}
	return true
}

type grantKey struct {
	session int
	tool    string
}

// grant records an "approve for this session" decision.
func (a *Approvals) grant(req ApprovalRequest) {
	if req.SessionID <= 0 {
		return
	}
	ttl := a.GrantTTL
	if ttl <= 0 {
		ttl = DefaultGrantTTL
	}
	a.mu.Lock()
	if a.grants == nil {
		a.grants = map[grantKey]time.Time{}
	}
	a.grants[grantKey{req.SessionID, req.Tool}] = time.Now().Add(ttl)
	a.mu.Unlock()
}

// Revoke drops every session grant for session (all sessions when 0).
func (a *Approvals) Revoke(session int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for k := range a.grants {
		if session == 0 || k.session == session {
			delete(a.grants, k)
			n++
		}
	}
	return n
}

// Grant is one active "approve for this session" grant.
type Grant struct {
	SessionID int       `json:"session_id"`
	Tool      string    `json:"tool"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Grants lists the active session grants.
func (a *Approvals) Grants() []Grant {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	out := []Grant{}
	for k, exp := range a.grants {
		if now.After(exp) {
			delete(a.grants, k)
			continue
		}
		out = append(out, Grant{SessionID: k.session, Tool: k.tool, ExpiresAt: exp.UTC()})
	}
	return out
}

// preapproved returns the decision for a call that needs nobody asked: a
// matching allow-rule or an unexpired session grant.
func (a *Approvals) preapproved(session int, tool string, args map[string]any) (ApprovalDecision, bool) {
	for _, r := range a.Rules {
		if r.Match(tool, args) {
			return ApprovalDecision{Approved: true, By: "rule", Reason: r.Text}, true
		}
	}
	if session <= 0 {
		return ApprovalDecision{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	k := grantKey{session, tool}
	if exp, ok := a.grants[k]; ok {
		if time.Now().Before(exp) {
			return ApprovalDecision{Approved: true, By: "session", Scope: ScopeSession,
				Reason: "approved for this session"}, true
		}
		delete(a.grants, k)
	}
	return ApprovalDecision{}, false
}
