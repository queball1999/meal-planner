package scrape

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Recipe is the structured result of parsing a recipe page (§5.7).
// All fields are best-effort; partial results are acceptable.
type Recipe struct {
	Title       string
	Description string
	ImageURL    string
	Servings    int
	PrepMinutes int
	CookMinutes int
	Tags        []string
	Ingredients []RecipeIngredient
	Steps       []string
	SourceURL   string
	SourceSite  string // hostname
	Author      string // who wrote the recipe; "" when the page doesn't say
}

// RecipeIngredient is one parsed ingredient line.
type RecipeIngredient struct {
	Raw      string // full original text
	Name     string
	Quantity string
	Unit     string
}

var jsonldScriptRE = regexp.MustCompile(`(?i)<script[^>]+type=["']application/ld\+json["'][^>]*>([\s\S]*?)</script>`)
var iso8601DurRE = regexp.MustCompile(`PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?`)

// ParseRecipe attempts to extract a Recipe from HTML. It tries JSON-LD first,
// then OpenGraph meta tags, then microdata - returning whatever it can find.
func ParseRecipe(htmlBody, sourceURL string) (*Recipe, error) {
	r := &Recipe{SourceURL: sourceURL}
	if u, err := url.Parse(sourceURL); err == nil {
		r.SourceSite = u.Hostname()
	}

	// 1. JSON-LD
	filled := parseJSONLDRecipe(htmlBody, r)

	// 2. OpenGraph fallback for missing title / image.
	parseOGFallback(htmlBody, r)

	// 3. Microdata fallback.
	if !filled {
		parseMicrodata(htmlBody, r)
	}

	decodeRecipeText(r)
	r.Title = cleanTitle(r.Title)

	if r.Title == "" && len(r.Ingredients) == 0 {
		return nil, NoRecipeFound
	}
	return r, nil
}

var trailingParenRE = regexp.MustCompile(`\s*\([^()]*\)\s*$`)

// cleanTitle strips trailing parenthetical annotations from a recipe title,
// e.g. "Spaghetti Bolognese (batch-cooked for tomorrow)" -> "Spaghetti
// Bolognese". Sites append these as meal-plan notes rather than part of the
// recipe's actual name, so they'd otherwise leak into saved recipes.
func cleanTitle(title string) string {
	for {
		stripped := trailingParenRE.ReplaceAllString(title, "")
		if stripped == title {
			break
		}
		title = strings.TrimSpace(stripped)
	}
	return title
}

// NoRecipeFound is returned when no parsers extracted anything useful.
var NoRecipeFound = &noRecipeErr{}

type noRecipeErr struct{}

func (e *noRecipeErr) Error() string { return "scrape: no recipe data found in page" }

// ── JSON-LD ───────────────────────────────────────────────────────────────────

// parseJSONLDRecipe scans every ld+json block and walks each document looking
// for a Recipe node. Real-world pages wrap it in a top-level array, an
// @graph list, or a mainEntity chain, so the walk has to recurse rather than
// assume the block decodes to a single object (allrecipes.com ships an array).
func parseJSONLDRecipe(body string, r *Recipe) bool {
	matches := jsonldScriptRE.FindAllStringSubmatch(body, -1)
	for _, m := range matches {
		var doc any
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &doc); err != nil {
			continue
		}
		if findRecipeNode(doc, r, 0) {
			return true
		}
	}
	return false
}

// findRecipeNode depth-first searches a decoded JSON-LD document for the first
// node that applyRecipeObj accepts.
func findRecipeNode(node any, r *Recipe, depth int) bool {
	if depth > 8 {
		return false
	}
	switch n := node.(type) {
	case []any:
		for _, item := range n {
			if findRecipeNode(item, r, depth+1) {
				return true
			}
		}
	case map[string]any:
		if applyRecipeObj(n, r) {
			return true
		}
		for _, key := range []string{"@graph", "mainEntity", "mainEntityOfPage", "itemListElement", "hasPart"} {
			if v, ok := n[key]; ok {
				if findRecipeNode(v, r, depth+1) {
					return true
				}
			}
		}
	}
	return false
}

