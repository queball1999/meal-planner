package scrape

import (
	"context"
	"strings"
	"testing"

	"goeat/llm"
)

const tilePage = `<html><body>
<div class="results">
  <article class="product-card" data-testid="product-card">
     <h3 data-testid="product-title">Large Eggs, 12 ct</h3>
     <span data-testid="item-price">$3.49</span>
     <span class="unit">12 ct</span>
  </article>
  <article class="product-card" data-testid="product-card">
     <h3 data-testid="product-title">Organic Eggs, 18 ct</h3>
     <span data-testid="item-price">$6.99</span>
     <span class="unit">18 ct</span>
  </article>
  <article class="product-card" data-testid="product-card">
     <h3 data-testid="product-title">Sold Out Eggs</h3>
     <span class="unit">6 ct</span>
  </article>
</div>
</body></html>`

func TestExtractWithItemContainer(t *testing.T) {
	sels := &Selectors{
		Item:     SelectorSpec{CSS: `article[data-testid="product-card"]`},
		Name:     SelectorSpec{CSS: `[data-testid="product-title"]`},
		Price:    SelectorSpec{CSS: `[data-testid="item-price"]`},
		PackSize: SelectorSpec{CSS: `.unit`},
	}
	got := ExtractWithSelectors(tilePage, sels, 5)
	if len(got) != 3 {
		t.Fatalf("got %d products, want 3: %#v", len(got), got)
	}
	if got[0].Name != "Large Eggs, 12 ct" || got[0].Price != 3.49 || got[0].PackSize != "12 ct" {
		t.Errorf("product 0 = %#v", got[0])
	}
	// The third tile has no price: it must keep its own name rather than
	// shifting the pairing of the tiles around it.
	if got[2].Name != "Sold Out Eggs" || got[2].Price != 0 {
		t.Errorf("product 2 = %#v", got[2])
	}
}

func TestExtractDescendantSelectorsWork(t *testing.T) {
	// The old hand-rolled matcher could not handle a descendant combinator.
	sels := &Selectors{
		Name:  SelectorSpec{CSS: `div.results article h3`},
		Price: SelectorSpec{CSS: `div.results article span[data-testid="item-price"]`},
	}
	got := ExtractWithSelectors(tilePage, sels, 5)
	if len(got) < 2 || got[1].Price != 6.99 {
		t.Fatalf("descendant selectors failed: %#v", got)
	}
}

func TestParseSelectorsAcceptsLegacyFlatKeys(t *testing.T) {
	s, err := ParseSelectors(`{"price_css":".price","name_css":".name"}`)
	if err != nil {
		t.Fatalf("ParseSelectors: %v", err)
	}
	if s.Price.CSS != ".price" || s.Name.CSS != ".name" {
		t.Errorf("legacy keys not mapped: %#v", s)
	}
	if empty, err := ParseSelectors(""); err != nil || !empty.Price.Empty() {
		t.Errorf("empty selectors json should parse to a blank set, got %#v %v", empty, err)
	}
}

func TestParsePricePrefersDollarAnchoredAmount(t *testing.T) {
	cases := map[string]float64{
		"$6.49 ($0.41/oz)":      6.49,
		"Was $9.99 now $6.49":   9.99, // first dollar amount wins
		"12 ct $3.49":           3.49,
		"3 49":                  3.49, // cents split across nodes
		"no price here":         0,
		"$1,299.00":             1299,
		"Price: 4.25 per pound": 4.25,
	}
	for in, want := range cases {
		if got := parsePrice(in); got != want {
			t.Errorf("parsePrice(%q) = %v, want %v", in, got, want)
		}
	}
}

const jsonLDListPage = `<html><head><script type="application/ld+json">
{"@context":"https://schema.org","@type":"ItemList","itemListElement":[
 {"@type":"ListItem","item":{"@type":"Product","name":"Milk 1 gal",
   "offers":{"@type":"Offer","priceSpecification":{"price":"4.19","priceCurrency":"USD"}}}},
 {"@type":"ListItem","item":{"@type":"Product","name":"Milk 1/2 gal",
   "offers":[{"@type":"Offer","price":2.79}]}}
]}</script></head><body></body></html>`

func TestExtractStructuredHandlesItemListAndOfferShapes(t *testing.T) {
	got := ExtractStructured(jsonLDListPage, 5)
	if len(got) != 2 {
		t.Fatalf("got %d products, want 2: %#v", len(got), got)
	}
	if got[0].Price != 4.19 {
		t.Errorf("priceSpecification not read: %#v", got[0])
	}
	if got[1].Price != 2.79 {
		t.Errorf("offers array not read: %#v", got[1])
	}
}

func TestExtractMicrodata(t *testing.T) {
	page := `<div itemscope itemtype="https://schema.org/Product">
	   <span itemprop="name">Butter 1 lb</span>
	   <meta itemprop="price" content="4.99">
	 </div>`
	got := ExtractStructured(page, 5)
	if len(got) != 1 || got[0].Price != 4.99 || got[0].Name != "Butter 1 lb" {
		t.Fatalf("microdata extraction failed: %#v", got)
	}
}

func TestDetectFindsHeuristicPattern(t *testing.T) {
	p := Detect(tilePage)
	if p.Confidence == "none" {
		t.Fatalf("heuristics found nothing: %+v", p)
	}
	if priced(p.Products) < 2 {
		t.Errorf("expected at least 2 priced products, got %#v", p.Products)
	}
}

