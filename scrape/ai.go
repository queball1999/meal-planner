package scrape

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"goeat/llm"
)

// AIClient is the subset of llm.Generator the scrape package needs. Keeping it
// narrow lets tests supply a stub without an LLM provider.
type AIClient interface {
	Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error)
}

// Proposal is the result of auto-detecting a scrape config for a page (§6.7).
type Proposal struct {
	Selectors  Selectors          `json:"selectors"`
	Products   []ExtractedProduct `json:"products"`
	Mode       string             `json:"mode"`       // "auto" | "auto_ai"
	Confidence string             `json:"confidence"` // "high" | "medium" | "low" | "none"
	Notes      string             `json:"notes"`      // human-readable explanation
	Attempts   int                `json:"attempts"`   // LLM round-trips used
}

// SelectorsJSON renders the proposal's selectors for storage in
// scrape_configs.selectors_json.
func (p *Proposal) SelectorsJSON() string {
	b, err := json.Marshal(p.Selectors)
	if err != nil {
		return "{}"
	}
	return string(b)
}

const (
	// maxCondensedBytes caps how much page markup is sent to the LLM. Retail
	// search pages routinely exceed 1 MB; the condenser keeps the parts that
	// look like product tiles.
	maxCondensedBytes = 60_000

	// aiSelectorAttempts is how many times ProposeWithAI asks for selectors,
	// feeding the previous failure back in as a correction.
	aiSelectorAttempts = 2
)

// Detect proposes a scrape config for a page without using an LLM: it tries
// structured data (JSON-LD / microdata) and, failing that, a set of built-in
// selector heuristics common to retail markup.
func Detect(pageHTML string) *Proposal {
	if products := ExtractStructured(pageHTML, 5); len(products) > 0 {
		return &Proposal{
			Products:   products,
			Mode:       "auto",
			Confidence: "high",
			Notes:      "Prices come from the page's own structured data (JSON-LD / microdata) - no CSS selectors needed.",
		}
	}

	if sels, products := guessSelectors(pageHTML); len(products) > 0 {
		return &Proposal{
			Selectors:  sels,
			Products:   products,
			Mode:       "auto",
			Confidence: "medium",
			Notes:      "Matched a common retail markup pattern. Verify the sample prices below before saving.",
		}
	}

	return &Proposal{
		Mode:       "auto",
		Confidence: "none",
		Notes:      "No structured data and no known markup pattern matched. Try Auto + AI, or pick selectors by hand in Assisted mode.",
	}
}

// heuristicPatterns are (item, name, price) selector triples seen across major
// US grocery sites. Ordered most-specific first; the first triple that yields a
// priced product wins.
var heuristicPatterns = []Selectors{
	{
		Item:  SelectorSpec{CSS: `[data-testid="product-card"], [data-test="product-card"]`},
		Name:  SelectorSpec{CSS: `[data-testid="product-title"], [data-test="product-title"], h2, h3, a`},
		Price: SelectorSpec{CSS: `[data-testid*="price"], [data-test*="price"], [class*="price"]`},
	},
	{
		Item:  SelectorSpec{CSS: `[itemprop="itemListElement"], li[data-item-id], div[data-item-id]`},
		Name:  SelectorSpec{CSS: `[itemprop="name"], h2, h3, a`},
		Price: SelectorSpec{CSS: `[itemprop="price"], [class*="price"]`},
	},
	{
		Item:  SelectorSpec{CSS: `article[class*="product"], li[class*="product-"], div[class*="ProductCard"], div[class*="product-card"]`},
		Name:  SelectorSpec{CSS: `[class*="title"], [class*="name"], h2, h3, a`},
		Price: SelectorSpec{CSS: `[class*="price"], [class*="Price"]`},
	},
	{
		Item:  SelectorSpec{CSS: `[class*="product-tile"], [class*="ProductTile"], [class*="search-result"]`},
		Name:  SelectorSpec{CSS: `[class*="description"], [class*="title"], h2, h3, a`},
		Price: SelectorSpec{CSS: `[class*="price"]`},
	},
}

func guessSelectors(pageHTML string) (Selectors, []ExtractedProduct) {
	for _, pattern := range heuristicPatterns {
		products := ExtractWithSelectors(pageHTML, &pattern, 5)
		if priced(products) >= 1 {
			return pattern, products
		}
	}
	return Selectors{}, nil
}