// isRecipeType reports whether a JSON-LD @type value (a string or a list of
// strings) names a Recipe. A node with no @type at all is treated as a
// candidate so sloppy markup still parses.
func isRecipeType(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == "" || strings.EqualFold(t, "Recipe")
	case []any:
		for _, item := range t {
			if sub, ok := item.(string); ok && strings.EqualFold(sub, "Recipe") {
				return true
			}
		}
	}
	return false
}

func applyRecipeObj(obj map[string]any, r *Recipe) bool {
	if !isRecipeType(obj["@type"]) {
		return false
	}

	r.Title = strVal(obj, "name")
	r.Description = cleanText(strVal(obj, "description"))
	r.Author = cleanText(extractAuthor(obj["author"]))
	r.ImageURL = extractImageURL(obj["image"])
	r.PrepMinutes = parseDuration(anyToString(obj["prepTime"]))
	r.CookMinutes = parseDuration(anyToString(obj["cookTime"]))
	r.Servings = parseServings(anyToString(obj["recipeYield"]))

	// Keywords / categories → tags
	for _, key := range []string{"keywords", "recipeCategory", "recipeCuisine"} {
		r.Tags = append(r.Tags, extractStringList(obj[key])...)
	}

	// Ingredients. Some sites use the older "ingredients" key.
	for _, key := range []string{"recipeIngredient", "ingredients"} {
		for _, line := range extractStringList(obj[key]) {
			if line = cleanText(line); line != "" {
				r.Ingredients = append(r.Ingredients, RecipeIngredient{Raw: line})
			}
		}
		if len(r.Ingredients) > 0 {
			break
		}
	}

	// Steps
	if instr, ok := obj["recipeInstructions"]; ok {
		r.Steps = extractSteps(instr)
	}

	return r.Title != "" || len(r.Ingredients) > 0
}

func extractImageURL(v any) string {
	if v == nil {
		return ""
	}
	switch img := v.(type) {
	case string:
		return img
	case map[string]any:
		if u, ok := img["url"].(string); ok {
			return u
		}
	case []any:
		if len(img) > 0 {
			return extractImageURL(img[0])
		}
	}
	return ""
}

// extractAuthor reads a JSON-LD author: a name string, a Person or
// Organization object, or a list of either (the first one wins).
func extractAuthor(v any) string {
	switch a := v.(type) {
	case string:
		return strings.TrimSpace(a)
	case map[string]any:
		if n, ok := a["name"].(string); ok {
			return strings.TrimSpace(n)
		}
	case []any:
		for _, item := range a {
			if n := extractAuthor(item); n != "" {
				return n
			}
		}
	}
	return ""
}

// extractSteps flattens recipeInstructions into plain step strings. It handles
// a bare string (split on newlines), a list of strings, HowToStep objects, and
// HowToSection objects whose steps live under itemListElement.
func extractSteps(v any) []string {
	var steps []string
	switch s := v.(type) {
	case string:
		// Split before cleaning: cleanText collapses newlines into spaces.
		for _, line := range strings.Split(strings.ReplaceAll(s, "<br>", "\n"), "\n") {
			if line = cleanText(line); line != "" {
				steps = append(steps, line)
			}
		}
	case []any:
		for _, item := range s {
			steps = append(steps, extractSteps(item)...)
		}
	case map[string]any:
		// HowToSection: recurse into its child steps.
		if sub, ok := s["itemListElement"]; ok {
			steps = append(steps, extractSteps(sub)...)
			break
		}
		if text := cleanText(strVal(s, "text")); text != "" {
			steps = append(steps, text)
		} else if name := cleanText(strVal(s, "name")); name != "" {
			steps = append(steps, name)
		}
	}
	return steps
}

var htmlTagRE = regexp.MustCompile(`<[^>]*>`)

