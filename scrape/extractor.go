package scrape

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// ExtractedProduct is one price result pulled from a scraped page.
type ExtractedProduct struct {
	Name     string
	Price    float64 // dollars
	PackSize string  // raw string e.g. "5 lb"
}

// Selectors is the parsed form of a scrape_config's selectors_json field.
//
// Two extraction shapes are supported:
//
//   - Item set: each product lives in its own container element. Name, price
//     and pack size are looked up *inside* each container, so a product with a
//     missing price never shifts the pairing of the products after it. This is
//     the shape the AI detector proposes and the one operators should prefer.
//   - Item empty: name and price selectors are matched document-wide and
//     zipped by index. Kept for configs saved before containers existed.
type Selectors struct {
	Item     SelectorSpec `json:"item"` // per-product container; optional
	Name     SelectorSpec `json:"name"`
	Price    SelectorSpec `json:"price"`
	PackSize SelectorSpec `json:"pack_size"`
}

// SelectorSpec holds one field's extraction configuration.
type SelectorSpec struct {
	CSS   string `json:"css"`   // CSS selector; empty = skip
	Attr  string `json:"attr"`  // read this attribute instead of text (e.g. "content")
	Regex string `json:"regex"` // optional post-extraction regex (first capture group)
}

// Empty reports whether the spec selects nothing.
func (s SelectorSpec) Empty() bool { return strings.TrimSpace(s.CSS) == "" }

// legacySelectors mirrors Selectors but with the pre-container key names used
// by the assisted-mode UI ("price_css" etc.), so old configs still load.
type legacySelectors struct {
	ItemCSS     string `json:"item_css"`
	NameCSS     string `json:"name_css"`
	PriceCSS    string `json:"price_css"`
	PackSizeCSS string `json:"pack_size_css"`
}

// ParseSelectors unmarshals selectors_json from a scrape config. It accepts
// both the nested form ({"price":{"css":"..."}}) and the flat legacy form
// ({"price_css":"..."}), and never fails on an empty or malformed value -
// callers treat a nil-ish result as "no selectors, use auto-detection".
func ParseSelectors(raw string) (*Selectors, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "null" {
		return &Selectors{}, nil
	}
	var s Selectors
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	// Fill anything still empty from the legacy flat keys.
	var l legacySelectors
	if err := json.Unmarshal([]byte(raw), &l); err == nil {
		if s.Item.Empty() {
			s.Item.CSS = l.ItemCSS
		}
		if s.Name.Empty() {
			s.Name.CSS = l.NameCSS
		}
		if s.Price.Empty() {
			s.Price.CSS = l.PriceCSS
		}
		if s.PackSize.Empty() {
			s.PackSize.CSS = l.PackSizeCSS
		}
	}
	return &s, nil
}

// Extract runs the selectors against HTML and returns up to limit products.
// JSON-LD / microdata auto-detection is tried first because it is far more
// stable than CSS; configured selectors are the fallback.
func Extract(htmlBody string, sels *Selectors, limit int) []ExtractedProduct {
	if limit <= 0 {
		limit = 5
	}
	if products := extractStructured(htmlBody); len(products) > 0 {
		return capProducts(products, limit)
	}
	if sels == nil || (sels.Name.Empty() && sels.Price.Empty()) {
		return nil
	}
	return capProducts(extractCSS(htmlBody, sels, limit), limit)
}

