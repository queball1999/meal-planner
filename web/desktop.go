package web

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
)

// ── Desktop mode (GOEAT_DESKTOP=1, see desktop/ and spec phase 14) ──────────

// ListeningPrefix starts the one stdout line the Tauri shell waits for:
// "GOEAT_LISTENING http://127.0.0.1:<port>". Everything else Go Eat logs goes
// to stderr, so this line is unambiguous.
const ListeningPrefix = "GOEAT_LISTENING "

func announceListening(w io.Writer, addr net.Addr) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "%shttp://%s\n", ListeningPrefix, addr.String())
}

// desktopHostGuard answers 421 to any request whose Host isn't this server's
// own loopback address. The desktop server listens on 127.0.0.1, which every
// website open in the user's browser can also reach; with DNS rebinding a
// hostile page could read the responses too. Checking Host closes that (QSS
// security design §9.3). /health stays open for local diagnostics.
func desktopHostGuard(port int, next http.Handler) http.Handler {
	p := strconv.Itoa(port)
	allowed := map[string]bool{
		"127.0.0.1:" + p: true,
		"localhost:" + p: true,
		"[::1]:" + p:     true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] && r.URL.Path != "/health" {
			http.Error(w, "Misdirected Request", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
