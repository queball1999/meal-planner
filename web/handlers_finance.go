package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goeat/db"
	"goeat/llm"
	"goeat/middleware"
	"goeat/plan"
)

// ── Finance dashboard ─────────────────────────────────────────────────────────
//
// Three sub-tabs under one nav section:
//   - Overview: week-over-week actual vs estimated spend, top items, by store.
//   - AI usage & cost: token/cost rollups by purpose and model, a trend chart,
//     and the pricing-reference + model-mapping editor (the fix lives next to
//     the problem, per the slack-llm-proxy reference).
//   - Price tracking: the household's item→store price map with history.
//
// "Actual" spend is what has been bought (checked-off lines); "estimate" is the
// full list. Unpriced AI models are reported as unpriced, never as $0.00.

// financeRange parses ?from / ?to (YYYY-MM-DD) or defaults to the trailing N
// weeks ending on the current week.
func financeRange(r *http.Request, weekStartDay string, defaultWeeks int) (from, to string) {
	now := time.Now().UTC()
	curStart, _ := plan.WeekBounds(now, weekStartDay)
	defFrom := curStart.AddDate(0, 0, -7*(defaultWeeks-1))
	defTo := curStart.AddDate(0, 0, 6)

	from = defFrom.Format("2006-01-02")
	to = defTo.Format("2006-01-02")
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from = t.UTC().Format("2006-01-02")
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to = t.UTC().Format("2006-01-02")
		}
	}
	return from, to
}

// ── Overview ──────────────────────────────────────────────────────────────────

type financeOverviewData struct {
	From, To string

	ActualLabel   string
	EstimateLabel string
	BudgetLabel   string
	OverBudget    bool

	Weeks      []financeWeekRow
	TopItems   []financeItemRow
	StoreSpend []financeStoreRow
}

type financeWeekRow struct {
	Label         string
	ActualLabel   string
	EstimateLabel string
	ActualCents   int64
	EstimateCents int64
}

type financeItemRow struct {
	Name          string
	ActualLabel   string
	EstimateLabel string
}

type financeStoreRow struct {
	Name          string
	ActualLabel   string
	EstimateLabel string
}

func (s *Server) handleFinanceOverview(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	from, to := financeRange(r, s.cfg.WeekStartDay, 8)

	actual, estimate, budget, _ := s.store.GetFinanceTotals(ctx, hh.ID, from, to)
	weeks, _ := s.store.GetWeeklySpend(ctx, hh.ID, from, to)
	topItems, _ := s.store.GetTopSpentItems(ctx, hh.ID, from, to, 10)
	storeSpend, _ := s.store.GetStoreSpend(ctx, hh.ID, from, to)

	data := financeOverviewData{
		From:          from,
		To:            to,
		ActualLabel:   fmt.Sprintf("$%.2f", float64(actual)/100),
		EstimateLabel: fmt.Sprintf("$%.2f", float64(estimate)/100),
		BudgetLabel:   fmt.Sprintf("$%.0f", float64(budget)/100),
		OverBudget:    actual > budget && budget > 0,
	}
	for _, w := range weeks {
		data.Weeks = append(data.Weeks, financeWeekRow{
			Label:         db.FinanceWeekLabel(w.WeekStart),
			ActualLabel:   fmt.Sprintf("$%.2f", float64(w.ActualCents)/100),
			EstimateLabel: fmt.Sprintf("$%.2f", float64(w.EstimateCents)/100),
			ActualCents:   w.ActualCents,
			EstimateCents: w.EstimateCents,
		})
	}
	for _, it := range topItems {
		data.TopItems = append(data.TopItems, financeItemRow{
			Name:          it.DisplayName,
			ActualLabel:   fmt.Sprintf("$%.2f", float64(it.ActualCents)/100),
			EstimateLabel: fmt.Sprintf("$%.2f", float64(it.EstimateCents)/100),
		})
	}
	for _, st := range storeSpend {
		data.StoreSpend = append(data.StoreSpend, financeStoreRow{
			Name:          st.StoreName,
			ActualLabel:   fmt.Sprintf("$%.2f", float64(st.ActualCents)/100),
			EstimateLabel: fmt.Sprintf("$%.2f", float64(st.EstimateCents)/100),
		})
	}

	s.renderWithPage(w, r, "finance_overview", "finance", data)
}