func priced(products []ExtractedProduct) int {
	n := 0
	for _, p := range products {
		if p.Price > 0 {
			n++
		}
	}
	return n
}

// ProposeWithAI asks the LLM to read a condensed version of the page and name
// the CSS selectors for the product container, title, price, and pack size.
// Every proposal is *verified* by running the extractor with it; a proposal
// that yields no priced product is fed back to the model as a correction and
// retried. Only verified selectors are returned with a non-"none" confidence,
// so the operator is never asked to save selectors that were never proven to
// work on the page in front of them.
func ProposeWithAI(ctx context.Context, gen AIClient, pageHTML string) (*Proposal, error) {
	if gen == nil {
		return nil, fmt.Errorf("scrape: no LLM configured")
	}

	// Cheap paths first - never spend tokens on a page that parses itself.
	if p := Detect(pageHTML); p.Confidence != "none" {
		return p, nil
	}

	condensed := Condense(pageHTML, maxCondensedBytes)
	if strings.TrimSpace(condensed) == "" {
		return &Proposal{Mode: "auto_ai", Confidence: "none",
			Notes: "The page returned no usable markup - it is probably rendered entirely by JavaScript. Configure FLARESOLVERR_URL and retry."}, nil
	}

	var lastNote string
	for attempt := 1; attempt <= aiSelectorAttempts; attempt++ {
		prompt := aiSelectorPrompt(condensed, lastNote)
		res, err := gen.Generate(llm.WithPurpose(ctx, "scrape"), llm.GenerateRequest{
			System:            aiSelectorSystem,
			Prompt:            prompt,
			MaxTokens:         2048,
			SuppressReasoning: true,
		})
		if err != nil {
			return nil, fmt.Errorf("scrape: LLM call failed: %w", err)
		}

		sels, notes, perr := parseSelectorReply(res.Content)
		if perr != nil {
			lastNote = "Your previous reply was not valid JSON matching the schema: " + perr.Error()
			continue
		}

		products := ExtractWithSelectors(pageHTML, sels, 5)
		if priced(products) >= 1 {
			return &Proposal{
				Selectors:  *sels,
				Products:   products,
				Mode:       "auto_ai",
				Confidence: aiConfidence(products),
				Notes:      strings.TrimSpace("AI-detected selectors, verified against the live page. " + notes),
				Attempts:   attempt,
			}, nil
		}

		lastNote = fmt.Sprintf(
			"Your selectors matched %d product containers and %d usable prices on the real page, so they are wrong. "+
				"Pick selectors that exist verbatim in the markup below and make sure the price selector lands on the element whose text holds the dollar amount.",
			len(selectAllCount(pageHTML, sels.Item.CSS)), priced(products))
	}

	return &Proposal{
		Mode:       "auto_ai",
		Confidence: "none",
		Notes:      "The AI proposed selectors but none extracted a price from this page. Use Assisted mode to click the price element directly.",
		Attempts:   aiSelectorAttempts,
	}, nil
}

func aiConfidence(products []ExtractedProduct) string {
	switch n := priced(products); {
	case n >= 3:
		return "high"
	case n == 2:
		return "medium"
	default:
		return "low"
	}
}

func selectAllCount(pageHTML, selector string) []*html.Node {
	if selector == "" {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		return nil
	}
	return selectAll(doc, selector)
}

const aiSelectorSystem = `You are a web-scraping engineer. You are given a condensed excerpt of a grocery store's product search results page. Identify the CSS selectors that extract each product's name, price, and pack size.

Rules:
- Reply with ONE JSON object and nothing else. No prose, no markdown fences.
- Schema:
  {"item":{"css":"..."},"name":{"css":"..."},"price":{"css":"...","regex":""},"pack_size":{"css":""},"notes":"one sentence"}
- "item" is the repeating container for a single product. "name", "price" and "pack_size" are matched INSIDE that container.
- Use only classes, ids, attributes and tags that appear verbatim in the excerpt. Never invent a class name.
- Prefer stable attribute selectors (e.g. [data-testid="..."]) over hashed CSS class names.
- The price selector must land on the element whose text contains the dollar amount, not an ancestor that also contains other numbers.
- Add "attr":"content" to a field when the value lives in an attribute rather than text.
- Leave a field's css as "" when the page genuinely does not show it.`

