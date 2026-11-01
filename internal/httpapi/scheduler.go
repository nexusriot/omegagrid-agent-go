package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
)

type createTaskRequest struct {
	Name                 string         `json:"name"`
	CronExpr             string         `json:"cron_expr"`
	Skill                string         `json:"skill"`
	Prompt               string         `json:"prompt"`
	Args                 map[string]any `json:"args"`
	NotifyTelegramChatID *int64         `json:"notify_telegram_chat_id"`
	OneShot              bool           `json:"one_shot"`
	Timezone             string         `json:"timezone"`
	MaxRetries           int            `json:"max_retries"`
	RetryDelaySec        int            `json:"retry_delay_sec"`
}

func (d *Deps) handleSchedulerCreate(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" || req.CronExpr == "" || (req.Skill == "" && req.Prompt == "") {
		writeError(w, http.StatusBadRequest, "name, cron_expr and skill (or prompt) are required")
		return
	}
	spec := scheduler.TaskSpec{
		Name: req.Name, CronExpr: req.CronExpr, Skill: req.Skill, Prompt: req.Prompt, Args: req.Args,
		NotifyTelegramChatID: req.NotifyTelegramChatID, OneShot: req.OneShot,
		Timezone: req.Timezone, MaxRetries: req.MaxRetries, RetryDelaySec: req.RetryDelaySec,
	}
	if err := scheduler.ValidateSpec(spec); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := d.Scheduler.CreateTask(spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (d *Deps) handleSchedulerList(w http.ResponseWriter, _ *http.Request) {
	tasks, err := d.Scheduler.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (d *Deps) handleSchedulerGet(w http.ResponseWriter, r *http.Request) {
	id, err := taskIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := d.Scheduler.Get(id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && task == nil) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Task not found"})
		return
	}
	if err != nil {
		// A real DB failure is not "not found"; reporting it as 404 hid it.
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (d *Deps) handleSchedulerEnable(w http.ResponseWriter, r *http.Request) {
	d.toggleEnabled(w, r, true)
}

func (d *Deps) handleSchedulerDisable(w http.ResponseWriter, r *http.Request) {
	d.toggleEnabled(w, r, false)
}

func (d *Deps) toggleEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, err := taskIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ok, err := d.Scheduler.SetEnabled(id, enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Task not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task_id": id, "enabled": enabled})
}

func (d *Deps) handleSchedulerDelete(w http.ResponseWriter, r *http.Request) {
	id, err := taskIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ok, err := d.Scheduler.Delete(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Task not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task_id": id})
}

func taskIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

// handleSchedulerRun runs a task now (POST /api/scheduler/tasks/{id}/run).
// The run is synchronous and bound to the request: an agent-prompt task can
// take as long as any agent query.
func (d *Deps) handleSchedulerRun(w http.ResponseWriter, r *http.Request) {
	if d.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "the scheduler runner is not available in this process")
		return
	}
	id, err := taskIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	run, err := d.Runner.RunNow(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Task not found"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleSchedulerRuns returns a task's run history, newest first.
func (d *Deps) handleSchedulerRuns(w http.ResponseWriter, r *http.Request) {
	id, err := taskIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := d.Scheduler.Get(id); errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Task not found"})
		return
	}
	runs, err := d.Scheduler.ListRuns(id, queryInt(r.URL.Query().Get("limit"), 50))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}
