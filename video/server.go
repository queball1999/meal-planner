package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A whisper.cpp server (whisper-server) transcribes instead of a local
// whisper-cli when Tools.ServerURL is set. That's how the Docker install gets
// speech-to-text - the Alpine app image can't run whisper-cli, so the
// `whisper` sidecar container does it - and lets a desktop or plain-binary
// install use a whisper server elsewhere on the network.
//
// Endpoints used: POST /inference (multipart "file"), GET /health
// ({"status":"ok"}, or 503 while a model loads), POST /load (multipart
// "model" = a path on the server's own filesystem).

// serverClient has no overall timeout: a long video on a slow CPU can take
// minutes. Calls are bounded by their context instead.
var serverClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: 10 * time.Minute,
}}

// maxServerResponse bounds what is read back from the server.
const maxServerResponse = 32 << 20

// serverBase validates and normalizes a whisper server URL.
func serverBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("whisper server URL %q must look like http://host:port", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// transcribeServer sends wavPath to the whisper server and returns the text.
func transcribeServer(ctx context.Context, t Tools, wavPath string) (text, model string, err error) {
	base, err := serverBase(t.ServerURL)
	if err != nil {
		return "", "", err
	}
	// The server knows which model it loaded; the shared models folder (the
	// Docker setup) tells us its name, for the language choice and the log.
	_, model, _ = t.ModelPath()
	lang := "auto"
	if EnglishOnly(model) {
		lang = "en"
	}

	f, err := os.Open(wavPath)
	if err != nil {
		return "", model, err
	}
	defer f.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return "", model, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", model, err
	}
	for k, v := range map[string]string{"response_format": "json", "temperature": "0.0", "language": lang} {
		if err := mw.WriteField(k, v); err != nil {
			return "", model, err
		}
	}
	if err := mw.Close(); err != nil {
		return "", model, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/inference", &body)
	if err != nil {
		return "", model, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := serverClient.Do(req)
	if err != nil {
		return "", model, fmt.Errorf("whisper server at %s: %w", base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxServerResponse))
	if err != nil {
		return "", model, err
	}
	var out struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		msg := out.Error
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return "", model, fmt.Errorf("whisper server: %s (%s)", msg, resp.Status)
	}
	if model == "" {
		model = "server"
	}
	return strings.Join(strings.Fields(out.Text), " "), model, nil
}

// ServerHealth asks the whisper server whether it is up with a model loaded.
func ServerHealth(ctx context.Context, rawURL string) error {
	base, err := serverBase(rawURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := serverClient.Do(req)
	if err != nil {
		return fmt.Errorf("not reachable at %s", base)
	}
	defer resp.Body.Close()
	var out struct {
		Status string `json:"status"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out)
	if resp.StatusCode == http.StatusOK && out.Status == "ok" {
		return nil
	}
	if out.Status != "" {
		return fmt.Errorf("server says %q", out.Status)
	}
	return fmt.Errorf("server answered %s", resp.Status)
}

// healthCache keeps the last health probe per URL for a few seconds, so a
// page that asks Capability on every load doesn't wait on the network each
// time.
var healthCache = struct {
	sync.Mutex
	m map[string]healthEntry
}{m: map[string]healthEntry{}}

type healthEntry struct {
	err error
	at  time.Time
}

const healthTTL = 10 * time.Second

func cachedServerHealth(rawURL string) error {
	healthCache.Lock()
	e, ok := healthCache.m[rawURL]
	healthCache.Unlock()
	if ok && time.Since(e.at) < healthTTL {
		return e.err
	}
	err := ServerHealth(context.Background(), rawURL)
	healthCache.Lock()
	healthCache.m[rawURL] = healthEntry{err: err, at: time.Now()}
	healthCache.Unlock()
	return err
}

func forgetServerHealth(rawURL string) {
	healthCache.Lock()
	delete(healthCache.m, rawURL)
	healthCache.Unlock()
}

// loadServerModel tells the whisper server to switch to the model at path.
// The path is on the server's filesystem: in the Docker setup both
// containers mount the models volume at the same place, so the app's own
// path works.
func loadServerModel(ctx context.Context, rawURL, path string) error {
	base, err := serverBase(rawURL)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("model", path); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/load", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := serverClient.Do(req)
	if err != nil {
		return fmt.Errorf("whisper server at %s: %w", base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	forgetServerHealth(rawURL)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("whisper server couldn't load %s: %s", filepath.Base(path), strings.TrimSpace(string(raw)))
	}
	return nil
}

// UseModel makes an installed model the one transcription uses and, with a
// whisper server, switches the running server to it. A server that is
// down isn't an error: the Docker sidecar reads the choice when it starts.
//
// /load is only sent when the models folder is shared with the server
// (WHISPER_MODELS_DIR, the Docker setup), because it takes a path on the
// server's own filesystem - and whisper-server has a bug where a /load for a
// path it can't find leaves it reporting "loading model" until restarted.
func (t Tools) UseModel(ctx context.Context, name string) error {
	if err := t.SetActiveModel(name); err != nil {
		return err
	}
	if t.ServerURL == "" || t.ModelsPath == "" || cachedServerHealth(t.ServerURL) != nil {
		return nil
	}
	path, _, err := t.ModelPath()
	if err != nil {
		return err
	}
	return loadServerModel(ctx, t.ServerURL, path)
}

// waitForServer polls the server's health until it answers ok or ctx ends.
// Used after a model download: the Docker sidecar only starts once a model
// exists, a few seconds later.
func waitForServer(ctx context.Context, rawURL string, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		err := ServerHealth(ctx, rawURL)
		if err == nil || time.Now().After(deadline) {
			forgetServerHealth(rawURL)
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