// ── AI usage & cost ───────────────────────────────────────────────────────────

type financeAIData struct {
	From, To string

	// Summary strip.
	RunCount      int
	TotalTokens   int64
	EstCostLabel  string
	UnpricedCount int
	HasUnpriced   bool

	ByPurpose  []financePurposeRow
	ByModel    []financeModelRow
	Daily      []financeDailyRow
	RecentRuns []financeRunRow

	// Pricing editor.
	References []financeRefRow
	Mappings   []financeMappingRow
	// Unmapped is the set of local model labels seen in usage with no mapping.
	Unmapped []string
}

type financePurposeRow struct {
	Purpose      string
	RunCount     int
	TotalTokens  int64
	EstCostLabel string
}

type financeModelRow struct {
	Provider       string
	Model          string
	RunCount       int
	TotalTokens    int64
	EstCostLabel   string
	Priced         bool
	ReferenceModel string
}

type financeDailyRow struct {
	Date         string
	Label        string
	RunCount     int
	TotalTokens  int64
	EstCostCents int64
}

type financeRunRow struct {
	When         string
	Purpose      string
	Provider     string
	Model        string
	PromptTokens int
	Completion   int
	EstCostLabel string
	Status       string
}

type financeRefRow struct {
	ID             int64
	Provider       string
	ReferenceModel string
	InputLabel     string // "$5.00 / M"
	OutputLabel    string
	EffectiveDate  string
	Notes          string
}

type financeMappingRow struct {
	LocalModel     string
	ReferenceID    int64
	Provider       string
	ReferenceModel string
}

