package web

import (
	"errors"
	"net/http"
	"strings"

	"goeat/video"
)

// ── Video import tools (phase 16) ─────────────────────────────────────────────
//
// yt-dlp, ffmpeg, whisper.cpp and a Whisper model are not shipped with Go
// Eat; an admin downloads them from Settings → AI Setup (or the
// "Download now" banner on Import Recipe). Every endpoint answers with the
// installer's Snapshot so the page can redraw from one shape.

// handleVideoToolsStatus: GET /settings/video-tools.
func (s *Server) handleVideoToolsStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.videoInstaller.Snapshot())
}

// handleVideoToolsInstall: POST /settings/video-tools/install.
// Form: component=<id> (repeatable) or missing=1 for everything not yet
// installed; model=<name> for the Whisper model.
func (s *Server) handleVideoToolsInstall(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		model = video.DefaultModel
	}
	components := r.Form["component"]
	if r.FormValue("missing") == "1" {
		_, components = s.videoTools().Capability()
	}
	if len(components) == 0 {
		writeJSON(w, http.StatusOK, s.videoInstaller.Snapshot())
		return
	}
	if err := s.videoInstaller.Start(components, model); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, video.ErrInstallRunning) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, s.videoInstaller.Snapshot())
}

// handleVideoToolsVerify: POST /settings/video-tools/verify - runs every
// tool and answers once the checks are done (a few seconds).
func (s *Server) handleVideoToolsVerify(w http.ResponseWriter, r *http.Request) {
	s.videoInstaller.Verify(r.Context())
	writeJSON(w, http.StatusOK, s.videoInstaller.Snapshot())
}

// handleVideoToolsModel: POST /settings/video-tools/model - name=<model>
// makes an installed model the one transcription uses.
func (s *Server) handleVideoToolsModel(w http.ResponseWriter, r *http.Request) {
	if err := s.videoTools().UseModel(r.Context(), r.FormValue("name")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.videoInstaller.Snapshot())
}

// handleVideoToolsRemove: POST /settings/video-tools/remove -
// component=<id>, or model=<name> to delete one model. Only removes what
// the installer downloaded, never a system install.
func (s *Server) handleVideoToolsRemove(w http.ResponseWriter, r *http.Request) {
	if s.videoInstaller.Snapshot().Running {
		writeJSON(w, http.StatusConflict, map[string]string{"error": video.ErrInstallRunning.Error()})
		return
	}
	var err error
	if m := r.FormValue("model"); m != "" {
		err = s.videoTools().RemoveModel(m)
	} else {
		err = s.videoTools().Remove(r.FormValue("component"))
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.videoInstaller.Snapshot())
}

// videoImportState is what the Import Recipe page needs to explain what a
// video link will do here.
type videoImportState struct {
	HasLLM     bool
	Capability string   // video.CapNone | CapCaption | CapFull
	Missing    []string // component ids
	CanInstall bool     // every missing component has a download for this machine
	Running    bool     // an install is in progress
	// ServerURL is the whisper.cpp server doing speech-to-text, "" for local.
	ServerURL string
	// NeedsModel: no speech model downloaded yet (with a server, the Docker
	// sidecar is waiting for one).
	NeedsModel bool
	// DownloadMB is roughly what "Download now" fetches.
	DownloadMB int64
}

func (s *Server) videoImportState() videoImportState {
	tools := s.videoTools()
	level, missing := tools.Capability()
	st := videoImportState{
		HasLLM:     s.llmGen() != nil,
		Capability: level,
		Missing:    missing,
		CanInstall: tools.Dir != "",
		Running:    s.videoInstaller.Snapshot().Running,
		ServerURL:  tools.ServerURL,
	}
	var size int64
	installable := 0
	for _, c := range missing {
		st.NeedsModel = st.NeedsModel || c == video.CompModel
		if c == video.CompWhisper && tools.ServerURL != "" {
			continue // the server, not a download
		}
		installable++
		st.CanInstall = st.CanInstall && video.CanInstall(c)
		size += video.DownloadSize(c, video.DefaultModel)
	}
	st.CanInstall = st.CanInstall && installable > 0
	st.DownloadMB = (size + 1<<20 - 1) >> 20
	return st
}