// ExtractWithSelectors runs only the CSS selector path, skipping structured
// auto-detection. The config UI uses it to prove that a proposed selector set
// actually works on a page that also happens to carry JSON-LD.
func ExtractWithSelectors(htmlBody string, sels *Selectors, limit int) []ExtractedProduct {
	if sels == nil || (sels.Name.Empty() && sels.Price.Empty()) {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	return capProducts(extractCSS(htmlBody, sels, limit), limit)
}

// ExtractStructured exposes the JSON-LD / microdata detector on its own.
func ExtractStructured(htmlBody string, limit int) []ExtractedProduct {
	return capProducts(extractStructured(htmlBody), limit)
}

func capProducts(products []ExtractedProduct, limit int) []ExtractedProduct {
	var out []ExtractedProduct
	for _, p := range products {
		if strings.TrimSpace(p.Name) == "" && p.Price <= 0 {
			continue
		}
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ── structured data (JSON-LD + microdata) ─────────────────────────────────────

var scriptJSONLDRE = regexp.MustCompile(`(?i)<script[^>]+type=["']application/ld\+json["'][^>]*>([\s\S]*?)</script>`)
var priceRE = regexp.MustCompile(`\d+(?:\.\d+)?`)

func extractStructured(body string) []ExtractedProduct {
	if products := extractJSONLD(body); len(products) > 0 {
		return products
	}
	return extractMicrodata(body)
}

// extractJSONLD walks every ld+json block. Retail pages nest Products inside
// arrays, @graph, and ItemList/itemListElement wrappers, so the walk recurses
// instead of only checking the top level.
func extractJSONLD(body string) []ExtractedProduct {
	var results []ExtractedProduct
	for _, m := range scriptJSONLDRE.FindAllStringSubmatch(body, -1) {
		var doc any
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &doc); err != nil {
			continue
		}
		collectProducts(doc, &results, 0)
	}
	return results
}

func collectProducts(node any, out *[]ExtractedProduct, depth int) {
	if depth > 8 || len(*out) > 50 {
		return
	}
	switch n := node.(type) {
	case []any:
		for _, item := range n {
			collectProducts(item, out, depth+1)
		}
	case map[string]any:
		if p := tryProduct(n); p != nil {
			*out = append(*out, *p)
		}
		for _, key := range []string{"@graph", "itemListElement", "item", "mainEntity", "hasPart", "isSimilarTo"} {
			if v, ok := n[key]; ok {
				collectProducts(v, out, depth+1)
			}
		}
	}
}

func tryProduct(obj map[string]any) *ExtractedProduct {
	if !hasType(obj["@type"], "Product", "Offer", "IndividualProduct", "ProductModel", "GroceryStore") {
		return nil
	}
	name := firstString(obj, "name", "title")
	if name == "" {
		return nil
	}
	price := offerPrice(obj["offers"])
	if price <= 0 {
		// A bare Offer node carries price/priceSpecification directly.
		price = offerPrice(obj)
	}
	if price <= 0 && name == "" {
		return nil
	}
	return &ExtractedProduct{
		Name:     name,
		Price:    price,
		PackSize: firstString(obj, "size", "weight", "netContent"),
	}
}

// offerPrice digs a numeric price out of an offers value, which may be an
// object, a list of objects, or carry the number under priceSpecification.
func offerPrice(v any) float64 {
	switch o := v.(type) {
	case []any:
		for _, item := range o {
			if p := offerPrice(item); p > 0 {
				return p
			}
		}
	case map[string]any:
		for _, key := range []string{"price", "lowPrice", "highPrice"} {
			if p := numberOrString(o[key]); p > 0 {
				return p
			}
		}
		if spec, ok := o["priceSpecification"]; ok {
			return offerPrice(spec)
		}
	}
	return 0
}

func numberOrString(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		return parsePrice(t)
	}
	return 0
}

func hasType(v any, want ...string) bool {
	match := func(s string) bool {
		for _, w := range want {
			if strings.EqualFold(s, w) {
				return true
			}
		}
		return false
	}
	switch t := v.(type) {
	case string:
		return match(t)
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && match(s) {
				return true
			}
		}
	}
	return false
}

func firstString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := obj[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case map[string]any:
			if s := firstString(v, "value", "name"); s != "" {
				return s
			}
		}
	}
	return ""
}

// extractMicrodata handles schema.org microdata - itemprop attributes with the
// value in text or in a meta/link content attribute. Common on older store
// templates that predate JSON-LD.
func extractMicrodata(body string) []ExtractedProduct {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	scopes := selectAll(doc, `[itemtype*="Product"]`)
	var out []ExtractedProduct
	for _, scope := range scopes {
		name := itemprop(scope, "name")
		price := parsePrice(itemprop(scope, "price"))
		if price <= 0 {
			price = parsePrice(itemprop(scope, "lowPrice"))
		}
		if name == "" && price <= 0 {
			continue
		}
		out = append(out, ExtractedProduct{Name: name, Price: price})
	}
	return out
}

func itemprop(scope *html.Node, prop string) string {
	nodes := selectAll(scope, `[itemprop="`+prop+`"]`)
	for _, n := range nodes {
		if v := attrValue(n, "content"); v != "" {
			return v
		}
		if v := nodeText(n); v != "" {
			return v
		}
	}
	return ""
}

// ── CSS selector extraction ───────────────────────────────────────────────────