func (s *Server) handleFinanceAI(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	from, to := financeRange(r, s.cfg.WeekStartDay, 8)

	totals, _ := s.store.GetAIRunTotals(ctx, hh.ID, from, to)
	byPurpose, _ := s.store.ListAIRunsByPurpose(ctx, hh.ID, from, to)
	byModel, _ := s.store.ListAIRunsByModel(ctx, hh.ID, from, to)
	daily, _ := s.store.ListAIRunsDaily(ctx, hh.ID, from, to)
	recent, _ := s.store.ListAIRuns(ctx, hh.ID, 25)

	// Resolve reference prices for the models seen in usage.
	modelLabels := make([]string, 0, len(byModel))
	for _, m := range byModel {
		modelLabels = append(modelLabels, m.Model)
	}
	pricing, _ := s.store.PricingForModels(ctx, modelLabels)
	estimates := llm.EstimateCosts(byModel, pricing)
	estByModel := make(map[string]*llm.CostEstimate, len(estimates))
	for _, e := range estimates {
		estByModel[e.Model] = e
	}
	_, unpricedCount := llm.SummariseCosts(estimates)

	data := financeAIData{
		From:          from,
		To:            to,
		RunCount:      totals.RunCount,
		TotalTokens:   totals.TotalTokens,
		UnpricedCount: unpricedCount,
		HasUnpriced:   unpricedCount > 0,
	}
	// EstCostLabel: prefer the reference-priced total when any model is mapped;
	// otherwise fall back to the stored est_cost_cents (which is 0 today).
	if unpricedCount < len(byModel) {
		estCents, _ := llm.SummariseCosts(estimates)
		data.EstCostLabel = fmt.Sprintf("$%.2f", float64(estCents)/100)
	} else {
		data.EstCostLabel = fmt.Sprintf("$%.2f", float64(totals.EstCostCents)/100)
	}

	for _, p := range byPurpose {
		data.ByPurpose = append(data.ByPurpose, financePurposeRow{
			Purpose:      p.Purpose,
			RunCount:     p.RunCount,
			TotalTokens:  p.TotalTokens,
			EstCostLabel: fmt.Sprintf("$%.2f", float64(p.EstCostCents)/100),
		})
	}
	for _, m := range byModel {
		row := financeModelRow{
			Provider:     m.Provider,
			Model:        m.Model,
			RunCount:     m.RunCount,
			TotalTokens:  m.TotalTokens,
			EstCostLabel: fmt.Sprintf("$%.2f", float64(m.EstCostCents)/100),
		}
		if e, ok := estByModel[m.Model]; ok && e.Priced {
			row.Priced = true
			row.ReferenceModel = e.ReferenceModel
			row.EstCostLabel = fmt.Sprintf("$%.2f", float64(e.EstimatedCents)/100)
		}
		data.ByModel = append(data.ByModel, row)
	}
	for _, d := range daily {
		data.Daily = append(data.Daily, financeDailyRow{
			Date:         d.Date,
			Label:        db.FinanceWeekLabel(d.Date),
			RunCount:     d.RunCount,
			TotalTokens:  d.TotalTokens,
			EstCostCents: d.EstCostCents,
		})
	}
	for _, run := range recent {
		data.RecentRuns = append(data.RecentRuns, financeRunRow{
			When:         run.CreatedAt.Format("Jan 2, 15:04"),
			Purpose:      run.Purpose,
			Provider:     run.Provider,
			Model:        run.Model,
			PromptTokens: run.PromptTokens,
			Completion:   run.CompletionTokens,
			EstCostLabel: fmt.Sprintf("$%.2f", float64(run.EstCostCents)/100),
			Status:       run.Status,
		})
	}

	// Pricing editor state.
	refs, _ := s.store.ListPricingReferences(ctx)
	for _, ref := range refs {
		data.References = append(data.References, financeRefRow{
			ID:             ref.ID,
			Provider:       ref.Provider,
			ReferenceModel: ref.ReferenceModel,
			InputLabel:     fmt.Sprintf("$%.2f / M", ref.InputPricePerMillion),
			OutputLabel:    fmt.Sprintf("$%.2f / M", ref.OutputPricePerMillion),
			EffectiveDate:  ref.EffectiveDate,
			Notes:          ref.Notes,
		})
	}
	mappings, _ := s.store.ListModelCostMappings(ctx)
	for _, m := range mappings {
		data.Mappings = append(data.Mappings, financeMappingRow{
			LocalModel:     m.LocalModel,
			ReferenceID:    m.PricingReferenceID,
			Provider:       m.Provider,
			ReferenceModel: m.ReferenceModel,
		})
	}
	mapped := make(map[string]bool, len(mappings))
	for _, m := range mappings {
		mapped[m.LocalModel] = true
	}
	for _, label := range modelLabels {
		if !mapped[label] {
			data.Unmapped = append(data.Unmapped, label)
		}
	}

	s.renderWithPage(w, r, "finance_ai", "finance", data)
}

// ── Price tracking ────────────────────────────────────────────────────────────

type financePriceData struct {
	Rows []financePriceRow
}

type financePriceRow struct {
	ItemName         string
	StoreName        string
	PriceLabel       string
	Unit             string
	AmountPerPackage float64
	Preferred        bool
	UpdatedAt        string
	// Spark is the item's recent price history as a simple "x,y" polyline.
	SparkPoints string
	SparkCount  int
}

func (s *Server) handleFinancePrices(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	rows, _ := s.store.ListPriceTracking(ctx, hh.ID)

	data := financePriceData{}
	for _, row := range rows {
		fr := financePriceRow{
			ItemName:         row.ItemName,
			StoreName:        row.StoreName,
			PriceLabel:       fmt.Sprintf("$%.2f", float64(row.PriceCents)/100),
			Unit:             row.PurchaseUnit,
			AmountPerPackage: row.AmountPerPackage,
			Preferred:        row.Preferred,
			UpdatedAt:        row.UpdatedAt.Format("Jan 2, 2006"),
		}
		if len(row.History) >= 2 {
			fr.SparkPoints = buildPriceSparkline(row.History)
			fr.SparkCount = len(row.History)
		}
		data.Rows = append(data.Rows, fr)
	}

	s.renderWithPage(w, r, "finance_prices", "finance", data)
}

// ── Pricing editor mutations ──────────────────────────────────────────────────

