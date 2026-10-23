package scheduler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// SkillExecutor runs a registered skill by name.  Wired up by the gateway to
// the sidecar skill client (or any other implementation).
type SkillExecutor func(skillName string, args map[string]any) (any, error)

// Runner is a goroutine that wakes every checkInterval and runs any task whose
// cron expression matches the current minute.  Mirrors scheduler/runner.py.
type Runner struct {
	store         *Store
	exec          SkillExecutor
	botToken      string
	checkInterval time.Duration

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// lastMinute is the newest minute already evaluated. Ticks only ever look
	// at whole minutes, and a 60s ticker drifts: fire at :59.8 and the next
	// fire at :01:00.9 lands two minutes later, so the minute in between was
	// never examined and everything scheduled for it silently never ran.
	// Tracking the last minute lets a tick sweep every minute it missed —
	// which also covers a suspended host or a tick blocked by a slow task.
	lastMinute time.Time
}

// maxCatchupMinutes bounds the sweep so a machine that was asleep for a week
// does not replay a week of cron minutes on the next tick.
const maxCatchupMinutes = 60

func NewRunner(store *Store, exec SkillExecutor, botToken string, checkInterval time.Duration) *Runner {
	if checkInterval <= 0 {
		checkInterval = 60 * time.Second
	}
	return &Runner{
		store:         store,
		exec:          exec,
		botToken:      botToken,
		checkInterval: checkInterval,
		stop:          make(chan struct{}),
	}
}

func (r *Runner) Start() {
	r.wg.Add(1)
	go r.loop()
	log.Printf("scheduler started (interval=%s)", r.checkInterval)
}

// defaultStopTimeout bounds how long Stop waits for an in-flight task.
const defaultStopTimeout = 10 * time.Second

// Stop signals the loop to exit and waits for the current tick, bounded by
// defaultStopTimeout. Safe to call more than once and on a runner that was
// never started.
func (r *Runner) Stop() { r.StopTimeout(defaultStopTimeout) }

// StopTimeout is Stop with an explicit bound. A task blocked in a skill (a
// hung SSH or HTTP call) used to hold shutdown open indefinitely; past the
// deadline the loop goroutine is abandoned and the process exits around it.
// Reports whether the loop finished in time.
func (r *Runner) StopTimeout(d time.Duration) bool {
	r.stopOnce.Do(func() { close(r.stop) })
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Printf("scheduler stopped")
		return true
	case <-time.After(d):
		log.Printf("scheduler: still busy after %s, not waiting for the running task", d)
		return false
	}
}

func (r *Runner) loop() {
	defer r.wg.Done()
	t := time.NewTicker(r.checkInterval)
	defer t.Stop()
	r.tick()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.tick()
		}
	}
}

func (r *Runner) tick() {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("scheduler tick panic: %v", rec)
		}
	}()
	now := time.Now().UTC().Truncate(time.Minute)
	minutes := r.pendingMinutes(now)

	tasks, err := r.store.ListEnabled()
	if err != nil {
		// Leave lastMinute untouched so the next tick sweeps these minutes
		// again instead of silently dropping them.
		log.Printf("scheduler list failed: %v", err)
		return
	}
	r.lastMinute = now
	for _, task := range tasks {
		// A task matching several of the swept minutes still runs once: the
		// sweep exists to not lose a run, not to replay a backlog.
		if !matchesAny(task.CronExpr, minutes) {
			continue
		}
		// Claim the minute atomically: skips tasks already run in this window
		// and, when another process shares the database, lets only one of
		// them run it.
		won, err := r.store.ClaimRun(task.ID, now)
		if err != nil {
			log.Printf("scheduler claim failed for task #%d: %v", task.ID, err)
			continue
		}
		if !won {
			continue
		}
		r.runTask(task)
	}
}

// pendingMinutes returns every minute that still needs evaluating, oldest
// first: the current one plus any skipped since the previous tick.
func (r *Runner) pendingMinutes(now time.Time) []time.Time {
	if r.lastMinute.IsZero() || !r.lastMinute.Before(now) {
		return []time.Time{now}
	}
	gap := int(now.Sub(r.lastMinute) / time.Minute)
	if gap > maxCatchupMinutes {
		log.Printf("scheduler: %d minute(s) missed, only the last %d are checked", gap, maxCatchupMinutes)
		gap = maxCatchupMinutes
	}
	out := make([]time.Time, 0, gap)
	for i := gap - 1; i >= 0; i-- {
		out = append(out, now.Add(-time.Duration(i)*time.Minute))
	}
	return out
}

