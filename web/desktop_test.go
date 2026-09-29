package web

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"goeat/config"
	"goeat/db"
)

// TestDesktopRunAnnouncesAndGuardsHost boots a real desktop-mode server on
// 127.0.0.1:0 and checks the contract the Tauri shell relies on: one
// GOEAT_LISTENING line with the real port, loopback Hosts served, anything
// else (DNS rebinding) refused with 421, /health always open.
func TestDesktopRunAnnouncesAndGuardsHost(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AppName: "test", SessionSecret: strings.Repeat("k", 32), SessionTTLHours: 1,
		AutoPlanHour: -1, ListenAddr: "127.0.0.1:0", Desktop: true}
	s := NewServer(cfg, store, nil, "test", nil)

	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, pw) }()

	lineCh := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(pr).ReadString('\n')
		lineCh <- line
	}()
	var base string
	select {
	case line := <-lineCh:
		if !strings.HasPrefix(line, ListeningPrefix+"http://127.0.0.1:") {
			t.Fatalf("announce line = %q", line)
		}
		base = strings.TrimSpace(strings.TrimPrefix(line, ListeningPrefix))
	case <-time.After(5 * time.Second):
		t.Fatal("no announce line within 5s")
	}

	get := func(path, host string) int {
		req, _ := http.NewRequest("GET", base+path, nil)
		if host != "" {
			req.Host = host
		}
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatalf("GET %s (Host %q): %v", path, host, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	port := base[strings.LastIndex(base, ":")+1:]

	if code := get("/auth/login", ""); code == http.StatusMisdirectedRequest {
		t.Errorf("own 127.0.0.1 Host refused")
	}
	if code := get("/auth/login", "localhost:"+port); code == http.StatusMisdirectedRequest {
		t.Errorf("localhost Host refused")
	}
	if code := get("/auth/login", "evil.example:"+port); code != http.StatusMisdirectedRequest {
		t.Errorf("rebinding Host: %d, want 421", code)
	}
	if code := get("/auth/login", "127.0.0.1:1"); code != http.StatusMisdirectedRequest {
		t.Errorf("wrong-port Host: %d, want 421", code)
	}
	if code := get("/health", "evil.example"); code != http.StatusOK {
		t.Errorf("/health with foreign Host: %d, want 200", code)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v after cancel", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run didn't return after cancel")
	}
}
