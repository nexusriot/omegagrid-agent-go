package scheduler

import (
	"bytes"
	"context"
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

// SkillExecutor runs a registered skill by name.  Wired up by bootstrap to
// the skill registry plus the native skills. ctx is cancelled when the runner
// stops, so a task blocked in a skill no longer outlives shutdown.
type SkillExecutor func(ctx context.Context, skillName string, args map[string]any) (any, error)

// AgentFunc answers a prompt task's prompt with a full agent run.
type AgentFunc func(ctx context.Context, prompt string) (answer string, err error)

// Runner is a goroutine that wakes every checkInterval and runs any task whose
// cron expression matches the current minute.  Mirrors scheduler/runner.py.
type Runner struct {
	store         *Store
	exec          SkillExecutor
	agent         AgentFunc
	botToken      string
	checkInterval time.Duration

	stop     chan struct{}
	stopOnce sync.Once
	// ctx is cancelled by Stop; every task runs under it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

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
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		store:         store,
		exec:          exec,
		botToken:      botToken,
		checkInterval: checkInterval,
		stop:          make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// SetAgent enables prompt tasks; without it they fail with an error.
func (r *Runner) SetAgent(fn AgentFunc) { r.agent = fn }

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
	r.stopOnce.Do(func() {
		close(r.stop)
		r.cancel()
	})
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
		// Do not consume a queued one-shot after shutdown cancelled the
		// preceding task. Leave its claim for the next process.
		if r.ctx.Err() != nil {
			return
		}
		// A task matching several of the swept minutes still runs once: the
		// sweep exists to not lose a run, not to replay a backlog.
		slot, ok := latestMatchIn(task.CronExpr, minutes, task.Location())
		if !ok {
			continue
		}
		// Claim the minute that matched, not the current one, atomically:
		// skips tasks already run in this window and, when another process
		// shares the database, lets only one of them run it. Claiming `now`
		// let a process whose ticker drifted past a slot re-run it after
		// another process had already claimed that very slot.
		won, err := r.store.ClaimRun(task.ID, slot)
		if err != nil {
			log.Printf("scheduler claim failed for task #%d: %v", task.ID, err)
			continue
		}
		if !won {
			continue
		}
		r.runTask(task)
	}
	r.runDueRetries(time.Now())
}

// runDueRetries runs every pending retry whose time has come. ClaimRetry is
// atomic, so a retry runs once even with several processes on one database.
func (r *Runner) runDueRetries(now time.Time) {
	due, err := r.store.DueRetries(now)
	if err != nil {
		log.Printf("scheduler: list retries failed: %v", err)
		return
	}
	for _, t := range due {
		if r.ctx.Err() != nil {
			return
		}
		if won, err := r.store.ClaimRetry(t.ID, *t.NextRetryAt); err != nil || !won {
			continue
		}
		r.execute(r.ctx, t, "retry", t.RetryAttempt)
	}
}