func (s *Server) handleFinanceRefSave(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	provider := strings.TrimSpace(r.FormValue("provider"))
	refModel := strings.TrimSpace(r.FormValue("reference_model"))
	inStr := strings.TrimSpace(r.FormValue("input_per_million"))
	outStr := strings.TrimSpace(r.FormValue("output_per_million"))
	notes := strings.TrimSpace(r.FormValue("notes"))

	if provider == "" || refModel == "" {
		s.setNotify(w, NotifyDanger, "Provider and reference model are required.")
		http.Redirect(w, r, "/finance/ai", http.StatusSeeOther)
		return
	}
	in, err1 := strconv.ParseFloat(inStr, 64)
	out, err2 := strconv.ParseFloat(outStr, 64)
	if err1 != nil || err2 != nil || in < 0 || out < 0 {
		s.setNotify(w, NotifyDanger, "Enter non-negative per-million prices.")
		http.Redirect(w, r, "/finance/ai", http.StatusSeeOther)
		return
	}

	if _, err := s.store.UpsertPricingReference(r.Context(), db.UpsertPricingReferenceParams{
		Provider:              provider,
		ReferenceModel:        refModel,
		InputPricePerMillion:  in,
		OutputPricePerMillion: out,
		Notes:                 notes,
	}); err != nil {
		s.setNotify(w, NotifyDanger, "Couldn't save the reference price.")
	} else {
		s.setNotify(w, NotifySuccess, "Reference price saved.")
	}
	http.Redirect(w, r, "/finance/ai", http.StatusSeeOther)
}

func (s *Server) handleFinanceRefDelete(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.setNotify(w, NotifyDanger, "Invalid reference.")
		http.Redirect(w, r, "/finance/ai", http.StatusSeeOther)
		return
	}
	if err := s.store.DeletePricingReference(r.Context(), id); err != nil {
		s.setNotify(w, NotifyDanger, err.Error())
	} else {
		s.setNotify(w, NotifySuccess, "Reference price removed.")
	}
	http.Redirect(w, r, "/finance/ai", http.StatusSeeOther)
}

func (s *Server) handleFinanceMappingSave(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	localModel := strings.TrimSpace(r.FormValue("local_model"))
	refIDStr := strings.TrimSpace(r.FormValue("reference_id"))
	if localModel == "" {
		s.setNotify(w, NotifyDanger, "Enter the local model label to map.")
		http.Redirect(w, r, "/finance/ai", http.StatusSeeOther)
		return
	}
	if refIDStr == "" {
		// Empty reference = unmap.
		if err := s.store.DeleteModelCostMapping(r.Context(), localModel); err != nil {
			s.setNotify(w, NotifyDanger, "Couldn't remove the mapping.")
		} else {
			s.setNotify(w, NotifySuccess, "Mapping removed.")
		}
		http.Redirect(w, r, "/finance/ai", http.StatusSeeOther)
		return
	}
	refID, err := strconv.ParseInt(refIDStr, 10, 64)
	if err != nil {
		s.setNotify(w, NotifyDanger, "Pick a reference price.")
		http.Redirect(w, r, "/finance/ai", http.StatusSeeOther)
		return
	}
	if err := s.store.SetModelCostMapping(r.Context(), localModel, refID); err != nil {
		s.setNotify(w, NotifyDanger, "Couldn't save the mapping.")
	} else {
		s.setNotify(w, NotifySuccess, "Mapping saved.")
	}
	http.Redirect(w, r, "/finance/ai", http.StatusSeeOther)
}

// buildPriceSparkline turns a price history into a "x,y x,y ..." polyline in a
// 100x28 viewBox, oldest left. Reuses the app's inline-SVG convention.
func buildPriceSparkline(hist []db.PriceHistoryEntry) string {
	const w, h, pad = 100.0, 28.0, 3.0
	minV, maxV := float64(hist[0].PriceCents), float64(hist[0].PriceCents)
	for _, e := range hist {
		v := float64(e.PriceCents)
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := maxV - minV
	var b strings.Builder
	for i, e := range hist {
		x := pad
		if len(hist) > 1 {
			x += (w - 2*pad) * float64(i) / float64(len(hist)-1)
		}
		y := pad + (h-2*pad)/2
		if span > 0 {
			y = pad + (h-2*pad)*(1-(float64(e.PriceCents)-minV)/span)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	return b.String()
}
