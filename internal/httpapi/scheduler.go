package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
)

type createTaskRequest struct {
	Name                 string         `json:"name"`
	CronExpr             string         `json:"cron_expr"`
	Skill                string         `json:"skill"`
	Args                 map[string]any `json:"args"`
	NotifyTelegramChatID *int64         `json:"notify_telegram_chat_id"`
}

func (d *Deps) handleSchedulerCreate(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" || req.CronExpr == "" || req.Skill == "" {
		writeError(w, http.StatusBadRequest, "name, cron_expr and skill are required")
		return
	}
	if err := scheduler.ValidateCron(req.CronExpr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cron_expr: "+err.Error())
		return
	}
	task, err := d.Scheduler.Create(req.Name, req.CronExpr, req.Skill, req.Args, req.NotifyTelegramChatID)
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
	if err != nil || task == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Task not found"})
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
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Task not found"})
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
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Task not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task_id": id})
}

func taskIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}
