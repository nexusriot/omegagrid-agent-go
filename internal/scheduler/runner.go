package scheduler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
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

	stop chan struct{}
	wg   sync.WaitGroup
}

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

func (r *Runner) Stop() {
	close(r.stop)
	r.wg.Wait()
	log.Printf("scheduler stopped")
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
	tasks, err := r.store.ListEnabled()
	if err != nil {
		log.Printf("scheduler list failed: %v", err)
		return
	}
	for _, task := range tasks {
		if !Matches(task.CronExpr, now) {
			continue
		}
		// Skip tasks already executed inside this same minute window.
		if task.LastRunAt != nil {
			last := time.Unix(0, int64(*task.LastRunAt*1e9)).UTC().Truncate(time.Minute)
			if !last.Before(now) {
				continue
			}
		}
		r.runTask(task)
	}
}

func (r *Runner) runTask(t *Task) {
	log.Printf("scheduler running task #%d %q: %s(%v)", t.ID, t.Name, t.Skill, t.Args)
	var resultStr string
	res, err := r.exec(t.Skill, t.Args)
	if err != nil {
		errMap := map[string]string{"error": err.Error()}
		b, _ := json.Marshal(errMap)
		resultStr = string(b)
		log.Printf("task #%d failed: %v", t.ID, err)
	} else {
		b, _ := json.MarshalIndent(res, "", "  ")
		resultStr = string(b)
	}

	if err := r.store.UpdateLastRun(t.ID, resultStr); err != nil {
		log.Printf("scheduler update last_run failed: %v", err)
	}

	if t.NotifyTelegramChatID != nil && r.botToken != "" {
		preview := resultStr
		if len(preview) > 3900 {
			preview = preview[:3900]
		}
		msg := fmt.Sprintf("⏰ Scheduled: %s\n\n%s", t.Name, preview)
		sendTelegram(r.botToken, *t.NotifyTelegramChatID, msg)
	}
}

func sendTelegram(token string, chatID int64, text string) {
	if len(text) > 4096 {
		text = text[:4096]
	}
	body, _ := json.Marshal(map[string]any{"chat_id": chatID, "text": text})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("telegram send error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("telegram send failed: %d", resp.StatusCode)
	}
}
