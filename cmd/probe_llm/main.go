// Command probe_llm exercises the AI price-estimate path against the live
// configured LLM, without going through the web app. Useful for confirming a
// backend actually answers short JSON prompts.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"goeat/config"
	"goeat/llm"
	"goeat/pricing"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	gen, err := llm.NewGenerator(cfg)
	if err != nil || gen == nil {
		fmt.Fprintln(os.Stderr, "no LLM configured:", err)
		os.Exit(1)
	}
	fmt.Printf("provider=%s model=%s\n\n", gen.ProviderName(), gen.ModelName())

	terms := os.Args[1:]
	if len(terms) == 0 {
		terms = []string{"eggs", "butter", "bread", "salt", "flour tortilla"}
	}

	p := pricing.NewAIEstimateProvider(gen, "30044")
	for _, term := range terms {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		start := time.Now()
		res, err := p.Lookup(ctx, term, 0, "30044")
		cancel()
		switch {
		case err != nil:
			fmt.Printf("%-18s ERROR   %v\n", term, err)
		case res == nil:
			fmt.Printf("%-18s NO ANSWER (%s)\n", term, time.Since(start).Round(time.Millisecond))
		default:
			fmt.Printf("%-18s $%-7.2f %s (pack %g)  %s\n", term,
				float64(res.PriceCents)/100, res.PurchaseUnit, res.PackSize,
				time.Since(start).Round(time.Millisecond))
		}
	}
}
