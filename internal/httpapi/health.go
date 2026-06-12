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
	switch d.Cfg.Provider {
	case "openai", "openai-codex", "codex":
		embedModel = d.Cfg.OpenAIEmbedModel
	case "digitalocean", "do":
		embedModel = d.Cfg.DigitalOceanEmbedModel
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
