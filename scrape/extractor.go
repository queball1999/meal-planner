package scrape

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// ExtractedProduct is one price result pulled from a scraped page.
type ExtractedProduct struct {
	Name     string
	Price    float64 // dollars
	PackSize string  // raw string e.g. "5 lb"
}

// Selectors is the parsed form of a scrape_config's selectors_json field.
type Selectors struct {
	Name      SelectorSpec `json:"name"`
	Price     SelectorSpec `json:"price"`
	PackSize  SelectorSpec `json:"pack_size"`
}

// SelectorSpec holds one field's extraction configuration.
type SelectorSpec struct {
	CSS   string `json:"css"`   // CSS selector; empty = skip
	Regex string `json:"regex"` // optional post-extraction regex (first capture group)
}

// ParseSelectors unmarshals selectors_json from a scrape config.
func ParseSelectors(raw string) (*Selectors, error) {
	var s Selectors
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Extract runs the selectors against HTML and returns up to limit products.
// Falls back to JSON-LD auto-detection when CSS selectors are empty.
func Extract(htmlBody string, sels *Selectors, limit int) []ExtractedProduct {
	// Try JSON-LD / schema.org first — most reliable when available.
	if products := extractJSONLD(htmlBody); len(products) > 0 {
		if len(products) > limit {
			products = products[:limit]
		}
		return products
	}

	// Fall back to CSS selectors when configured.
	if sels == nil || (sels.Name.CSS == "" && sels.Price.CSS == "") {
		return nil
	}
	return extractCSS(htmlBody, sels, limit)
}

// ── JSON-LD auto-detection ────────────────────────────────────────────────────

var scriptJSONLDRE = regexp.MustCompile(`(?i)<script[^>]+type=["']application/ld\+json["'][^>]*>([\s\S]*?)</script>`)
var priceRE = regexp.MustCompile(`[\d]+\.?\d*`)

func extractJSONLD(body string) []ExtractedProduct {
	matches := scriptJSONLDRE.FindAllStringSubmatch(body, -1)
	var results []ExtractedProduct
	for _, m := range matches {
		raw := strings.TrimSpace(m[1])
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			continue
		}
		if p := tryProduct(obj); p != nil {
			results = append(results, *p)
		}
		// Some pages wrap in @graph array.
		if graph, ok := obj["@graph"].([]any); ok {
			for _, item := range graph {
				if sub, ok := item.(map[string]any); ok {
					if p := tryProduct(sub); p != nil {
						results = append(results, *p)
					}
				}
			}
		}
	}
	return results
}

func tryProduct(obj map[string]any) *ExtractedProduct {
	t, _ := obj["@type"].(string)
	if !strings.EqualFold(t, "Product") {
		return nil
	}
	name, _ := obj["name"].(string)
	if name == "" {
		return nil
	}
	var priceStr string
	if offers, ok := obj["offers"]; ok {
		switch v := offers.(type) {
		case map[string]any:
			priceStr, _ = v["price"].(string)
			if priceStr == "" {
				if f, ok := v["price"].(float64); ok {
					priceStr = strconv.FormatFloat(f, 'f', 2, 64)
				}
			}
		}
	}
	price, _ := strconv.ParseFloat(strings.TrimSpace(priceStr), 64)
	return &ExtractedProduct{Name: name, Price: price}
}

// ── CSS selector extraction ───────────────────────────────────────────────────

func extractCSS(body string, sels *Selectors, limit int) []ExtractedProduct {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}

	nameNodes := cssSelect(doc, sels.Name.CSS)
	priceNodes := cssSelect(doc, sels.Price.CSS)

	count := len(nameNodes)
	if len(priceNodes) < count {
		count = len(priceNodes)
	}
	if count > limit {
		count = limit
	}

	var out []ExtractedProduct
	for i := 0; i < count; i++ {
		name := applyRegex(nodeText(nameNodes[i]), sels.Name.Regex)
		priceText := applyRegex(nodeText(priceNodes[i]), sels.Price.Regex)
		price := parsePrice(priceText)
		var packSize string
		if sels.PackSize.CSS != "" && i < len(cssSelect(doc, sels.PackSize.CSS)) {
			packSize = applyRegex(nodeText(cssSelect(doc, sels.PackSize.CSS)[i]), sels.PackSize.Regex)
		}
		out = append(out, ExtractedProduct{Name: name, Price: price, PackSize: packSize})
	}
	return out
}

func nodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

func applyRegex(text, pattern string) string {
	if pattern == "" {
		return text
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return text
	}
	m := re.FindStringSubmatch(text)
	if len(m) > 1 {
		return m[1]
	}
	if len(m) == 1 {
		return m[0]
	}
	return text
}

func parsePrice(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	if m := priceRE.FindString(s); m != "" {
		v, _ := strconv.ParseFloat(m, 64)
		return v
	}
	return 0
}

// cssSelect is a minimal CSS class + tag selector (no full CSS engine dep).
// Supports: "tag", ".class", "tag.class", "#id". Sufficient for common retail
// markup; operators can provide more specific selectors.
func cssSelect(n *html.Node, selector string) []*html.Node {
	if selector == "" || n == nil {
		return nil
	}
	tag, class, id := parseSimpleSelector(selector)
	var results []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if matchesSelector(n, tag, class, id) {
				results = append(results, n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return results
}

func parseSimpleSelector(sel string) (tag, class, id string) {
	if i := strings.IndexByte(sel, '#'); i >= 0 {
		tag = sel[:i]
		id = sel[i+1:]
		return
	}
	if i := strings.IndexByte(sel, '.'); i >= 0 {
		tag = sel[:i]
		class = sel[i+1:]
		return
	}
	tag = sel
	return
}

func matchesSelector(n *html.Node, tag, class, id string) bool {
	if tag != "" && n.Data != tag {
		return false
	}
	for _, a := range n.Attr {
		if class != "" && a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == class {
					return true
				}
			}
			return false
		}
		if id != "" && a.Key == "id" {
			return a.Val == id
		}
	}
	return class == "" && id == ""
}

// StripScripts removes <script> and <style> tags from HTML for safe iframe
// embedding in the scrape configuration tool (§6.7 sandbox).
func StripScripts(body string) string {
	var b strings.Builder
	r := strings.NewReader(body)
	z := html.NewTokenizer(r)
	skip := false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		token := z.Token()
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			name := token.Data
			if name == "script" || name == "style" {
				skip = (tt == html.StartTagToken)
				continue
			}
			if !skip {
				b.WriteString(token.String())
			}
		case html.EndTagToken:
			if token.Data == "script" || token.Data == "style" {
				skip = false
				continue
			}
			if !skip {
				b.WriteString(token.String())
			}
		default:
			if !skip {
				b.WriteString(token.String())
			}
		}
	}
	return b.String()
}
