// Command probe_clearance forces the FlareSolverr -> Browserless clearance
// handoff against a real store, bypassing the ChallengeReason gate that
// normally decides whether the handoff runs. It is the direct answer to
// "does the vetted-visitor replay actually get past the wall?"
package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"goeat/scrape"
)

func main() {
	target := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	flareURL := "http://localhost:8191"
	bl := scrape.Renderer{
		Backend: scrape.RendererBrowserless,
		URL:     "http://localhost:3900",
		Token:   "local-dev-token-change-before-production-use",
	}

	fmt.Println("== step 1: FlareSolverr clearance ==")
	cl, err := scrape.FlareClearance(ctx, flareURL, target)
	if err != nil {
		fmt.Println("clearance ERR:", err)
		return
	}
	fmt.Printf("got %d cookies, UA=%q\n", len(cl.Cookies), cl.UserAgent)
	for _, c := range cl.Cookies {
		fmt.Printf("  cookie %s = %s (domain=%s)\n", c.Name, truncate(c.Value, 40), c.Domain)
	}

	fmt.Println("\n== step 2: Browserless render WITH clearance ==")
	res, err := scrape.RenderViaWith(ctx, bl, target, cl)
	if err != nil {
		fmt.Println("render ERR:", err)
		return
	}
	fmt.Printf("http=%d len=%d\n", res.StatusCode, len(res.HTML))

	prices := regexp.MustCompile(`\$\d+\.\d{2}`).FindAllString(res.HTML, -1)
	fmt.Println("price-like strings:", len(prices))
	if len(prices) > 8 {
		prices = prices[:8]
	}
	fmt.Println(" ", prices)

	for _, needle := range []string{"prd-itm-prc", "product-comp-v1", "add to cart", "just a moment", "incapsula", "access denied"} {
		fmt.Printf("  %-24s %v\n", needle, strings.Contains(strings.ToLower(res.HTML), needle))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
