// Command probe fetches one URL through the configured scraping pipeline and
// prints what came back. Development tool: it is how the renderer chain is
// checked against a real store without going through the web UI.
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
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	cfg := scrape.RenderConfig{
		Backend:         scrape.RendererAuto,
		URL:             "http://localhost:3900",
		Token:           "local-dev-token-change-before-production-use",
		FlareSolverrURL: "http://localhost:8191",
	}

	res, err := scrape.FetchSmart(ctx, os.Args[1], cfg)
	if err != nil {
		fmt.Println("ERR:", err)
		return
	}

	fmt.Println("backend:", res.Backend, "| http:", res.StatusCode, "| len:", len(res.HTML))
	fmt.Println("challenge:", res.Challenge)

	prices := regexp.MustCompile(`\$\d+\.\d{2}`).FindAllString(res.HTML, -1)
	fmt.Println("price-like strings:", len(prices))
	if len(prices) > 8 {
		prices = prices[:8]
	}
	fmt.Println(" ", prices)

	counts := map[string]int{}
	for _, m := range regexp.MustCompile(`data-qa="([^"]+)"`).FindAllStringSubmatch(res.HTML, -1) {
		counts[m[1]]++
	}
	fmt.Println("distinct data-qa attrs:", len(counts))
	shown := 0
	for k, c := range counts {
		fmt.Printf("  %-42s x%d\n", k, c)
		if shown++; shown >= 20 {
			break
		}
	}

	for _, needle := range []string{"prd-itm-prc", "product-comp-v1", "add to cart", "sign in"} {
		fmt.Printf("  %-24s %v\n", needle, strings.Contains(strings.ToLower(res.HTML), needle))
	}
}