func aiSelectorPrompt(condensed, correction string) string {
	var b strings.Builder
	if correction != "" {
		b.WriteString("CORRECTION - your previous answer failed verification.\n")
		b.WriteString(correction)
		b.WriteString("\n\n")
	}
	b.WriteString("Condensed page markup:\n\n")
	b.WriteString(condensed)
	return b.String()
}

var jsonObjectRE = regexp.MustCompile(`(?s)\{.*\}`)

// parseSelectorReply pulls the JSON object out of an LLM reply, tolerating
// markdown fences and surrounding chatter.
func parseSelectorReply(content string) (*Selectors, string, error) {
	raw := strings.TrimSpace(content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	if m := jsonObjectRE.FindString(raw); m != "" {
		raw = m
	}

	var payload struct {
		Item     SelectorSpec `json:"item"`
		Name     SelectorSpec `json:"name"`
		Price    SelectorSpec `json:"price"`
		PackSize SelectorSpec `json:"pack_size"`
		Notes    string       `json:"notes"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, "", err
	}

	sels := &Selectors{
		Item:     payload.Item,
		Name:     payload.Name,
		Price:    payload.Price,
		PackSize: payload.PackSize,
	}
	if sels.Price.Empty() && sels.Name.Empty() {
		return nil, "", fmt.Errorf("no name or price selector returned")
	}
	for _, spec := range []SelectorSpec{sels.Item, sels.Name, sels.Price, sels.PackSize} {
		if err := ValidateSelector(spec.CSS); err != nil {
			return nil, "", fmt.Errorf("selector %q is not valid CSS: %w", spec.CSS, err)
		}
	}
	return sels, payload.Notes, nil
}

// ── Direct AI extraction ─────────────────────────────────────────────────────

// ExtractWithAI asks the LLM to read prices straight off the page, bypassing
// selectors entirely. It is the last resort for pages whose markup is too
// unstable to pin down (hashed class names that change per deploy). Results
// are cached by the caller through price_cache, so a store costs roughly one
// LLM call per ingredient per cache window.
func ExtractWithAI(ctx context.Context, gen AIClient, pageHTML, term string) ([]ExtractedProduct, error) {
	if gen == nil {
		return nil, fmt.Errorf("scrape: no LLM configured")
	}
	condensed := Condense(pageHTML, maxCondensedBytes)
	if strings.TrimSpace(condensed) == "" {
		return nil, fmt.Errorf("scrape: page had no extractable markup")
	}

	res, err := gen.Generate(llm.WithPurpose(ctx, "scrape"), llm.GenerateRequest{
		System: `You read grocery search-result markup and report the products you can see.
Reply with ONE JSON object and nothing else:
{"products":[{"name":"...","price":0.00,"pack_size":"5 lb"}]}
Rules: price is a number in dollars for the shelf price of the whole package - never a unit price like "$0.41/oz". Omit products with no visible price. Return at most 5, most relevant first. If nothing is visible, return {"products":[]}.`,
		Prompt:            "Shopper searched for: " + term + "\n\nCondensed page markup:\n\n" + condensed,
		MaxTokens:         2048,
		SuppressReasoning: true,
	})
	if err != nil {
		return nil, fmt.Errorf("scrape: LLM call failed: %w", err)
	}

	raw := strings.TrimSpace(res.Content)
	if m := jsonObjectRE.FindString(raw); m != "" {
		raw = m
	}
	var payload struct {
		Products []struct {
			Name     string  `json:"name"`
			Price    float64 `json:"price"`
			PackSize string  `json:"pack_size"`
		} `json:"products"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("scrape: LLM reply was not valid JSON: %w", err)
	}

	var out []ExtractedProduct
	for _, p := range payload.Products {
		if p.Price <= 0 || p.Price > 1000 { // sanity bound: no grocery line item costs $1000
			continue
		}
		out = append(out, ExtractedProduct{
			Name:     strings.TrimSpace(p.Name),
			Price:    p.Price,
			PackSize: strings.TrimSpace(p.PackSize),
		})
	}
	return out, nil
}

// ── Page condensing ──────────────────────────────────────────────────────────

// dollarTextRE finds a price-looking string, used to locate product tiles.
var dollarTextRE = regexp.MustCompile(`\$\s?\d`)

// keptAttrs are the attributes worth showing the model: they are what stable
// selectors are built from. Everything else (style, srcset, event handlers,
// tracking payloads) is noise that crowds out real markup.
var keptAttrs = map[string]bool{
	"class": true, "id": true, "itemprop": true, "itemtype": true,
	"content": true, "aria-label": true, "role": true, "href": true,
}

// Condense reduces a page to the markup an LLM needs to write selectors: it
// drops scripts, styles, SVG, head content and non-selector attributes, then
// keeps only the subtrees that contain a dollar amount (the product tiles),
// budgeting maxBytes across them. Whole-page markup is far too large - and
// mostly navigation chrome - to send as-is.
func Condense(pageHTML string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = maxCondensedBytes
	}
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		return ""
	}

	// Prefer tiles: the smallest elements that contain a price and a name.
	tiles := priceTiles(doc)
	var b strings.Builder
	if len(tiles) > 0 {
		budget := maxBytes / min(len(tiles), 8)
		for i, tile := range tiles {
			if i >= 8 || b.Len() >= maxBytes {
				break
			}
			b.WriteString(renderCondensed(tile, budget))
			b.WriteString("\n\n")
		}
		return strings.TrimSpace(b.String())
	}

	// No price found anywhere - hand over the body so the model can at least
	// report that the page is a JS shell or a bot wall.
	if body := findBody(doc); body != nil {
		return renderCondensed(body, maxBytes)
	}
	return renderCondensed(doc, maxBytes)
}

