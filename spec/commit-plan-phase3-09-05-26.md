# Phase 3 Commit Plan — AI Generation
Date: 2026-09-05

## Commits

### 1. db: add plans, meals, recipes, ingredients migration
Files: `db/migrations/00004_plans.sql`
```
db: add plans, meals, recipes, and ingredients migration
```
Status: ✅

### 2. db: Plan, Meal, MealRecipe, MealIngredient models and store interface
Files: `db/models.go`, `db/store.go`
```
db: add Plan, Meal, MealRecipe, MealIngredient models and store interface
```
Status: ✅

### 3. db: plan and meal query implementations
Files: `db/plans.go`, `db/meals.go`, `db/meal_recipes.go`, `db/meal_ingredients.go`
```
db: implement plan and meal query methods
```
Status: ✅

### 4. plan: preference resolver and generated types
Files: `plan/types.go`, `plan/resolver.go`
```
plan: add PreferenceProfile, GeneratedPlan types and preference resolver
```
Status: ✅

### 5. plan: prompt builder and validator
Files: `plan/prompt.go`, `plan/validate.go`
```
plan: add prompt builder and plan validator
```
Status: ✅

### 6. plan: generation call site and job manager
Files: `plan/generate.go`, `plan/job.go`
```
plan: add Generate call site and JobManager for async generation
```
Status: ✅

### 7. web: wire plan routes, handlers, and SSE status stream
Files: `web/server.go`, `web/routes.go`, `web/handlers_plan.go`, `web/icons.go`
```
web: add plan calendar, generation trigger, and SSE status stream
```
Status: ✅

### 8. web: plan calendar and generation progress templates
Files: `web/templates/plan.html`, `web/templates/plan_generate.html`
```
web: add plan calendar and generation progress templates
```
Status: ✅