func TestCondenseKeepsTilesAndDropsNoise(t *testing.T) {
	page := `<html><head><style>.x{color:red}</style></head><body>
	  <nav>Home Deals Weekly Ad Account Cart</nav>
	  <script>window.__DATA__={"tracking":"lots and lots of junk"}</script>
	  <div class="results">
	    <article class="product-card" style="margin:4px" onclick="track()">
	      <h3 class="title">Large Eggs</h3><span class="price">$3.49</span>
	    </article>
	    <article class="product-card"><h3 class="title">Small Eggs</h3><span class="price">$2.49</span></article>
	  </div></body></html>`
	got := Condense(page, 5000)
	if !strings.Contains(got, "product-card") || !strings.Contains(got, "$3.49") {
		t.Errorf("condensed output lost the product tiles:\n%s", got)
	}
	if strings.Contains(got, "__DATA__") || strings.Contains(got, "onclick") || strings.Contains(got, "margin:4px") {
		t.Errorf("condensed output kept noise:\n%s", got)
	}
}

// ── AI detector ───────────────────────────────────────────────────────────────

type stubGen struct {
	replies []string
	calls   int
	prompts []string
}

func (s *stubGen) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	s.prompts = append(s.prompts, req.Prompt)
	i := s.calls
	s.calls++
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	return llm.GenerateResponse{Content: s.replies[i]}, nil
}

// A page with hashed class names and no structured data: heuristics cannot
// touch it, so the AI path has to carry it.
const opaquePage = `<html><body><div class="a1b2">
  <div class="c3d4"><span class="e5f6">Cage Free Eggs 12ct</span><span class="g7h8">$4.29</span></div>
  <div class="c3d4"><span class="e5f6">Jumbo Eggs 18ct</span><span class="g7h8">$7.15</span></div>
</div></body></html>`

func TestProposeWithAIVerifiesAndRetries(t *testing.T) {
	gen := &stubGen{replies: []string{
		// First reply: plausible-looking but wrong selectors.
		`{"item":{"css":".nope"},"name":{"css":".nope-name"},"price":{"css":".nope-price"},"notes":"guess"}`,
		// Second reply, after the correction: the real ones, in a code fence.
		"```json\n{\"item\":{\"css\":\".c3d4\"},\"name\":{\"css\":\".e5f6\"},\"price\":{\"css\":\".g7h8\"},\"pack_size\":{\"css\":\"\"},\"notes\":\"hashed classes\"}\n```",
	}}

	p, err := ProposeWithAI(context.Background(), gen, opaquePage)
	if err != nil {
		t.Fatalf("ProposeWithAI: %v", err)
	}
	if gen.calls != 2 {
		t.Errorf("expected a retry after the failed verification, got %d call(s)", gen.calls)
	}
	if p.Confidence == "none" {
		t.Fatalf("proposal rejected: %+v", p)
	}
	if p.Selectors.Price.CSS != ".g7h8" {
		t.Errorf("selectors = %#v", p.Selectors)
	}
	if len(p.Products) != 2 || p.Products[0].Price != 4.29 {
		t.Errorf("verification products = %#v", p.Products)
	}
	if !strings.Contains(gen.prompts[1], "CORRECTION") {
		t.Error("retry prompt did not include the verification failure")
	}
}

func TestProposeWithAIRejectsUnverifiableSelectors(t *testing.T) {
	gen := &stubGen{replies: []string{`{"price":{"css":".still-wrong"}}`}}
	p, err := ProposeWithAI(context.Background(), gen, opaquePage)
	if err != nil {
		t.Fatalf("ProposeWithAI: %v", err)
	}
	if p.Confidence != "none" || p.Selectors.Price.CSS != "" {
		t.Errorf("unverified selectors should never be offered: %+v", p)
	}
}

func TestProposeWithAISkipsLLMWhenPageParsesItself(t *testing.T) {
	gen := &stubGen{replies: []string{`{"price":{"css":".x"}}`}}
	p, err := ProposeWithAI(context.Background(), gen, jsonLDListPage)
	if err != nil {
		t.Fatalf("ProposeWithAI: %v", err)
	}
	if gen.calls != 0 {
		t.Errorf("spent %d LLM call(s) on a page with JSON-LD", gen.calls)
	}
	if p.Confidence != "high" {
		t.Errorf("proposal = %+v", p)
	}
}

func TestExtractWithAIDropsImplausiblePrices(t *testing.T) {
	gen := &stubGen{replies: []string{
		`{"products":[{"name":"Eggs","price":4.29,"pack_size":"12 ct"},{"name":"Bad","price":0},{"name":"Worse","price":98000}]}`,
	}}
	got, err := ExtractWithAI(context.Background(), gen, opaquePage, "eggs")
	if err != nil {
		t.Fatalf("ExtractWithAI: %v", err)
	}
	if len(got) != 1 || got[0].Price != 4.29 {
		t.Fatalf("got %#v", got)
	}
}

func TestValidateSelectorRejectsGarbage(t *testing.T) {
	if err := ValidateSelector(`div[unclosed`); err == nil {
		t.Error("expected an error for malformed CSS")
	}
	if err := ValidateSelector(`div.card > span[data-x="1"]:nth-child(2)`); err != nil {
		t.Errorf("valid selector rejected: %v", err)
	}
}