// priceTiles finds candidate product containers: elements holding exactly one
// price whose parent holds more than one, i.e. the repeating unit of a grid.
func priceTiles(doc *html.Node) []*html.Node {
	var tiles []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && isSkippedTag(n.Data) {
			return
		}
		if n.Type == html.ElementNode && countPrices(n) == 1 && n.Parent != nil && countPrices(n.Parent) > 1 {
			tiles = append(tiles, n)
			return // do not descend: this is the tile
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if len(tiles) > 0 {
		return tiles
	}
	// Single-result pages: fall back to the smallest element with a price.
	var smallest *html.Node
	var walk2 func(*html.Node)
	walk2 = func(n *html.Node) {
		if n.Type == html.ElementNode && isSkippedTag(n.Data) {
			return
		}
		if n.Type == html.ElementNode && countPrices(n) >= 1 {
			smallest = n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk2(c)
		}
	}
	walk2(doc)
	if smallest != nil {
		// Climb one level so the model sees the tile, not just the price span.
		if smallest.Parent != nil && smallest.Parent.Type == html.ElementNode {
			smallest = smallest.Parent
		}
		return []*html.Node{smallest}
	}
	return nil
}

func countPrices(n *html.Node) int {
	count := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && isSkippedTag(n.Data) {
			return
		}
		if n.Type == html.TextNode && dollarTextRE.MatchString(n.Data) {
			count++
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return count
}

func isSkippedTag(tag string) bool {
	switch tag {
	case "script", "style", "svg", "noscript", "head", "template", "iframe", "path":
		return true
	}
	return false
}

// renderCondensed re-serializes a subtree with only selector-relevant
// attributes, stopping once budget bytes have been written.
func renderCondensed(n *html.Node, budget int) string {
	var b strings.Builder
	var walk func(*html.Node, int)
	walk = func(n *html.Node, depth int) {
		if b.Len() >= budget || depth > 14 {
			return
		}
		switch n.Type {
		case html.TextNode:
			if text := strings.Join(strings.Fields(n.Data), " "); text != "" {
				if len(text) > 120 {
					text = text[:120] + "…"
				}
				b.WriteString(text)
				b.WriteByte(' ')
			}
			return
		case html.ElementNode:
			if isSkippedTag(n.Data) {
				return
			}
			b.WriteByte('<')
			b.WriteString(n.Data)
			for _, a := range n.Attr {
				key := strings.ToLower(a.Key)
				if !keptAttrs[key] && !strings.HasPrefix(key, "data-") {
					continue
				}
				val := a.Val
				if len(val) > 80 {
					val = val[:80]
				}
				b.WriteString(" " + key + `="` + val + `"`)
			}
			b.WriteByte('>')
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c, depth+1)
			}
			b.WriteString("</" + n.Data + ">")
			return
		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c, depth)
			}
		}
	}
	walk(n, 0)
	out := b.String()
	if len(out) > budget {
		out = out[:budget]
	}
	return out
}

func findBody(doc *html.Node) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "body" {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// FormatPrice renders dollars for UI messages.
func FormatPrice(v float64) string { return "$" + strconv.FormatFloat(v, 'f', 2, 64) }
