package httpapi

import "net/http"

func (d *Deps) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"provider":     d.Cfg.Provider,
		"chat_base":    d.Chat.BaseURL(),
		"chat_model":   d.Chat.Model(),
		"skills_dir":   d.Cfg.SkillsDir,
		"scheduler_db": d.Cfg.SchedulerDB,
	})
}
