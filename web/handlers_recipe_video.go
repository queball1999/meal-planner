package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"goeat/middleware"
	"goeat/plan"
	"goeat/recipes"
	"goeat/video"
)

// ── Video recipe import (phase 16) ────────────────────────────────────────────
//
// A TikTok / Reel / Short link pasted into Import Recipe runs as a background
// job - downloading, transcribing and asking the model takes 10-90 seconds -
// and the browser watches it on /recipes/import/video, the same SSE progress
// pattern as plan generation.

// videoImportTimeout bounds one whole import: download, transcription on a
// slow CPU, and the model call.
const videoImportTimeout = 10 * time.Minute

// startVideoImport checks a video import can run, starts it, and sends the
// browser to the progress page.
func (s *Server) startVideoImport(w http.ResponseWriter, r *http.Request, householdID int64, rawURL string) {
	gen := s.llmGen()
	if gen == nil {
		s.setNotify(w, NotifyDanger, "Importing from a video needs an AI provider. Set one up in Settings → AI Setup.")
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}
	if level, _ := s.videoTools.Capability(); level == video.CapNone {
		who := "Ask an admin to download them in Settings → AI Setup."
		if middleware.UserFromCtx(r).IsAdmin() {
			who = "Use Download now above, or Settings → AI Setup."
		}
		s.setNotify(w, NotifyDanger, "Importing from a video needs yt-dlp and FFmpeg, which aren't downloaded yet. "+who)
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}

	s.setVideoResult(householdID, nil)
	_, started := s.videoJobs.Start(context.Background(), householdID, func(j *plan.Job) {
		ctx, cancel := context.WithTimeout(context.Background(), videoImportTimeout)
		defer cancel()

		importer := recipes.VideoImporter{
			Store:       s.store,
			Gen:         gen,
			Tools:       s.videoTools,
			ImageDir:    s.imageDir,
			Progress:    j.EmitStatus,
			OnDelta:     j.EmitDelta,
			OnModelCall: j.EmitLLMStart,
		}
		id, err := importer.Import(ctx, householdID, rawURL)
		var final plan.JobEvent
		if err != nil {
			log.Printf("video import %s: %v", rawURL, err)
			j.Status, j.Error = plan.JobFailed, err.Error()
			final = plan.JobEvent{Type: "error", Message: videoImportMessage(err)}
		} else {
			j.Status = plan.JobDone
			final = plan.JobEvent{Type: "done", Message: fmt.Sprintf("/recipes/%d", id)}
		}
		s.setVideoResult(householdID, &final)
		j.Emit(final)
	})
	if !started {
		s.setNotify(w, NotifyInfo, "A video import is already running - showing its progress.")
	}
	http.Redirect(w, r, "/recipes/import/video", http.StatusSeeOther)
}

func (s *Server) handleVideoImportPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "recipe_import_video", nil)
}

// handleVideoImportStatus streams the household's video import as SSE:
// "status" lines, "llm_start"/"llm_delta" for the model's live reply, then
// "done" (data: the new recipe's URL) or "error" (data: what went wrong).
func (s *Server) handleVideoImportStatus(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	// The stream outlives the server's 30s write timeout (see
	// handlePlanGenerateStatus).
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if job := s.videoJobs.Get(hh.ID); job != nil {
		job.Subscribe(w)
		return
	}
	// The job already finished (a bad link fails in about a second, before
	// the browser has connected): replay how it ended.
	final := s.videoResult(hh.ID)
	if final == nil {
		final = &plan.JobEvent{Type: "error", Message: "No video import is running. Paste a link on the Import Recipe page."}
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", final.Type, final.Message)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// setVideoResult records (or, with nil, clears) how a household's last video
// import ended. In memory: it only has to outlive the gap between the job
// finishing and the progress page connecting.
func (s *Server) setVideoResult(householdID int64, e *plan.JobEvent) {
	s.videoMu.Lock()
	defer s.videoMu.Unlock()
	if e == nil {
		delete(s.videoLast, householdID)
		return
	}
	s.videoLast[householdID] = *e
}

func (s *Server) videoResult(householdID int64) *plan.JobEvent {
	s.videoMu.Lock()
	defer s.videoMu.Unlock()
	if e, ok := s.videoLast[householdID]; ok {
		return &e
	}
	return nil
}

// videoImportMessage is the error a person sees on the progress page:
// package prefixes stripped, and the two common failures explained.
func videoImportMessage(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("The import took longer than %d minutes and was stopped.", int(videoImportTimeout.Minutes()))
	case errors.Is(err, recipes.ErrNoRecipeInVideo):
		msg := strings.TrimPrefix(err.Error(), "recipes: ")
		return "Couldn't find a recipe in this video (" + msg + "). If the creator says it's at a link in their bio, import that page instead."
	}
	msg := err.Error()
	for _, p := range []string{"recipes: ", "video: "} {
		msg = strings.TrimPrefix(msg, p)
	}
	return msg
}
