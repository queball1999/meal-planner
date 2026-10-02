// Package video reads short recipe videos (TikTok, Instagram Reels, YouTube
// Shorts) into text a model can turn into a recipe (phase 16): the caption,
// the creator's own comments, and a transcript of the audio.
//
// It shells out to three local tools - yt-dlp to read the post, ffmpeg to
// convert its audio, whisper.cpp to transcribe it - found in TOOLS_DIR first,
// then on PATH (see Tools). Nothing here talks to an AI provider; turning the
// text into a recipe is recipes.ImportVideo's job.
package video

import (
	"net/url"
	"strings"
)

// IsVideoURL reports whether rawURL is a post on one of the video platforms
// video imports support. It is also the security boundary for Fetch: yt-dlp
// can fetch almost any URL, so only these hosts are ever handed to it.
func IsVideoURL(rawURL string) bool {
	return Platform(rawURL) != ""
}

// Host is rawURL's hostname without "www." / "m." ("tiktok.com"), used as a
// recipe's source site.
func Host(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	return strings.TrimPrefix(host, "m.")
}

// Platform names the video platform rawURL belongs to ("TikTok",
// "Instagram", "YouTube"), or "" when it isn't a supported video link.
func Platform(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return ""
	}
	host := Host(rawURL)
	path := strings.ToLower(u.Path)
	switch host {
	case "tiktok.com":
		if strings.Contains(path, "/video/") || strings.HasPrefix(path, "/t/") {
			return "TikTok"
		}
	case "vm.tiktok.com", "vt.tiktok.com":
		if len(path) > 1 {
			return "TikTok"
		}
	case "instagram.com":
		if strings.HasPrefix(path, "/reel/") || strings.HasPrefix(path, "/reels/") || strings.HasPrefix(path, "/p/") {
			return "Instagram"
		}
	case "youtube.com":
		if strings.HasPrefix(path, "/shorts/") || (path == "/watch" && u.Query().Get("v") != "") {
			return "YouTube"
		}
	case "youtu.be":
		if len(path) > 1 {
			return "YouTube"
		}
	}
	return ""
}