// RunNow runs a task immediately, outside its schedule, and returns the run.
// It does not move last_run_at (the cron schedule is untouched), does not
// disable a one-shot, and does not arm retries.
func (r *Runner) RunNow(ctx context.Context, id int64) (*Run, error) {
	t, err := r.store.Get(id)
	if err != nil {
		return nil, err
	}
	run := r.execute(ctx, t, "manual", 0)
	return &run, nil
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

// latestMatch returns the newest of minutes (oldest first) that cronExpr
// matches.
func latestMatch(cronExpr string, minutes []time.Time) (time.Time, bool) {
	return latestMatchIn(cronExpr, minutes, time.UTC)
}

// latestMatchIn evaluates the expression on the wall clock of loc. Across a
// DST change a wall-clock minute can occur twice (fall back: both fire, they
// are distinct instants) or not at all (spring forward: skipped).
func latestMatchIn(cronExpr string, minutes []time.Time, loc *time.Location) (time.Time, bool) {
	for i := len(minutes) - 1; i >= 0; i-- {
		if Matches(cronExpr, minutes[i].In(loc)) {
			return minutes[i], true
		}
	}
	return time.Time{}, false
}

func (r *Runner) runTask(t *Task) {
	r.execute(r.ctx, t, "cron", 0)
}

// execute runs t once, records the run, and handles the outcome: a failed
// cron/retry run arms the next retry while attempts remain; the Telegram
// notification goes out once the outcome is final.
func (r *Runner) execute(ctx context.Context, t *Task, trigger string, attempt int) Run {
	// Args are deliberately not logged: they can carry credentials (an
	// http_request Authorization header, an SSH command with a password).
	log.Printf("scheduler running task #%d %q: skill=%s trigger=%s attempt=%d", t.ID, t.Name, t.Skill, trigger, attempt)
	started := time.Now()
	var resultStr string
	res, err := r.safeExec(ctx, t)
	if err != nil {
		log.Printf("task #%d failed: %v", t.ID, err)
		if b, merr := json.Marshal(map[string]string{"error": err.Error()}); merr == nil {
			resultStr = string(b)
		} else {
			resultStr = `{"error":"` + err.Error() + `"}`
		}
	} else if s, ok := res.(string); ok && t.Prompt != "" {
		resultStr = s
	} else {
		if b, merr := json.MarshalIndent(res, "", "  "); merr == nil {
			resultStr = string(b)
		} else {
			log.Printf("task #%d marshal result failed: %v", t.ID, merr)
			resultStr = fmt.Sprintf("%v", res)
		}
	}
	run := Run{TaskID: t.ID, Trigger: trigger, Attempt: attempt,
		StartedAt: float64(started.UnixNano()) / 1e9, FinishedAt: float64(time.Now().UnixNano()) / 1e9,
		OK: err == nil, Result: resultStr}
	if id, rerr := r.store.RecordRun(run); rerr != nil {
		log.Printf("scheduler record run failed: %v", rerr)
	} else {
		run.ID = id
	}

	if err := r.store.RecordResult(t.ID, resultStr); err != nil {
		log.Printf("scheduler record result failed: %v", err)
	}
	if trigger == "manual" {
		return run
	}

	retrying := false
	if err != nil && attempt < t.MaxRetries {
		delay := time.Duration(t.RetryDelaySec) * time.Second << attempt
		if delay <= 0 || delay > 24*time.Hour {
			delay = 24 * time.Hour
		}
		if serr := r.store.ScheduleRetry(t.ID, attempt+1, time.Now().Add(delay)); serr != nil {
			log.Printf("scheduler: arming retry for task #%d failed: %v", t.ID, serr)
		} else {
			retrying = true
			log.Printf("scheduler: task #%d will retry (attempt %d of %d) in %s", t.ID, attempt+1, t.MaxRetries, delay)
		}
	} else if t.RetryAttempt > 0 || t.NextRetryAt != nil {
		_ = r.store.ClearRetry(t.ID)
	}

	// One-shot tasks are disabled (not deleted) after their first run so the
	// last_result / run_count audit trail survives. ClaimRun already did
	// this atomically; this covers a direct call.
	if t.OneShot && !retrying {
		if _, derr := r.store.SetEnabled(t.ID, false); derr != nil {
			log.Printf("scheduler one-shot disable failed for task #%d: %v", t.ID, derr)
		} else {
			log.Printf("scheduler one-shot task #%d %q disabled after run", t.ID, t.Name)
		}
	}

	if t.NotifyTelegramChatID != nil && r.botToken != "" && !retrying {
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
	return run
}

// safeExec runs one task's skill, converting a panic into a task error. Only
// the tick's own recover stood between a panicking skill and the process, and
// it fired after unwinding runTask — so one bad task both lost its own
// last_result and cancelled every task still queued behind it in that minute.
func (r *Runner) safeExec(ctx context.Context, t *Task) (res any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("scheduler task #%d %q: skill %q panicked: %v\n%s", t.ID, t.Name, t.Skill, rec, debug.Stack())
			res = nil
			err = fmt.Errorf("skill %q crashed: %v", t.Skill, rec)
		}
	}()
	if t.Prompt != "" {
		if r.agent == nil {
			return nil, fmt.Errorf("prompt tasks need the agent, which this process has not wired")
		}
		return r.agent(ctx, t.Prompt)
	}
	return r.exec(ctx, t.Skill, t.Args)
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

// telegramAPIBase is the Bot API origin; tests point it at a fake.
var telegramAPIBase = "https://api.telegram.org"

func sendTelegram(token string, chatID int64, text string) {
	text = truncateUTF8(text, 4096)
	body, err := json.Marshal(map[string]any{"chat_id": chatID, "text": text})
	if err != nil {
		log.Printf("telegram marshal error: %v", err)
		return
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, token)
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
