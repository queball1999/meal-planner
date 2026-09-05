# Commit Plan — meal-planner (Phase 2: Preferences + LLM)

**8 commits, continuing from the Phase 1 branch.**  
`go build ./...` and `go test ./...` pass after commit 8 (the final one).

Exclude from every `git add`:

- `bin/` (build artefact)
- `data/` (SQLite runtime files — WAL, SHM)

---

## 1. ✅ feat(db): add Phase 2 migration and models

New migration, model structs, params types, and store interface additions for
stores, preferences, allergies, meal slot hints, meal feedback, and AI runs.

**Stage:**
```
git add db/migrations/00003_preferences.sql db/models.go db/store.go
```

**Commit message:**
```
feat(db): add Phase 2 migration and models

- Migration 00003: stores, preferences, allergies, meal_slot_hints,
  meal_feedback, ai_runs tables with FK→households ON DELETE CASCADE.
- diet_tags, cuisines, dislikes, tags_snapshot stored as JSON text arrays.
- meal_slot_hints has UNIQUE(household_id, slot) + slot CHECK constraint.
- meal_feedback.rating CHECK(rating IN (-1, 1)).
- New model structs: GroceryStore, Preferences, MealSlotHint, MealFeedback, AIRun.
- New params types: UpsertStoreParams, UpsertPreferencesParams,
  UpsertMealSlotHintParams, CreateFeedbackParams, CreateAIRunParams.
- Store interface extended with 12 new method signatures.
```

---

## 2. ✅ feat(db): implement Phase 2 store query files

Six new query files implementing the Phase 2 store interface methods.

**Stage:**
```
git add db/stores.go db/preferences.go db/allergies.go db/meal_slot_hints.go db/meal_feedback.go db/ai_runs.go
```

**Commit message:**
```
feat(db): implement Phase 2 store query files

- db/stores.go: CreateStore, ListStores, DeleteStore; storeScanner
  interface shared with meal_slot_hints.go.
- db/preferences.go: UpsertPreferences (INSERT … ON CONFLICT DO UPDATE);
  GetPreferences returns zero-value defaults when no row exists yet.
- db/allergies.go: SetAllergies via transaction (DELETE all + INSERT OR IGNORE);
  ListAllergies returns alphabetically ordered list.
- db/meal_slot_hints.go: UpsertMealSlotHint (ON CONFLICT DO UPDATE);
  GetMealSlotHints always returns exactly 3 slots in fixed order.
- db/meal_feedback.go: CreateFeedback, ListFeedbackDigest (most-recent first).
- db/ai_runs.go: CreateAIRun with re-query for full row.
```

---

## 3. ✅ feat(config): add LLM provider configuration fields

New environment-variable fields for LLM provider selection and credentials.

**Stage:**
```
git add config/config.go
```

**Commit message:**
```
feat(config): add LLM provider configuration fields

- PROVIDER, ANTHROPIC_API_KEY, ANTHROPIC_MODEL (default claude-sonnet-5).
- LLM_API_URL, LLM_MODEL, LLM_API_KEY, LLM_MAX_TOKENS (default 4096),
  LLM_TEMPERATURE (default 0.7) for OpenAI-compatible endpoints.
- Provider field left empty → LLM disabled; no required-field error.
- .env.example LLM vars were already committed in Phase 1; no change needed.
```

---

## 4. ✅ feat(llm): add LLM package — Generator interface, clients, free-text parser

Full LLM abstraction layer: interface, Anthropic native SDK client,
OpenAI-compatible HTTP client, provider factory, and free-text meal parser.

**Stage:**
```
git add llm/generator.go llm/anthropic.go llm/openai.go llm/provider.go llm/freetext.go
```

**Commit message:**
```
feat(llm): add LLM package with Generator interface and free-text parser

- llm/generator.go: Generator interface (Generate, ProviderName, ModelName),
  GenerateRequest/GenerateResponse types.
- llm/anthropic.go: native Anthropic SDK client (anthropic-sdk-go v1.71.0);
  maps system prompt to []TextBlockParam.
- llm/openai.go: hand-rolled OpenAI Chat Completions client with 120 s
  timeout and 4 MB response body cap; supports any OpenAI-compatible base URL.
- llm/provider.go: NewGenerator factory routing anthropic / openai / google /
  openai_compatible based on PROVIDER env var.
- llm/freetext.go: ParseMealDescription extracts candidate_foods, staples,
  frequency from a meal slot description; validates JSON before storing;
  returns "null" on empty input or invalid response.
```

---

## 5. ✅ feat(web): extend Server with LLM generator and add Phase 2 routes

Update server constructor and register preference/store routes.

**Stage:**
```
git add web/server.go web/routes.go
```

**Commit message:**
```
feat(web): extend Server with LLM generator and add Phase 2 routes

- Server struct gets gen llm.Generator field (nil when provider not configured).
- NewServer signature updated: NewServer(cfg, store, gen, version).
- Routes: GET/POST /preferences, GET /stores, POST /stores,
  POST /stores/{id}/delete — all behind requireAuth.
```

---

## 6. ✅ feat(web): add Phase 2 handlers — preferences and stores

Handlers for the preferences form (with LLM free-text parse) and store CRUD.

**Stage:**
```
git add web/handlers_preferences.go web/handlers_stores.go
```

**Commit message:**
```
feat(web): add Phase 2 handlers — preferences and stores

- handlers_preferences.go: GET /preferences renders pre-computed checkbox
  state (diet tags, cuisines) and slot hint rows; POST saves structured prefs,
  allergies, and meal slot hints, triggering LLM parse when generator is
  configured and text changed; logs AI run for cost tracking.
- handlers_stores.go: GET /stores lists stores; POST /stores creates with
  name + kind; POST /stores/{id}/delete removes by ID.
- splitTrimmed helper for comma-separated fields; sliceToSet for checkbox state.
```

---

## 7. ✅ feat(web): add Phase 2 icons and templates

New SVG icons and the preferences/stores page templates; update dashboard.

**Stage:**
```
git add web/icons.go web/templates/preferences.html web/templates/stores.html web/templates/index.html
```

**Commit message:**
```
feat(web): add Phase 2 icons and templates

- icons.go: store, thumb-up, thumb-down, plus, tag-multiple (MDI paths).
- preferences.html: meal slot hints with effort selector; diet-tag and cuisine
  checkbox grids pre-computed server-side; allergies and dislikes as
  comma-separated text inputs; leftover-tolerance checkbox.
- stores.html: store list with per-store delete form; add-store form with
  name and kind selector.
- index.html: update Phase 2 badge to ✓ and add setup links to dashboard.
```

---

## 8. ✅ feat: wire LLM generator into main and update commit plan

Update main.go to initialise the optional LLM generator; record Phase 2 plan.

**Stage:**
```
git add main.go spec/commit-plan-phase2-09-05-26.md
```

**Commit message:**
```
feat: wire LLM generator into main and record Phase 2 commit plan

- main.go: call llm.NewGenerator when PROVIDER is set; log provider/model on
  success; skip gracefully on error or empty provider.
- spec/commit-plan-phase2-09-05-26.md: 8-commit plan for Phase 2 work.
```
