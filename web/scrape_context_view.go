package web

import (
	"sort"
	"strings"

	"goeat/scrape"
)

// The store-context form is three plain textareas rather than a JSON blob, so
// these render a stored context back into them. They take the raw
// context_json because that is what the page templates already carry.

// storeContextCookies renders the store's cookies as NAME=value lines.
func storeContextCookies(raw string) string {
	sc := scrape.ParseStoreContext(raw)
	names := make([]string, 0, len(sc.Cookies))
	for name := range sc.Cookies {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(sc.Cookies[name])
		b.WriteByte('\n')
	}
	return b.String()
}

// storeContextPrewarm renders the pre-warm URLs, one per line.
func storeContextPrewarm(raw string) string {
	sc := scrape.ParseStoreContext(raw)
	if len(sc.Prewarm) == 0 {
		return ""
	}
	return strings.Join(sc.Prewarm, "\n") + "\n"
}

// storeContextWaitFor renders the selector the render waits for.
func storeContextWaitFor(raw string) string {
	return scrape.ParseStoreContext(raw).WaitFor
}
