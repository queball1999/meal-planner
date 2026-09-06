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
// then OpenGraph meta tags, then microdata — returning whatever it can find.
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

	if r.Title == "" && len(r.Ingredients) == 0 {
		return nil, NoRecipeFound
	}
	return r, nil
}

// NoRecipeFound is returned when no parsers extracted anything useful.
var NoRecipeFound = &noRecipeErr{}

type noRecipeErr struct{}

func (e *noRecipeErr) Error() string { return "scrape: no recipe data found in page" }

// ── JSON-LD ───────────────────────────────────────────────────────────────────

func parseJSONLDRecipe(body string, r *Recipe) bool {
	matches := jsonldScriptRE.FindAllStringSubmatch(body, -1)
	for _, m := range matches {
		var obj map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &obj); err != nil {
			continue
		}
		if applyRecipeObj(obj, r) {
			return true
		}
		// @graph array
		if graph, ok := obj["@graph"].([]any); ok {
			for _, item := range graph {
				if sub, ok := item.(map[string]any); ok {
					if applyRecipeObj(sub, r) {
						return true
					}
				}
			}
		}
	}
	return false
}

func applyRecipeObj(obj map[string]any, r *Recipe) bool {
	t, _ := obj["@type"].(string)
	if !strings.EqualFold(t, "Recipe") {
		// @type can also be a []any
		if arr, ok := obj["@type"].([]any); ok {
			found := false
			for _, v := range arr {
				if s, ok := v.(string); ok && strings.EqualFold(s, "Recipe") {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		} else if t == "" {
			return false
		}
	}

	r.Title = strVal(obj, "name")
	r.Description = strVal(obj, "description")
	r.ImageURL = extractImageURL(obj["image"])
	r.PrepMinutes = parseDuration(strVal(obj, "prepTime"))
	r.CookMinutes = parseDuration(strVal(obj, "cookTime"))
	r.Servings = parseServings(strVal(obj, "recipeYield"))

	// Keywords / categories → tags
	for _, key := range []string{"keywords", "recipeCategory", "recipeCuisine"} {
		r.Tags = append(r.Tags, extractStringList(obj[key])...)
	}

	// Ingredients
	if ings, ok := obj["recipeIngredient"].([]any); ok {
		for _, v := range ings {
			if s, ok := v.(string); ok && s != "" {
				r.Ingredients = append(r.Ingredients, RecipeIngredient{Raw: strings.TrimSpace(s)})
			}
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

func extractSteps(v any) []string {
	var steps []string
	switch s := v.(type) {
	case string:
		if s != "" {
			steps = append(steps, s)
		}
	case []any:
		for _, item := range s {
			switch it := item.(type) {
			case string:
				if it != "" {
					steps = append(steps, strings.TrimSpace(it))
				}
			case map[string]any:
				// HowToStep
				if text, ok := it["text"].(string); ok && text != "" {
					steps = append(steps, strings.TrimSpace(text))
				} else if name, ok := it["name"].(string); ok && name != "" {
					steps = append(steps, strings.TrimSpace(name))
				}
			}
		}
	}
	return steps
}

func extractStringList(v any) []string {
	if v == nil {
		return nil
	}
	var out []string
	switch s := v.(type) {
	case string:
		for _, tok := range strings.Split(s, ",") {
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
			r.Title = html.UnescapeString(m[1])
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
