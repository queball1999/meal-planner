package web

import (
	"net/http"
)

type settingsPageData struct {
	HasLLM       bool
	Provider     string
	Model        string
	WeekStartDay string
	ImageDir     string
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	model := s.cfg.LLMModel
	if model == "" {
		model = s.cfg.AnthropicModel
	}

	s.render(w, r, "settings", settingsPageData{
		HasLLM:       s.gen != nil,
		Provider:     s.cfg.Provider,
		Model:        model,
		WeekStartDay: s.cfg.WeekStartDay,
		ImageDir:     s.cfg.RecipeImageDir,
	})
}
