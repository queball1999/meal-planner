package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestChatStreamOutlivesWriteTimeout: an assistant turn runs for minutes, far
// past the server's WriteTimeout. The stream must still deliver an event sent
// after that deadline, and the request context must not be canceled by it -
// that cancellation is what aborted a plan correction halfway.
func TestChatStreamOutlivesWriteTimeout(t *testing.T) {
	canceled := make(chan bool, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send, ok := openChatStream(w)
		if !ok {
			return
		}
		send("step", map[string]string{"tool": "early"})
		time.Sleep(400 * time.Millisecond)
		send("done", map[string]string{"content": "late"})
		canceled <- r.Context().Err() != nil
	}))
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), `event: done`) || !strings.Contains(string(body), `"late"`) {
		t.Errorf("event sent after the write timeout never arrived; got %q", body)
	}
	if <-canceled {
		t.Error("request context was canceled by the write deadline")
	}
}
