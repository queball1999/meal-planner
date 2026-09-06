package web

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
)

// perPageChoices are the page sizes offered in the pagination bar. Anything
// outside this set is clamped, so a hand-edited ?per_page= can never ask the
// server to render an unbounded page.
var perPageChoices = []int{10, 25, 50, 100}

const defaultPerPage = 25

// Pagination is the view model behind the shared "pagination" partial. It is
// built by paginate, which also slices the page's rows, so a handler never
// does the arithmetic itself.
type Pagination struct {
	Page        int
	PerPage     int
	Total       int
	TotalPages  int
	Prev        int // 0 when there is no previous page
	Next        int // 0 when there is no next page
	From        int // 1-based index of the first row on this page
	To          int // 1-based index of the last row on this page
	Pages       []int
	PerPageOpts []int

	// Query is every unrelated query parameter, already encoded and ending in
	// "&" (or empty), so a template can write href="?{{.Query}}page=2" and
	// keep the active filters.
	Query string
	// Carried is the same set of parameters as Query, minus per_page, as
	// hidden inputs for the rows-per-page form.
	Carried map[string]string
	// Param is the name of the page parameter, so a page with more than one
	// paginated table can scope them (see PaginateNamed).
	Param string
}

// HasPages reports whether the bar is worth rendering at all - a single page
// of results needs no navigation.
func (p Pagination) HasPages() bool { return p.TotalPages > 1 }

// paginate slices items to the page requested by ?page= / ?per_page= and
// returns the rows to render alongside the nav model.
func paginate[T any](r *http.Request, items []T) ([]T, Pagination) {
	return paginateNamed(r, items, "page")
}

// paginateNamed is paginate with a caller-chosen page parameter, for a page
// that renders several independently paged tables (the Prices page has one
// per store). Every table still shares one ?per_page=.
func paginateNamed[T any](r *http.Request, items []T, param string) ([]T, Pagination) {
	q := r.URL.Query()

	perPage := defaultPerPage
	if n, err := strconv.Atoi(q.Get("per_page")); err == nil {
		perPage = clampPerPage(n)
	}

	total := len(items)
	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}

	page := 1
	if n, err := strconv.Atoi(q.Get(param)); err == nil && n > 1 {
		page = n
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * perPage
	end := start + perPage
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	p := Pagination{
		Page:        page,
		PerPage:     perPage,
		Total:       total,
		TotalPages:  totalPages,
		From:        start + 1,
		To:          end,
		Pages:       pageWindow(page, totalPages),
		PerPageOpts: perPageChoices,
		Query:       carryQuery(q, param),
		Carried:     carriedPairs(q, param),
		Param:       param,
	}
	if total == 0 {
		p.From = 0
	}
	if page > 1 {
		p.Prev = page - 1
	}
	if page < totalPages {
		p.Next = page + 1
	}
	return items[start:end], p
}

// clampPerPage snaps a requested size to the nearest allowed choice.
func clampPerPage(n int) int {
	for _, c := range perPageChoices {
		if n == c {
			return n
		}
	}
	if n < perPageChoices[0] {
		return perPageChoices[0]
	}
	return perPageChoices[len(perPageChoices)-1]
}

// pageWindow returns at most 7 page numbers centred on the current page, so
// the bar stays a fixed width no matter how many pages there are.
func pageWindow(page, totalPages int) []int {
	const window = 7
	start, end := 1, totalPages
	if totalPages > window {
		start = page - window/2
		if start < 1 {
			start = 1
		}
		end = start + window - 1
		if end > totalPages {
			end = totalPages
			start = end - window + 1
		}
	}
	out := make([]int, 0, end-start+1)
	for i := start; i <= end; i++ {
		out = append(out, i)
	}
	return out
}

// Link is the href for one page of this list. It is built here rather than
// concatenated in the template because html/template escapes an interpolated
// query fragment inside an href, which would turn the carried "&" separators
// into "%26" and drop every filter.
func (p Pagination) Link(page int) template.URL {
	return template.URL("?" + p.Query + p.Param + "=" + strconv.Itoa(page))
}

// CarriedPairs exposes Carried to the pagination partial, which renders one
// hidden input per entry so the rows-per-page form keeps the active filters.
func (p Pagination) CarriedPairs() map[string]string { return p.Carried }

// carriedPairs is carryQuery's map form: every parameter that must survive a
// rows-per-page change, which resets the page and replaces per_page.
func carriedPairs(q url.Values, param string) map[string]string {
	out := make(map[string]string, len(q))
	for k, vs := range q {
		if k == param || k == "per_page" || len(vs) == 0 {
			continue
		}
		out[k] = vs[0]
	}
	return out
}

// carryQuery re-encodes every query parameter except the page one, so
// following a page link keeps the active filters. The result ends in "&"
// when non-empty, ready to be concatenated with "page=N".
func carryQuery(q url.Values, param string) string {
	out := url.Values{}
	for k, vs := range q {
		if k == param {
			continue
		}
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return out.Encode() + "&"
}