func extractCSS(body string, sels *Selectors, limit int) []ExtractedProduct {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}

	// Preferred shape: one container per product.
	if !sels.Item.Empty() {
		var out []ExtractedProduct
		for _, item := range selectAll(doc, sels.Item.CSS) {
			p := ExtractedProduct{
				Name:     valueIn(item, sels.Name),
				PackSize: valueIn(item, sels.PackSize),
			}
			p.Price = parsePrice(valueIn(item, sels.Price))
			if p.Name == "" && p.Price <= 0 {
				continue
			}
			out = append(out, p)
			if len(out) >= limit {
				break
			}
		}
		if len(out) > 0 {
			return out
		}
		// Container matched nothing usable - fall through to the flat pairing.
	}

	nameNodes := selectAll(doc, sels.Name.CSS)
	priceNodes := selectAll(doc, sels.Price.CSS)
	packNodes := selectAll(doc, sels.PackSize.CSS)

	// Price-only configs are valid: the price is what the chain needs.
	count := len(priceNodes)
	if len(nameNodes) > 0 && len(nameNodes) < count {
		count = len(nameNodes)
	}
	if count == 0 {
		count = len(nameNodes)
	}
	if count > limit {
		count = limit
	}

	var out []ExtractedProduct
	for i := 0; i < count; i++ {
		var p ExtractedProduct
		if i < len(nameNodes) {
			p.Name = readNode(nameNodes[i], sels.Name)
		}
		if i < len(priceNodes) {
			p.Price = parsePrice(readNode(priceNodes[i], sels.Price))
		}
		if i < len(packNodes) {
			p.PackSize = readNode(packNodes[i], sels.PackSize)
		}
		out = append(out, p)
	}
	return out
}

// valueIn resolves a spec inside a container, treating the container itself as
// a candidate when the selector is empty or matches nothing beneath it.
func valueIn(item *html.Node, spec SelectorSpec) string {
	if spec.Empty() {
		return ""
	}
	for _, n := range selectAll(item, spec.CSS) {
		if v := readNode(n, spec); v != "" {
			return v
		}
	}
	return ""
}

func readNode(n *html.Node, spec SelectorSpec) string {
	if n == nil {
		return ""
	}
	text := ""
	if spec.Attr != "" {
		text = attrValue(n, spec.Attr)
	}
	if text == "" {
		text = nodeText(n)
	}
	// Elements that carry their value in an attribute by convention.
	if text == "" && (n.Data == "meta" || n.Data == "link") {
		text = attrValue(n, "content")
		if text == "" {
			text = attrValue(n, "href")
		}
	}
	return applyRegex(text, spec.Regex)
}

// selectAll compiles and runs a full CSS selector (descendant combinators,
// attribute matches, :nth-child and friends) via cascadia. An invalid selector
// yields no matches rather than an error - operator input is untrusted.
func selectAll(root *html.Node, selector string) []*html.Node {
	selector = strings.TrimSpace(selector)
	if selector == "" || root == nil {
		return nil
	}
	sel, err := cascadia.Compile(selector)
	if err != nil {
		return nil
	}
	return cascadia.QueryAll(root, sel)
}

// ValidateSelector reports whether a selector string is syntactically usable,
// so the config UI can reject typos before saving them.
func ValidateSelector(selector string) error {
	if strings.TrimSpace(selector) == "" {
		return nil
	}
	_, err := cascadia.Compile(selector)
	return err
}

func attrValue(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
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
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
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

// parsePrice pulls the first plausible dollar amount out of a string. It
// prefers a "$" -anchored number so "Was $9.99, now $6.49" style text and
// unit-price suffixes ("$6.49 ($0.41/oz)") resolve to the shelf price.
func parsePrice(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	s = strings.ReplaceAll(s, ",", "")
	if i := strings.IndexByte(s, '$'); i >= 0 {
		if m := priceRE.FindString(s[i+1:]); m != "" {
			v, _ := strconv.ParseFloat(m, 64)
			return v
		}
	}
	// Cents-style markup: "6" + "49" split across nodes collapses to "6 49".
	if m := regexp.MustCompile(`^(\d+)\s+(\d{2})$`).FindStringSubmatch(s); m != nil {
		v, _ := strconv.ParseFloat(m[1]+"."+m[2], 64)
		return v
	}
	if m := priceRE.FindString(s); m != "" {
		v, _ := strconv.ParseFloat(m, 64)
		return v
	}
	return 0
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
