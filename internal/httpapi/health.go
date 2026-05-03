package httpapi

import "net/http"

func (d *Deps) handleHealth(w http.ResponseWriter, _ *http.Request) {
	embedErr := d.Memory.EmbedHealthy()
	embedOK := embedErr == nil
	embedErrStr := ""
	if !embedOK {
		embedErrStr = embedErr.Error()
	}
	embedModel := d.Cfg.OllamaEmbedModel
	if d.Cfg.Provider == "openai" || d.Cfg.Provider == "openai-codex" || d.Cfg.Provider == "codex" {
		embedModel = d.Cfg.OpenAIEmbedModel
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           embedOK, // false when vector memory is broken
		"provider":     d.Cfg.Provider,
		"chat_base":    d.Chat.BaseURL(),
		"chat_model":   d.Chat.Model(),
		"skills_dir":   d.Cfg.SkillsDir,
		"scheduler_db": d.Cfg.SchedulerDB,
		"embed_model":  embedModel,
		"embed_ok":     embedOK,
		"embed_error":  embedErrStr,
	})
}
