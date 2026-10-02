// Command probe_video runs a video recipe import (phase 16) against the live
// configured LLM and the local tools, without saving anything: it prints what
// was read from the post, the transcript, and the recipe the model wrote.
//
//	go run ./cmd/probe_video https://www.tiktok.com/@user/video/123
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"goeat/config"
	"goeat/cryptbox"
	"goeat/db"
	"goeat/llm"
	"goeat/recipes"
	"goeat/settings"
	"goeat/video"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: probe_video <video URL>")
		os.Exit(2)
	}
	url := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		log.Fatal("config: ", err)
	}
	// Provider settings usually live in the database (Settings page), not
	// .env, so read them the same way the server does.
	store, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatal("db: ", err)
	}
	defer store.Close()
	settings.UseBox(cryptbox.New(cfg.SessionSecret))
	if err := settings.Apply(context.Background(), store, cfg, log.Printf); err != nil {
		log.Fatal("settings: ", err)
	}
	gen, err := llm.NewGenerator(cfg)
	if err != nil || gen == nil {
		log.Fatal("no LLM configured: ", err)
	}
	tools := video.Tools{Dir: cfg.ToolsDir}
	fmt.Printf("provider=%s model=%s tools=%s\n\n", gen.ProviderName(), gen.ModelName(), tools.Dir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	work, err := os.MkdirTemp("", "probe-video-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(work)

	start := time.Now()
	m, err := video.Fetch(ctx, tools, url, work)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("fetched in %s: %s by %s, %ds\ncaption: %s\ncomments: %q\n\n",
		time.Since(start).Round(time.Millisecond), m.Platform, m.Uploader, m.Duration, m.Caption, m.Comments)

	text := recipes.VideoText{Platform: m.Platform, Uploader: m.Uploader, Title: m.Title, Caption: m.Caption, Comments: m.Comments}
	if m.AudioPath != "" {
		start = time.Now()
		transcript, model, err := video.Transcribe(ctx, tools, m.AudioPath)
		if err != nil {
			fmt.Printf("transcribe: %v\n\n", err)
		} else {
			text.Transcript = transcript
			fmt.Printf("transcribed in %s with %s:\n%s\n\n", time.Since(start).Round(time.Millisecond), model, transcript)
		}
	}

	start = time.Now()
	r, err := recipes.StructureVideoRecipe(ctx, gen, text, nil)
	if err != nil {
		log.Fatal(err)
	}
	out, _ := json.MarshalIndent(r, "", "  ")
	fmt.Printf("structured in %s:\n%s\n", time.Since(start).Round(time.Millisecond), out)
}
