package httpapi

import "net/http"

func (d *Deps) handleHealth(w http.ResponseWriter, _ *http.Request) {
	embedErr := d.Memory.EmbedHealthy()
	embedOK := embedErr == nil
	embedErrStr := ""
	if !embedOK {
		embedErrStr = embedErr.Error()
	}
	// Mirrors memory.buildEmbeddings: EMBED_PROVIDER wins, else the chat provider.
	embedProvider := d.Cfg.EmbedProvider
	if embedProvider == "" {
		embedProvider = d.Cfg.Provider
	}
	embedModel := d.Cfg.OllamaEmbedModel
	switch embedProvider {
	case "openai", "openai-codex", "codex":
		embedModel = d.Cfg.OpenAIEmbedModel
	case "digitalocean", "do":
		embedModel = d.Cfg.DigitalOceanEmbedModel
	default:
		// Everything else — including opencode, which serves no embeddings —
		// falls back to Ollama, so report the backend actually in use rather
		// than the chat provider's name.
		embedProvider = "ollama"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             embedOK, // false when vector memory is broken
		"provider":       d.Cfg.Provider,
		"chat_base":      d.Chat.BaseURL(),
		"chat_model":     d.Chat.Model(),
		"skills_dir":     d.Cfg.SkillsDir,
		"scheduler_db":   d.Cfg.SchedulerDB,
		"embed_model":    embedModel,
		"embed_provider": embedProvider,
		"embed_ok":       embedOK,
		"embed_error":    embedErrStr,
	})
}