// cleanText strips embedded HTML tags, unescapes entities, and collapses
// whitespace. JSON-LD payloads routinely carry markup inside step text.
func cleanText(s string) string {
	if s == "" {
		return ""
	}
	s = htmlTagRE.ReplaceAllString(s, " ")
	s = decodeEntities(s)
	s = strings.ReplaceAll(s, " ", " ")
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// decodeEntities unescapes HTML entities ("Steve&#39;s" -> "Steve's").
// Recipe sites often encode twice ("&amp;#39;"), so it repeats until the text
// stops changing - capped, so a pathological "&amp;amp;amp;..." terminates.
func decodeEntities(s string) string {
	for i := 0; i < 3 && strings.Contains(s, "&"); i++ {
		next := html.UnescapeString(s)
		if next == s {
			break
		}
		s = next
	}
	return s
}

// decodeRecipeText runs decodeEntities over every text field, whichever
// parser filled it. The title, tags, and microdata text bypass cleanText, and
// entities left in any of them would show literally once saved.
func decodeRecipeText(r *Recipe) {
	fix := func(s string) string { return strings.TrimSpace(decodeEntities(s)) }
	r.Title = fix(r.Title)
	r.Description = fix(r.Description)
	for i := range r.Tags {
		r.Tags[i] = fix(r.Tags[i])
	}
	for i := range r.Ingredients {
		r.Ingredients[i].Raw = fix(r.Ingredients[i].Raw)
		r.Ingredients[i].Name = fix(r.Ingredients[i].Name)
	}
	for i := range r.Steps {
		r.Steps[i] = fix(r.Steps[i])
	}
}

// anyToString renders a JSON value that may be a string, number, or list of
// either as a single string - recipeYield and the *Time fields vary by site.
func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		if len(t) > 0 {
			return anyToString(t[0])
		}
	}
	return ""
}

func extractStringList(v any) []string {
	if v == nil {
		return nil
	}
	var out []string
	switch s := v.(type) {
	case string:
		sep := ","
		if strings.Contains(s, "\n") {
			sep = "\n"
		}
		for _, tok := range strings.Split(s, sep) {
			if t := strings.TrimSpace(tok); t != "" {
				out = append(out, t)
			}
		}
	case []any:
		for _, item := range s {
			if str, ok := item.(string); ok && str != "" {
				out = append(out, strings.TrimSpace(str))
			}
		}
	}
	return out
}

func strVal(obj map[string]any, key string) string {
	if s, ok := obj[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// parseDuration parses ISO 8601 duration strings like "PT1H30M" → minutes.
func parseDuration(s string) int {
	m := iso8601DurRE.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return h*60 + min
}

func parseServings(s string) int {
	// "4 servings", "serves 4", "4", "4-6" → take first number
	re := regexp.MustCompile(`\d+`)
	if n := re.FindString(s); n != "" {
		v, _ := strconv.Atoi(n)
		return v
	}
	return 0
}

// ── OpenGraph fallback ────────────────────────────────────────────────────────

var ogTitleRE = regexp.MustCompile(`(?i)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']+)["']`)
var ogImageRE = regexp.MustCompile(`(?i)<meta[^>]+property=["']og:image["'][^>]+content=["']([^"']+)["']`)

func parseOGFallback(body string, r *Recipe) {
	if r.Title == "" {
		if m := ogTitleRE.FindStringSubmatch(body); m != nil {
			r.Title = m[1] // entities decoded by decodeRecipeText
		}
	}
	if r.ImageURL == "" {
		if m := ogImageRE.FindStringSubmatch(body); m != nil {
			r.ImageURL = m[1]
		}
	}
}

// ── Microdata fallback ────────────────────────────────────────────────────────

func parseMicrodata(body string, r *Recipe) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if getAttr(n, "itemtype") != "" &&
				strings.Contains(strings.ToLower(getAttr(n, "itemtype")), "recipe") {
				extractMicrodataRecipe(n, r)
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

func extractMicrodataRecipe(n *html.Node, r *Recipe) {
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			prop := getAttr(node, "itemprop")
			switch prop {
			case "name":
				if r.Title == "" {
					r.Title = strings.TrimSpace(innerText(node))
				}
			case "image":
				if r.ImageURL == "" {
					if src := getAttr(node, "src"); src != "" {
						r.ImageURL = src
					} else if content := getAttr(node, "content"); content != "" {
						r.ImageURL = content
					}
				}
			case "recipeIngredient":
				text := strings.TrimSpace(innerText(node))
				if text != "" {
					r.Ingredients = append(r.Ingredients, RecipeIngredient{Raw: text})
				}
			case "recipeInstructions":
				text := strings.TrimSpace(innerText(node))
				if text != "" {
					r.Steps = append(r.Steps, text)
				}
			case "recipeYield":
				if r.Servings == 0 {
					r.Servings = parseServings(strings.TrimSpace(innerText(node)))
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func innerText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