func matchesAny(cronExpr string, minutes []time.Time) bool {
	for _, m := range minutes {
		if Matches(cronExpr, m) {
			return true
		}
	}
	return false
}

func (r *Runner) runTask(t *Task) {
	// Args are deliberately not logged: they can carry credentials (an
	// http_request Authorization header, an SSH command with a password).
	log.Printf("scheduler running task #%d %q: skill=%s", t.ID, t.Name, t.Skill)
	var resultStr string
	res, err := r.safeExec(t)
	if err != nil {
		log.Printf("task #%d failed: %v", t.ID, err)
		if b, merr := json.Marshal(map[string]string{"error": err.Error()}); merr == nil {
			resultStr = string(b)
		} else {
			resultStr = `{"error":"` + err.Error() + `"}`
		}
	} else {
		if b, merr := json.MarshalIndent(res, "", "  "); merr == nil {
			resultStr = string(b)
		} else {
			log.Printf("task #%d marshal result failed: %v", t.ID, merr)
			resultStr = fmt.Sprintf("%v", res)
		}
	}

	if err := r.store.RecordResult(t.ID, resultStr); err != nil {
		log.Printf("scheduler record result failed: %v", err)
	}

	// One-shot tasks are disabled (not deleted) after their first run so the
	// last_result / run_count audit trail survives.
	if t.OneShot {
		if _, derr := r.store.SetEnabled(t.ID, false); derr != nil {
			log.Printf("scheduler one-shot disable failed for task #%d: %v", t.ID, derr)
		} else {
			log.Printf("scheduler one-shot task #%d %q disabled after run", t.ID, t.Name)
		}
	}

	if t.NotifyTelegramChatID != nil && r.botToken != "" {
		var msg string
		if reminderText := reminderMessage(t, err, res); reminderText != "" {
			// Plain reminder text reads better than a JSON blob.
			msg = "⏰ Reminder: " + reminderText
		} else {
			preview := truncateUTF8(resultStr, 3900)
			msg = fmt.Sprintf("⏰ Scheduled: %s\n\n%s", t.Name, preview)
		}
		sendTelegram(r.botToken, *t.NotifyTelegramChatID, msg)
	}
}

// safeExec runs one task's skill, converting a panic into a task error. Only
// the tick's own recover stood between a panicking skill and the process, and
// it fired after unwinding runTask — so one bad task both lost its own
// last_result and cancelled every task still queued behind it in that minute.
func (r *Runner) safeExec(t *Task) (res any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("scheduler task #%d %q: skill %q panicked: %v\n%s", t.ID, t.Name, t.Skill, rec, debug.Stack())
			res = nil
			err = fmt.Errorf("skill %q crashed: %v", t.Skill, rec)
		}
	}()
	return r.exec(t.Skill, t.Args)
}

// reminderMessage returns the bare reminder text for successful runs of the
// reminder skill, or "" when the generic JSON notification should be used.
func reminderMessage(t *Task, execErr error, res any) string {
	if t.Skill != "reminder" || execErr != nil {
		return ""
	}
	if m, ok := res.(map[string]any); ok {
		if s, ok := m["reminder"].(string); ok {
			return s
		}
	}
	return ""
}

// truncateUTF8 cuts s to at most n bytes and returns valid UTF-8; Telegram
// rejects messages containing invalid UTF-8, and so does SQLite's TEXT type for
// last_result. Trailing partial runes are trimmed and any stray invalid bytes
// already present in the payload (binary shell output, say) are dropped —
// scanning back until the whole prefix validated used to return "", wiping a
// task's stored result and its notification body.
func truncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) > n {
		s = s[:n]
	}
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "")
}

func sendTelegram(token string, chatID int64, text string) {
	text = truncateUTF8(text, 4096)
	body, err := json.Marshal(map[string]any{"chat_id": chatID, "text": text})
	if err != nil {
		log.Printf("telegram marshal error: %v", err)
		return
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("telegram send error: %s", redactTelegramErr(err, token))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("telegram send failed: %d", resp.StatusCode)
	}
}

// redactTelegramErr renders a send error without the bot token. Transport
// failures come back as *url.Error, whose message embeds the request URL —
// and Telegram puts the token in the path.
func redactTelegramErr(err error, token string) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = fmt.Errorf("%s api.telegram.org: %w", uerr.Op, uerr.Err)
	}
	msg := err.Error()
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "<redacted>")
	}
	return msg
}
