package catalog

import (
	"sort"
	"strings"

	"goeat/db"
)

// Match is one candidate item for a free-text ingredient name, with how
// confident the match is on a 0-1 scale.
type Match struct {
	ItemID int64
	Name   string
	Term   string
	Score  float64
}

// Confidence thresholds.
//
// AutoLinkScore is deliberately high. Linking two items together is a
// destructive merge - prices, pantry stock and history all move - and the cost
// of a wrong automatic merge (silently buying the wrong thing all week, with
// no obvious place to look) far exceeds the cost of asking. Below it the UI
// shows suggestions and a person decides.
//
// SuggestScore is low enough to surface plausible-but-not-certain candidates,
// since a human reading three options rejects a bad one instantly.
const (
	AutoLinkScore = 0.92
	SuggestScore  = 0.45
)

// stopWords are the words that carry no identity in a grocery name. "Fresh
// organic boneless chicken breast" and "chicken breast" are the same thing to
// a shopping list, and leaving these in makes long descriptive names score
// badly against the short catalog names they should match.
var stopWords = map[string]bool{
	"fresh": true, "organic": true, "raw": true, "whole": true, "large": true,
	"small": true, "medium": true, "boneless": true, "skinless": true,
	"chopped": true, "diced": true, "sliced": true, "minced": true,
	"frozen": true, "dried": true, "ground": false, // "ground beef" needs it
	// "canned" is identity, not noise: ignoring it scored a fresh "tomato" as
	// a perfect match for "Canned diced tomatoes" and auto-linked the two.
	"of": true, "the": true, "a": true, "an": true, "and": true,
	"unsalted": true, "salted": true, "low": true, "reduced": true, "fat": true,
	"free": true, "range": true, "extra": true, "virgin": true, "pure": true,
}

// tokens splits a normalized term into its meaningful words, singularized.
func tokens(term string) []string {
	fields := strings.Fields(strings.ToLower(term))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, ".,()")
		if f == "" || stopWords[f] {
			continue
		}
		out = append(out, singular(f))
	}
	// A name made entirely of stop words ("fresh organic") still has to match
	// something rather than matching everything equally.
	if len(out) == 0 {
		for _, f := range fields {
			if f != "" {
				out = append(out, singular(f))
			}
		}
	}
	return out
}

// singular strips the plural forms that actually turn up in grocery names.
// Deliberately not a general stemmer: "breasts" -> "breast" and "tomatoes" ->
// "tomato" is the whole job, and an aggressive stemmer collapses words that
// are genuinely different foods.
func singular(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y" // berries -> berry
	case len(w) > 4 && strings.HasSuffix(w, "oes"):
		return w[:len(w)-2] // tomatoes -> tomato
	case len(w) > 3 && strings.HasSuffix(w, "ses"):
		return w[:len(w)-2] // molasses stays, glasses -> glasse (harmless)
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1] // breasts -> breast
	}
	return w
}

// Score rates how well two grocery names refer to the same thing, 0-1.
//
// Token overlap rather than edit distance: grocery names differ by whole words
// far more often than by characters ("chicken breast" vs "boneless skinless
// chicken breasts"), and edit distance on those two is terrible while the
// overlap is perfect. Weighted toward the *shorter* name's coverage, because a
// short catalog name fully contained in a long descriptive one is the case
// this has to catch.
func Score(a, b string) float64 {
	ta, tb := tokens(a), tokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	if strings.Join(ta, " ") == strings.Join(tb, " ") {
		return 1
	}

	setB := make(map[string]bool, len(tb))
	for _, t := range tb {
		setB[t] = true
	}
	var shared int
	for _, t := range ta {
		if setB[t] {
			shared++
		}
	}
	if shared == 0 {
		return 0
	}

	shorter := len(ta)
	if len(tb) < shorter {
		shorter = len(tb)
	}
	longer := len(ta)
	if len(tb) > longer {
		longer = len(tb)
	}

	// Coverage of the shorter name dominates; the longer name's extra words
	// cost something but not everything, since they are usually descriptors.
	coverage := float64(shared) / float64(shorter)
	dilution := float64(shared) / float64(longer)
	return 0.75*coverage + 0.25*dilution
}

// Suggest ranks items against a free-text name, best first, dropping anything
// below SuggestScore and returning at most limit results.
func Suggest(name string, items []scorable, limit int) []Match {
	out := make([]Match, 0, len(items))
	for _, it := range items {
		// Score against the item's display name and its normalized term, and
		// keep the better: a catalog name can carry punctuation and casing
		// that the term has already thrown away, and either can be the closer
		// form for a given query.
		s := Score(name, it.ItemName())
		if ts := Score(name, it.ItemTerm()); ts > s {
			s = ts
		}
		if s < SuggestScore {
			continue
		}
		out = append(out, Match{ItemID: it.ItemID(), Name: it.ItemName(), Term: it.ItemTerm(), Score: s})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// Ties break on name, so the list does not reshuffle between requests.
		return out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// scorable is the shape Suggest needs from a catalog item. An interface rather
// than *db.Item so the scoring can be tested without a database.
type scorable interface {
	ItemID() int64
	ItemName() string
	ItemTerm() string
}

// itemAdapter adapts a *db.Item to scorable.
type itemAdapter struct{ it *db.Item }

func (a itemAdapter) ItemID() int64    { return a.it.ID }
func (a itemAdapter) ItemName() string { return a.it.Name }
func (a itemAdapter) ItemTerm() string { return a.it.NormalizedTerm }

// SuggestItems ranks a household's catalog items against a free-text name.
//
// Auto-created placeholders (source "auto") are excluded: they are themselves
// the unmatched free-text names this is trying to resolve away, so offering
// one as a match for another would just chain two guesses together.
func SuggestItems(name string, items []*db.Item, limit int) []Match {
	cands := make([]scorable, 0, len(items))
	for _, it := range items {
		if it.Source == "auto" {
			continue
		}
		cands = append(cands, itemAdapter{it})
	}
	return Suggest(name, cands, limit)
}
