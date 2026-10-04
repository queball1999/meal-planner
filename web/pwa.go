package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ── Installable web app + phone share target (phase 16) ──────────────────────
//
// On a server, Go Eat can be installed from the phone's browser, and then
// shows up in TikTok's / Instagram's / YouTube's Share sheet: the share opens
// /recipes/share, which fills in Import Recipe. Server-only - the desktop
// app's server listens on loopback, so a phone can never reach it, and the
// manifest isn't offered there (see shareAvailability).

// dockerGuideURL is where the import page sends desktop users who want the
// server-only features.
const dockerGuideURL = "https://github.com/queball1999/meal-planner#quick-start-docker"

// shareTargetState is how the import page's "Share from your phone" card
// renders: Mode "ready" | "desktop" | "needs_https".
type shareTargetState struct {
	Mode      string
	DockerURL string
}

func (s *Server) shareAvailability(r *http.Request) shareTargetState {
	st := shareTargetState{Mode: "ready", DockerURL: dockerGuideURL}
	switch {
	case s.cfg.Desktop:
		st.Mode = "desktop"
	case !s.secureOrigin(r):
		st.Mode = "needs_https"
	}
	return st
}

// secureOrigin reports whether this request reached Go Eat over HTTPS - the
// browser only installs a web app (and so only offers its share target) from
// a secure origin. X-Forwarded-Proto is trusted here because it only changes
// a hint on the import page, never access.
func (s *Server) secureOrigin(r *http.Request) bool {
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	public := publicOrigin(s.cfg.PublicBaseURL)
	return public != nil && public.Scheme == "https"
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Desktop {
		http.NotFound(w, r)
		return
	}
	type icon struct {
		Src   string `json:"src"`
		Sizes string `json:"sizes"`
		Type  string `json:"type"`
	}
	type param struct {
		Title string `json:"title"`
		Text  string `json:"text"`
		URL   string `json:"url"`
	}
	type shareTarget struct {
		Action string `json:"action"`
		Method string `json:"method"`
		Params param  `json:"params"`
	}
	manifest := struct {
		Name            string      `json:"name"`
		ShortName       string      `json:"short_name"`
		StartURL        string      `json:"start_url"`
		Scope           string      `json:"scope"`
		Display         string      `json:"display"`
		BackgroundColor string      `json:"background_color"`
		ThemeColor      string      `json:"theme_color"`
		Icons           []icon      `json:"icons"`
		ShareTarget     shareTarget `json:"share_target"`
	}{
		Name:            s.cfg.AppName,
		ShortName:       s.cfg.AppName,
		StartURL:        "/",
		Scope:           "/",
		Display:         "standalone",
		BackgroundColor: "#f5f5f5",
		ThemeColor:      "#2e7d32",
		Icons: []icon{
			{Src: "/static/img/app-icon-256.png", Sizes: "256x256", Type: "image/png"},
			{Src: "/static/img/app-icon-512.png", Sizes: "512x512", Type: "image/png"},
		},
		// GET: the share only fills in the import form; nothing is saved
		// until the person presses Import (a GET can't carry a CSRF token).
		ShareTarget: shareTarget{
			Action: "/recipes/share",
			Method: "GET",
			Params: param{Title: "title", Text: "text", URL: "url"},
		},
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	_ = json.NewEncoder(w).Encode(manifest)
}

// serviceWorkerJS passes every request straight to the network; on a
// navigation that fails it shows a short offline page. Android needs a
// service worker before it installs a web app.
const serviceWorkerJS = `self.addEventListener('install', function () { self.skipWaiting(); });
self.addEventListener('activate', function (e) { e.waitUntil(self.clients.claim()); });
self.addEventListener('fetch', function (e) {
    if (e.request.mode !== 'navigate') return;
    e.respondWith(fetch(e.request).catch(function () {
        return new Response('<!doctype html><meta name="viewport" content="width=device-width"><title>Offline</title>' +
            '<p style="font-family:sans-serif;padding:2rem">Can\'t reach Go Eat right now. Check your connection and try again.</p>',
            { status: 503, headers: { 'Content-Type': 'text/html; charset=utf-8' } });
    }));
});
`

// handleServiceWorker serves /sw.js from the root so its scope covers the
// whole app, /recipes/share included.
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Desktop {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(serviceWorkerJS))
}

// sharedURLRE finds the first link in shared text. TikTok and Instagram
// share "Check out this video! https://vm.tiktok.com/…" as text, not url.
var sharedURLRE = regexp.MustCompile(`https?://[^\s<>"']+`)

// sharedURL picks the link out of a share-target request: the url field
// first, then the first link in text, then in title.
func sharedURL(q url.Values) string {
	for _, field := range []string{"url", "text", "title"} {
		if m := sharedURLRE.FindString(q.Get(field)); m != "" {
			return strings.TrimRight(m, ".,;:!?)")
		}
	}
	return ""
}

// handleRecipeShare is the share target: it opens Import Recipe with the
// shared link filled in.
func (s *Server) handleRecipeShare(w http.ResponseWriter, r *http.Request) {
	link := sharedURL(r.URL.Query())
	if link == "" {
		s.setNotify(w, NotifyWarning, "That share didn't include a link. Copy the post's link and paste it below.")
		http.Redirect(w, r, "/recipes/import", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/recipes/import?url="+url.QueryEscape(link), http.StatusSeeOther)
}
