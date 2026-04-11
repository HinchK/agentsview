# Usage Cost Tracking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `agentsview usage` subcommand that calculates daily token costs
for Claude Code and Codex sessions using pricing data and the existing messages
table.

**Architecture:** Query messages table for token_usage JSON blobs, join with a
new model_pricing table for per-model rates, aggregate by date. Pricing fetched
from LiteLLM GitHub on sync, with embedded fallback for offline use. CLI outputs
JSON (VibePulse-compatible) or terminal table.

**Tech Stack:** Go stdlib, SQLite json_extract(), existing db/config packages,
embedded pricing JSON fallback.

______________________________________________________________________

## File Map

| Action | Path                               | Responsibility                                  |
| ------ | ---------------------------------- | ----------------------------------------------- |
| Create | `internal/db/usage.go`             | Daily cost aggregation queries                  |
| Create | `internal/db/usage_test.go`        | Tests for cost aggregation                      |
| Create | `internal/db/pricing.go`           | model_pricing table DDL, upsert, lookup         |
| Create | `internal/db/pricing_test.go`      | Tests for pricing storage                       |
| Create | `internal/pricing/litellm.go`      | LiteLLM JSON fetch + parse                      |
| Create | `internal/pricing/litellm_test.go` | Tests for LiteLLM parsing                       |
| Create | `internal/pricing/fallback.go`     | Embedded fallback pricing map                   |
| Create | `cmd/agentsview/usage.go`          | CLI subcommand: flag parsing, output formatting |
| Create | `cmd/agentsview/usage_test.go`     | Tests for CLI output formatting                 |
| Modify | `cmd/agentsview/main.go:37-71`     | Add "usage" case to command dispatch            |
| Modify | `cmd/agentsview/main.go:77-170`    | Add usage help text to printUsage()             |
| Modify | `internal/db/db.go:264-304`        | Add model_pricing to migrateColumns()           |
| Modify | `internal/db/schema.sql`           | Add model_pricing CREATE TABLE                  |

______________________________________________________________________

### Task 1: model_pricing Table Schema and Migration

**Files:**

- Modify: `internal/db/schema.sql`

- Modify: `internal/db/db.go:264-304`

- Create: `internal/db/pricing.go`

- Create: `internal/db/pricing_test.go`

- [ ] **Step 1: Write failing test for model_pricing table existence**

```go
// internal/db/pricing_test.go
package db

import "testing"

func TestMigrationCreatesModelPricingTable(t *testing.T) {
	d := testDB(t)
	w := d.getWriter()

	var count int
	err := w.QueryRow(
		"SELECT count(*) FROM sqlite_master" +
			" WHERE type='table' AND name='model_pricing'",
	).Scan(&count)
	requireNoError(t, err, "probing model_pricing")
	if count != 1 {
		t.Error("expected model_pricing table to exist")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
`cd /Users/wesm/code/agentsview && CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestMigrationCreatesModelPricingTable -v`
Expected: FAIL — table does not exist

- [ ] **Step 3: Add model_pricing table to schema.sql**

Append to `internal/db/schema.sql`:

```sql
-- Model pricing for cost calculation
CREATE TABLE IF NOT EXISTS model_pricing (
    model_pattern    TEXT PRIMARY KEY,
    input_per_mtok   REAL NOT NULL DEFAULT 0,
    output_per_mtok  REAL NOT NULL DEFAULT 0,
    cache_creation_per_mtok REAL NOT NULL DEFAULT 0,
    cache_read_per_mtok     REAL NOT NULL DEFAULT 0,
    updated_at       TEXT NOT NULL
        DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
```

Note: prices stored per million tokens (matches LiteLLM format).

- [ ] **Step 4: Run test to verify it passes**

Run:
`cd /Users/wesm/code/agentsview && CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestMigrationCreatesModelPricingTable -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/wesm/code/agentsview
git add internal/db/schema.sql internal/db/pricing.go internal/db/pricing_test.go
git commit -m "feat: add model_pricing table for usage cost tracking"
```

______________________________________________________________________

### Task 2: Pricing Storage Functions

**Files:**

- Modify: `internal/db/pricing.go`

- Modify: `internal/db/pricing_test.go`

- [ ] **Step 1: Write failing test for UpsertModelPricing**

```go
// append to internal/db/pricing_test.go

func TestUpsertModelPricing(t *testing.T) {
	d := testDB(t)

	err := d.UpsertModelPricing([]ModelPricing{
		{
			ModelPattern:        "claude-sonnet-4-20250514",
			InputPerMTok:        3.0,
			OutputPerMTok:       15.0,
			CacheCreationPerMTok: 3.75,
			CacheReadPerMTok:    0.30,
		},
	})
	requireNoError(t, err, "upsert pricing")

	prices, err := d.GetModelPricing("claude-sonnet-4-20250514")
	requireNoError(t, err, "get pricing")
	if prices == nil {
		t.Fatal("expected pricing, got nil")
	}
	if prices.InputPerMTok != 3.0 {
		t.Errorf("InputPerMTok = %f, want 3.0",
			prices.InputPerMTok)
	}
	if prices.OutputPerMTok != 15.0 {
		t.Errorf("OutputPerMTok = %f, want 15.0",
			prices.OutputPerMTok)
	}
}

func TestUpsertModelPricingOverwrites(t *testing.T) {
	d := testDB(t)

	initial := []ModelPricing{{
		ModelPattern:  "claude-opus-4-20250514",
		InputPerMTok:  15.0,
		OutputPerMTok: 75.0,
	}}
	requireNoError(t, d.UpsertModelPricing(initial), "first upsert")

	updated := []ModelPricing{{
		ModelPattern:  "claude-opus-4-20250514",
		InputPerMTok:  10.0,
		OutputPerMTok: 50.0,
	}}
	requireNoError(t, d.UpsertModelPricing(updated), "second upsert")

	prices, err := d.GetModelPricing("claude-opus-4-20250514")
	requireNoError(t, err, "get pricing")
	if prices.InputPerMTok != 10.0 {
		t.Errorf("InputPerMTok = %f, want 10.0 after update",
			prices.InputPerMTok)
	}
}

func TestGetModelPricingNotFound(t *testing.T) {
	d := testDB(t)

	prices, err := d.GetModelPricing("nonexistent-model")
	requireNoError(t, err, "get missing pricing")
	if prices != nil {
		t.Errorf("expected nil for unknown model, got %+v", prices)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
`cd /Users/wesm/code/agentsview && CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestUpsertModelPricing -v`
Expected: FAIL — functions not defined

- [ ] **Step 3: Implement ModelPricing type and storage functions**

```go
// internal/db/pricing.go
package db

import "fmt"

// ModelPricing holds per-model token pricing in cost per million tokens.
type ModelPricing struct {
	ModelPattern         string  `json:"model_pattern"`
	InputPerMTok         float64 `json:"input_per_mtok"`
	OutputPerMTok        float64 `json:"output_per_mtok"`
	CacheCreationPerMTok float64 `json:"cache_creation_per_mtok"`
	CacheReadPerMTok     float64 `json:"cache_read_per_mtok"`
	UpdatedAt            string  `json:"updated_at"`
}

// UpsertModelPricing inserts or replaces pricing rows.
func (db *DB) UpsertModelPricing(prices []ModelPricing) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	w := db.getWriter()

	tx, err := w.Begin()
	if err != nil {
		return fmt.Errorf("begin pricing upsert: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO model_pricing
			(model_pattern, input_per_mtok, output_per_mtok,
			 cache_creation_per_mtok, cache_read_per_mtok,
			 updated_at)
		VALUES (?, ?, ?, ?, ?,
			strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(model_pattern) DO UPDATE SET
			input_per_mtok = excluded.input_per_mtok,
			output_per_mtok = excluded.output_per_mtok,
			cache_creation_per_mtok = excluded.cache_creation_per_mtok,
			cache_read_per_mtok = excluded.cache_read_per_mtok,
			updated_at = excluded.updated_at`)
	if err != nil {
		return fmt.Errorf("prepare pricing upsert: %w", err)
	}
	defer stmt.Close()

	for _, p := range prices {
		if _, err := stmt.Exec(
			p.ModelPattern, p.InputPerMTok, p.OutputPerMTok,
			p.CacheCreationPerMTok, p.CacheReadPerMTok,
		); err != nil {
			return fmt.Errorf(
				"upsert pricing %s: %w", p.ModelPattern, err)
		}
	}
	return tx.Commit()
}

// GetModelPricing returns pricing for an exact model name.
// Returns nil if no pricing exists.
func (db *DB) GetModelPricing(
	model string,
) (*ModelPricing, error) {
	r := db.getReader()
	var p ModelPricing
	err := r.QueryRow(`
		SELECT model_pattern, input_per_mtok, output_per_mtok,
			cache_creation_per_mtok, cache_read_per_mtok, updated_at
		FROM model_pricing WHERE model_pattern = ?`, model,
	).Scan(
		&p.ModelPattern, &p.InputPerMTok, &p.OutputPerMTok,
		&p.CacheCreationPerMTok, &p.CacheReadPerMTok, &p.UpdatedAt,
	)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("get pricing %s: %w", model, err)
	}
	return &p, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
`cd /Users/wesm/code/agentsview && CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestUpsertModelPricing -v && CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestGetModelPricing -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/wesm/code/agentsview
git add internal/db/pricing.go internal/db/pricing_test.go
git commit -m "feat: add model pricing storage functions"
```

______________________________________________________________________

### Task 3: LiteLLM Pricing Fetcher

**Files:**

- Create: `internal/pricing/litellm.go`

- Create: `internal/pricing/litellm_test.go`

- Create: `internal/pricing/fallback.go`

- [ ] **Step 1: Write failing test for ParseLiteLLMPricing**

```go
// internal/pricing/litellm_test.go
package pricing

import (
	"testing"
)

func TestParseLiteLLMPricing(t *testing.T) {
	raw := []byte(`{
		"claude-sonnet-4-20250514": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_creation_input_token_cost": 0.00000375,
			"cache_read_input_token_cost": 0.0000003,
			"litellm_provider": "anthropic"
		},
		"gpt-4o": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.000015,
			"litellm_provider": "openai"
		}
	}`)

	prices, err := ParseLiteLLMPricing(raw)
	if err != nil {
		t.Fatalf("ParseLiteLLMPricing: %v", err)
	}

	// Should include claude model
	found := false
	for _, p := range prices {
		if p.ModelPattern == "claude-sonnet-4-20250514" {
			found = true
			// 0.000003 per token = 3.0 per million
			if p.InputPerMTok != 3.0 {
				t.Errorf("InputPerMTok = %f, want 3.0",
					p.InputPerMTok)
			}
			if p.OutputPerMTok != 15.0 {
				t.Errorf("OutputPerMTok = %f, want 15.0",
					p.OutputPerMTok)
			}
			if p.CacheCreationPerMTok != 3.75 {
				t.Errorf("CacheCreationPerMTok = %f, want 3.75",
					p.CacheCreationPerMTok)
			}
			if p.CacheReadPerMTok != 0.3 {
				t.Errorf("CacheReadPerMTok = %f, want 0.3",
					p.CacheReadPerMTok)
			}
			break
		}
	}
	if !found {
		t.Error("claude-sonnet-4-20250514 not found in results")
	}
}

func TestParseLiteLLMPricingFiltersProviders(t *testing.T) {
	raw := []byte(`{
		"claude-sonnet-4-20250514": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"litellm_provider": "anthropic"
		},
		"gpt-4o": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.000015,
			"litellm_provider": "openai"
		}
	}`)

	prices, err := ParseLiteLLMPricing(raw)
	if err != nil {
		t.Fatalf("ParseLiteLLMPricing: %v", err)
	}

	// Should include both providers (we don't filter)
	if len(prices) != 2 {
		t.Errorf("expected 2 prices, got %d", len(prices))
	}
}

func TestFallbackPricing(t *testing.T) {
	prices := FallbackPricing()
	if len(prices) == 0 {
		t.Error("fallback pricing is empty")
	}

	// Must include current Claude models
	models := make(map[string]bool)
	for _, p := range prices {
		models[p.ModelPattern] = true
	}

	required := []string{
		"claude-sonnet-4-20250514",
		"claude-opus-4-20250514",
		"claude-haiku-3-5-20241022",
	}
	for _, m := range required {
		if !models[m] {
			t.Errorf("fallback missing required model: %s", m)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
`cd /Users/wesm/code/agentsview && go test ./internal/pricing/ -run TestParseLiteLLM -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement LiteLLM parser**

```go
// internal/pricing/litellm.go
package pricing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const liteLLMURL = "https://raw.githubusercontent.com/" +
	"BerriAI/litellm/main/model_prices_and_context_window.json"

// ModelPricing holds per-model token pricing in cost per million tokens.
type ModelPricing struct {
	ModelPattern         string  `json:"model_pattern"`
	InputPerMTok         float64 `json:"input_per_mtok"`
	OutputPerMTok        float64 `json:"output_per_mtok"`
	CacheCreationPerMTok float64 `json:"cache_creation_per_mtok"`
	CacheReadPerMTok     float64 `json:"cache_read_per_mtok"`
}

// litellmEntry is one model entry in the LiteLLM JSON.
type litellmEntry struct {
	InputCost         *float64 `json:"input_cost_per_token"`
	OutputCost        *float64 `json:"output_cost_per_token"`
	CacheCreationCost *float64 `json:"cache_creation_input_token_cost"`
	CacheReadCost     *float64 `json:"cache_read_input_token_cost"`
	Provider          string   `json:"litellm_provider"`
}

// FetchLiteLLMPricing downloads and parses the LiteLLM pricing file.
func FetchLiteLLMPricing() ([]ModelPricing, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(liteLLMURL)
	if err != nil {
		return nil, fmt.Errorf("fetching litellm pricing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"litellm pricing HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading litellm body: %w", err)
	}
	return ParseLiteLLMPricing(body)
}

// ParseLiteLLMPricing parses the LiteLLM JSON pricing blob.
// Converts per-token costs to per-million-token costs.
func ParseLiteLLMPricing(data []byte) ([]ModelPricing, error) {
	var entries map[string]litellmEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing litellm pricing: %w", err)
	}

	var prices []ModelPricing
	for model, e := range entries {
		if e.InputCost == nil && e.OutputCost == nil {
			continue
		}
		p := ModelPricing{ModelPattern: model}
		if e.InputCost != nil {
			p.InputPerMTok = *e.InputCost * 1_000_000
		}
		if e.OutputCost != nil {
			p.OutputPerMTok = *e.OutputCost * 1_000_000
		}
		if e.CacheCreationCost != nil {
			p.CacheCreationPerMTok = *e.CacheCreationCost * 1_000_000
		}
		if e.CacheReadCost != nil {
			p.CacheReadPerMTok = *e.CacheReadCost * 1_000_000
		}
		prices = append(prices, p)
	}
	return prices, nil
}
```

- [ ] **Step 4: Implement fallback pricing**

```go
// internal/pricing/fallback.go
package pricing

// FallbackPricing returns hardcoded pricing for common Claude
// and Codex models. Used when LiteLLM fetch fails or --offline.
// Prices in USD per million tokens, current as of 2025-05.
func FallbackPricing() []ModelPricing {
	return []ModelPricing{
		{
			ModelPattern:         "claude-sonnet-4-20250514",
			InputPerMTok:         3.0,
			OutputPerMTok:        15.0,
			CacheCreationPerMTok: 3.75,
			CacheReadPerMTok:     0.30,
		},
		{
			ModelPattern:         "claude-opus-4-20250514",
			InputPerMTok:         15.0,
			OutputPerMTok:        75.0,
			CacheCreationPerMTok: 18.75,
			CacheReadPerMTok:     1.50,
		},
		{
			ModelPattern:         "claude-haiku-3-5-20241022",
			InputPerMTok:         0.80,
			OutputPerMTok:        4.0,
			CacheCreationPerMTok: 1.0,
			CacheReadPerMTok:     0.08,
		},
		{
			ModelPattern:         "claude-sonnet-4-5-20250514",
			InputPerMTok:         3.0,
			OutputPerMTok:        15.0,
			CacheCreationPerMTok: 3.75,
			CacheReadPerMTok:     0.30,
		},
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/wesm/code/agentsview && go test ./internal/pricing/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/wesm/code/agentsview
git add internal/pricing/litellm.go internal/pricing/litellm_test.go internal/pricing/fallback.go
git commit -m "feat: add LiteLLM pricing fetcher with fallback"
```

______________________________________________________________________

### Task 4: Daily Cost Aggregation Query

**Files:**

- Create: `internal/db/usage.go`

- Create: `internal/db/usage_test.go`

- [ ] **Step 1: Write failing test for GetDailyUsage**

```go
// internal/db/usage_test.go
package db

import (
	"context"
	"encoding/json"
	"math"
	"testing"
)

func TestGetDailyUsageEmpty(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	result, err := d.GetDailyUsage(ctx, UsageFilter{
		From: "2024-01-01", To: "2024-12-31",
	})
	requireNoError(t, err, "GetDailyUsage empty")
	if len(result.Daily) != 0 {
		t.Errorf("expected 0 daily entries, got %d",
			len(result.Daily))
	}
	if result.Totals.TotalCost != 0 {
		t.Errorf("expected 0 total cost, got %f",
			result.Totals.TotalCost)
	}
}

func TestGetDailyUsageWithData(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	// Insert pricing
	requireNoError(t, d.UpsertModelPricing([]ModelPricing{{
		ModelPattern:         "claude-sonnet-4-20250514",
		InputPerMTok:         3.0,
		OutputPerMTok:        15.0,
		CacheCreationPerMTok: 3.75,
		CacheReadPerMTok:     0.30,
	}}), "upsert pricing")

	// Insert session
	s := Session{
		ID:       "s1",
		Project:  "proj",
		Machine:  "local",
		Agent:    "claude",
		StartedAt: Ptr("2024-06-15T10:00:00Z"),
	}
	requireNoError(t, d.UpsertSession(s), "upsert session")

	// Insert message with token usage
	insertMessages(t, d, Message{
		SessionID:     "s1",
		Ordinal:       0,
		Role:          "assistant",
		Content:       "hello",
		ContentLength: 5,
		Timestamp:     "2024-06-15T10:00:00Z",
		Model:         "claude-sonnet-4-20250514",
		TokenUsage: json.RawMessage(`{
			"input_tokens": 1000,
			"output_tokens": 500,
			"cache_creation_input_tokens": 200,
			"cache_read_input_tokens": 300
		}`),
		ContextTokens:    1500,
		OutputTokens:     500,
		HasContextTokens: true,
		HasOutputTokens:  true,
	})

	result, err := d.GetDailyUsage(ctx, UsageFilter{
		From: "2024-06-01", To: "2024-06-30",
	})
	requireNoError(t, err, "GetDailyUsage")

	if len(result.Daily) != 1 {
		t.Fatalf("expected 1 daily entry, got %d",
			len(result.Daily))
	}

	day := result.Daily[0]
	if day.Date != "2024-06-15" {
		t.Errorf("date = %q, want 2024-06-15", day.Date)
	}
	if day.InputTokens != 1000 {
		t.Errorf("InputTokens = %d, want 1000", day.InputTokens)
	}
	if day.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, want 500", day.OutputTokens)
	}

	// Cost: (1000 * 3.0 + 500 * 15.0 + 200 * 3.75 + 300 * 0.30) / 1M
	// = (3000 + 7500 + 750 + 90) / 1M = 11340 / 1M = 0.01134
	expectedCost := 0.01134
	if math.Abs(day.TotalCost-expectedCost) > 0.0001 {
		t.Errorf("TotalCost = %f, want ~%f",
			day.TotalCost, expectedCost)
	}

	if len(day.ModelsUsed) != 1 ||
		day.ModelsUsed[0] != "claude-sonnet-4-20250514" {
		t.Errorf("ModelsUsed = %v, want [claude-sonnet-4-20250514]",
			day.ModelsUsed)
	}
}

func TestGetDailyUsageAgentFilter(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	requireNoError(t, d.UpsertModelPricing([]ModelPricing{{
		ModelPattern:  "claude-sonnet-4-20250514",
		InputPerMTok:  3.0,
		OutputPerMTok: 15.0,
	}}), "upsert pricing")

	// Claude session
	requireNoError(t, d.UpsertSession(Session{
		ID: "s-claude", Project: "proj", Machine: "local",
		Agent: "claude", StartedAt: Ptr("2024-06-15T10:00:00Z"),
	}), "upsert claude session")
	insertMessages(t, d, Message{
		SessionID: "s-claude", Ordinal: 0, Role: "assistant",
		Content: "hi", ContentLength: 2,
		Timestamp: "2024-06-15T10:00:00Z",
		Model:     "claude-sonnet-4-20250514",
		TokenUsage: json.RawMessage(
			`{"input_tokens":1000,"output_tokens":500}`),
		ContextTokens: 1000, OutputTokens: 500,
		HasContextTokens: true, HasOutputTokens: true,
	})

	// Codex session
	requireNoError(t, d.UpsertSession(Session{
		ID: "s-codex", Project: "proj", Machine: "local",
		Agent: "codex", StartedAt: Ptr("2024-06-15T11:00:00Z"),
	}), "upsert codex session")
	insertMessages(t, d, Message{
		SessionID: "s-codex", Ordinal: 0, Role: "assistant",
		Content: "hi", ContentLength: 2,
		Timestamp: "2024-06-15T11:00:00Z",
		Model:     "claude-sonnet-4-20250514",
		TokenUsage: json.RawMessage(
			`{"input_tokens":2000,"output_tokens":1000}`),
		ContextTokens: 2000, OutputTokens: 1000,
		HasContextTokens: true, HasOutputTokens: true,
	})

	// Filter to claude only
	result, err := d.GetDailyUsage(ctx, UsageFilter{
		From: "2024-06-01", To: "2024-06-30", Agent: "claude",
	})
	requireNoError(t, err, "GetDailyUsage claude")

	if len(result.Daily) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Daily))
	}
	if result.Daily[0].InputTokens != 1000 {
		t.Errorf("filtered InputTokens = %d, want 1000",
			result.Daily[0].InputTokens)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
`cd /Users/wesm/code/agentsview && CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestGetDailyUsage -v`
Expected: FAIL — function not defined

- [ ] **Step 3: Implement GetDailyUsage**

```go
// internal/db/usage.go
package db

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// UsageFilter controls date range and agent filtering for
// usage cost queries.
type UsageFilter struct {
	From     string // YYYY-MM-DD, inclusive
	To       string // YYYY-MM-DD, inclusive
	Agent    string // "claude", "codex", or "" for all
	Timezone string // IANA timezone, "" for UTC
}

// DailyUsageEntry holds aggregated usage for one day.
type DailyUsageEntry struct {
	Date                string           `json:"date"`
	InputTokens         int              `json:"inputTokens"`
	OutputTokens        int              `json:"outputTokens"`
	CacheCreationTokens int              `json:"cacheCreationTokens"`
	CacheReadTokens     int              `json:"cacheReadTokens"`
	TotalCost           float64          `json:"totalCost"`
	ModelsUsed          []string         `json:"modelsUsed"`
	ModelBreakdowns     []ModelBreakdown `json:"modelBreakdowns,omitempty"`
}

// ModelBreakdown holds per-model token and cost data.
type ModelBreakdown struct {
	ModelName           string  `json:"modelName"`
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	Cost                float64 `json:"cost"`
}

// UsageTotals holds aggregate totals across all days.
type UsageTotals struct {
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	TotalCost           float64 `json:"totalCost"`
}

// DailyUsageResult wraps the daily series and totals.
type DailyUsageResult struct {
	Daily  []DailyUsageEntry `json:"daily"`
	Totals UsageTotals       `json:"totals"`
}

// GetDailyUsage aggregates token usage and cost by date.
func (db *DB) GetDailyUsage(
	ctx context.Context, f UsageFilter,
) (DailyUsageResult, error) {
	loc := f.location()

	preds := []string{
		"m.token_usage != ''",
		"s.deleted_at IS NULL",
	}
	var args []any

	if f.From != "" {
		preds = append(preds,
			"s.started_at >= ?")
		args = append(args, f.From+"T00:00:00Z")
	}
	if f.To != "" {
		preds = append(preds,
			"s.started_at <= ?")
		args = append(args, f.To+"T23:59:59Z")
	}
	if f.Agent != "" {
		preds = append(preds, "s.agent = ?")
		args = append(args, f.Agent)
	}

	query := `
		SELECT
			COALESCE(m.timestamp, s.started_at) as ts,
			m.model,
			COALESCE(json_extract(m.token_usage, '$.input_tokens'), 0),
			COALESCE(json_extract(m.token_usage, '$.output_tokens'), 0),
			COALESCE(json_extract(m.token_usage, '$.cache_creation_input_tokens'), 0),
			COALESCE(json_extract(m.token_usage, '$.cache_read_input_tokens'), 0),
			COALESCE(p.input_per_mtok, 0),
			COALESCE(p.output_per_mtok, 0),
			COALESCE(p.cache_creation_per_mtok, 0),
			COALESCE(p.cache_read_per_mtok, 0)
		FROM messages m
		JOIN sessions s ON m.session_id = s.id
		LEFT JOIN model_pricing p ON m.model = p.model_pattern
		WHERE ` + strings.Join(preds, " AND ")

	rows, err := db.getReader().QueryContext(ctx, query, args...)
	if err != nil {
		return DailyUsageResult{},
			fmt.Errorf("querying daily usage: %w", err)
	}
	defer rows.Close()

	type dayModelKey struct {
		date  string
		model string
	}
	breakdowns := make(map[dayModelKey]*ModelBreakdown)
	dayOrder := make(map[string]int)
	var orderedDates []string

	for rows.Next() {
		var ts, model string
		var inputTok, outputTok, cacheCrTok, cacheRdTok int
		var inputRate, outputRate, cacheCrRate, cacheRdRate float64

		if err := rows.Scan(
			&ts, &model,
			&inputTok, &outputTok, &cacheCrTok, &cacheRdTok,
			&inputRate, &outputRate, &cacheCrRate, &cacheRdRate,
		); err != nil {
			return DailyUsageResult{},
				fmt.Errorf("scanning usage row: %w", err)
		}

		date := localDate(ts, loc)
		if date == "" || date < f.From || date > f.To {
			continue
		}

		key := dayModelKey{date: date, model: model}
		mb, ok := breakdowns[key]
		if !ok {
			mb = &ModelBreakdown{ModelName: model}
			breakdowns[key] = mb
		}
		mb.InputTokens += inputTok
		mb.OutputTokens += outputTok
		mb.CacheCreationTokens += cacheCrTok
		mb.CacheReadTokens += cacheRdTok

		cost := (float64(inputTok)*inputRate +
			float64(outputTok)*outputRate +
			float64(cacheCrTok)*cacheCrRate +
			float64(cacheRdTok)*cacheRdRate) / 1_000_000
		mb.Cost += cost

		if _, seen := dayOrder[date]; !seen {
			dayOrder[date] = len(orderedDates)
			orderedDates = append(orderedDates, date)
		}
	}
	if err := rows.Err(); err != nil {
		return DailyUsageResult{},
			fmt.Errorf("iterating usage rows: %w", err)
	}

	sort.Strings(orderedDates)

	var result DailyUsageResult
	for _, date := range orderedDates {
		entry := DailyUsageEntry{Date: date}
		modelSet := make(map[string]bool)

		for key, mb := range breakdowns {
			if key.date != date {
				continue
			}
			entry.InputTokens += mb.InputTokens
			entry.OutputTokens += mb.OutputTokens
			entry.CacheCreationTokens += mb.CacheCreationTokens
			entry.CacheReadTokens += mb.CacheReadTokens
			entry.TotalCost += mb.Cost
			entry.ModelBreakdowns = append(
				entry.ModelBreakdowns, *mb)
			if mb.ModelName != "" {
				modelSet[mb.ModelName] = true
			}
		}

		for m := range modelSet {
			entry.ModelsUsed = append(entry.ModelsUsed, m)
		}
		sort.Strings(entry.ModelsUsed)
		sort.Slice(entry.ModelBreakdowns, func(i, j int) bool {
			return entry.ModelBreakdowns[i].Cost >
				entry.ModelBreakdowns[j].Cost
		})

		result.Totals.InputTokens += entry.InputTokens
		result.Totals.OutputTokens += entry.OutputTokens
		result.Totals.CacheCreationTokens += entry.CacheCreationTokens
		result.Totals.CacheReadTokens += entry.CacheReadTokens
		result.Totals.TotalCost += entry.TotalCost

		result.Daily = append(result.Daily, entry)
	}

	if result.Daily == nil {
		result.Daily = []DailyUsageEntry{}
	}
	return result, nil
}

// location returns the timezone for date bucketing.
func (f UsageFilter) location() *time.Location {
	if f.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(f.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}
```

Note: Add `"time"` to the imports in usage.go.

- [ ] **Step 4: Run tests to verify they pass**

Run:
`cd /Users/wesm/code/agentsview && CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestGetDailyUsage -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/wesm/code/agentsview
git add internal/db/usage.go internal/db/usage_test.go
git commit -m "feat: add daily usage cost aggregation query"
```

______________________________________________________________________

### Task 5: CLI Subcommand — `agentsview usage`

**Files:**

- Create: `cmd/agentsview/usage.go`

- Modify: `cmd/agentsview/main.go:37-71` (add dispatch)

- Modify: `cmd/agentsview/main.go:77-170` (add help text)

- [ ] **Step 1: Write failing test for usage JSON output formatting**

```go
// cmd/agentsview/usage_test.go
package main

import (
	"encoding/json"
	"testing"

	"github.com/wesm/agentsview/internal/db"
)

func TestFormatDailyUsageJSON(t *testing.T) {
	result := db.DailyUsageResult{
		Daily: []db.DailyUsageEntry{{
			Date:         "2024-06-15",
			InputTokens:  1000,
			OutputTokens: 500,
			TotalCost:    0.01134,
			ModelsUsed:   []string{"claude-sonnet-4-20250514"},
		}},
		Totals: db.UsageTotals{
			InputTokens:  1000,
			OutputTokens: 500,
			TotalCost:    0.01134,
		},
	}

	out, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify VibePulse-compatible structure
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := parsed["daily"]; !ok {
		t.Error("missing 'daily' key in output")
	}
	if _, ok := parsed["totals"]; !ok {
		t.Error("missing 'totals' key in output")
	}
}
```

- [ ] **Step 2: Run test to verify it passes** (this tests serialization of
  existing types)

Run:
`cd /Users/wesm/code/agentsview && CGO_ENABLED=1 go test -tags fts5 ./cmd/agentsview/ -run TestFormatDailyUsageJSON -v`

- [ ] **Step 3: Implement runUsage command**

```go
// cmd/agentsview/usage.go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/wesm/agentsview/internal/config"
	"github.com/wesm/agentsview/internal/db"
	"github.com/wesm/agentsview/internal/pricing"
)

func runUsage(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr,
			"usage: agentsview usage <daily|statusline> [flags]")
		os.Exit(1)
	}

	switch args[0] {
	case "daily":
		runUsageDaily(args[1:])
	case "statusline":
		runUsageStatusline(args[1:])
	case "help", "--help", "-h":
		printUsageHelp()
	default:
		fmt.Fprintf(os.Stderr,
			"unknown usage subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func runUsageDaily(args []string) {
	fs := flag.NewFlagSet("usage daily", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "Output as JSON")
	since := fs.String("since", "", "Start date (YYYY-MM-DD)")
	until := fs.String("until", "", "End date (YYYY-MM-DD)")
	agent := fs.String("agent", "",
		"Filter by agent (claude, codex)")
	breakdown := fs.Bool("breakdown", false,
		"Show per-model breakdown")
	offline := fs.Bool("offline", false,
		"Use fallback pricing (no network)")
	timezone := fs.String("timezone", "",
		"IANA timezone for date bucketing")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "parsing flags: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.LoadMinimal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading config: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	ensurePricing(database, *offline)

	filter := db.UsageFilter{
		From:     *since,
		To:       *until,
		Agent:    *agent,
		Timezone: *timezone,
	}

	ctx := context.Background()
	result, err := database.GetDailyUsage(ctx, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "querying usage: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "encoding JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	printDailyTable(result, *breakdown)
}

func printDailyTable(result db.DailyUsageResult, breakdown bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DATE\tINPUT\tOUTPUT\tCACHE_CR\tCACHE_RD\tCOST\tMODELS")
	fmt.Fprintln(w, "----\t-----\t------\t--------\t--------\t----\t------")

	for _, day := range result.Daily {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t$%.4f\t%s\n",
			day.Date,
			day.InputTokens,
			day.OutputTokens,
			day.CacheCreationTokens,
			day.CacheReadTokens,
			day.TotalCost,
			strings.Join(day.ModelsUsed, ", "),
		)
		if breakdown {
			for _, mb := range day.ModelBreakdowns {
				fmt.Fprintf(w, "  %s\t%d\t%d\t%d\t%d\t$%.4f\t\n",
					mb.ModelName,
					mb.InputTokens,
					mb.OutputTokens,
					mb.CacheCreationTokens,
					mb.CacheReadTokens,
					mb.Cost,
				)
			}
		}
	}

	fmt.Fprintln(w, "----\t-----\t------\t--------\t--------\t----\t------")
	fmt.Fprintf(w, "TOTAL\t%d\t%d\t%d\t%d\t$%.4f\t\n",
		result.Totals.InputTokens,
		result.Totals.OutputTokens,
		result.Totals.CacheCreationTokens,
		result.Totals.CacheReadTokens,
		result.Totals.TotalCost,
	)
	w.Flush()
}

// ensurePricing loads pricing into the database, fetching from
// LiteLLM or falling back to embedded defaults.
func ensurePricing(database *db.DB, offline bool) {
	var prices []pricing.ModelPricing
	if !offline {
		var err error
		prices, err = pricing.FetchLiteLLMPricing()
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"warning: fetch pricing failed (%v), "+
					"using fallback\n", err)
			prices = pricing.FallbackPricing()
		}
	} else {
		prices = pricing.FallbackPricing()
	}

	dbPrices := make([]db.ModelPricing, len(prices))
	for i, p := range prices {
		dbPrices[i] = db.ModelPricing{
			ModelPattern:         p.ModelPattern,
			InputPerMTok:         p.InputPerMTok,
			OutputPerMTok:        p.OutputPerMTok,
			CacheCreationPerMTok: p.CacheCreationPerMTok,
			CacheReadPerMTok:     p.CacheReadPerMTok,
		}
	}
	if err := database.UpsertModelPricing(dbPrices); err != nil {
		fmt.Fprintf(os.Stderr,
			"warning: failed to store pricing: %v\n", err)
	}
}

func runUsageStatusline(args []string) {
	fs := flag.NewFlagSet("usage statusline", flag.ExitOnError)
	agent := fs.String("agent", "",
		"Filter by agent (claude, codex)")
	offline := fs.Bool("offline", false,
		"Use fallback pricing")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "parsing flags: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.LoadMinimal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading config: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	ensurePricing(database, *offline)

	today := todayDate()
	filter := db.UsageFilter{
		From:  today,
		To:    today,
		Agent: *agent,
	}

	ctx := context.Background()
	result, err := database.GetDailyUsage(ctx, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "querying usage: %v\n", err)
		os.Exit(1)
	}

	cost := result.Totals.TotalCost
	fmt.Printf("$%.2f today", cost)
	if *agent != "" {
		fmt.Printf(" (%s)", *agent)
	}
	fmt.Println()
}

func todayDate() string {
	return time.Now().Format("2006-01-02")
}

func printUsageHelp() {
	fmt.Print(`agentsview usage - token cost tracking

Commands:
  agentsview usage daily [flags]       Daily cost summary
  agentsview usage statusline [flags]  One-line cost summary

Daily flags:
  -json              Output as JSON
  -since YYYY-MM-DD  Start date (inclusive)
  -until YYYY-MM-DD  End date (inclusive)
  -agent string      Filter: claude, codex
  -breakdown         Show per-model cost breakdown
  -offline           Use fallback pricing (no network)
  -timezone string   IANA timezone for date bucketing

Statusline flags:
  -agent string      Filter: claude, codex
  -offline           Use fallback pricing (no network)
`)
}
```

Note: Add `"time"` to imports.

- [ ] **Step 4: Add dispatch to main.go**

Add this case to the switch in `main()` at `cmd/agentsview/main.go:38`:

```go
		case "usage":
			runUsage(os.Args[2:])
			return
```

Add this to `printUsage()` help text after the `token-use` line:

```
  agentsview usage <daily|statusline> [flags]
                          Token cost tracking and reporting
```

- [ ] **Step 5: Run go vet and test**

Run:
`cd /Users/wesm/code/agentsview && go vet ./... && CGO_ENABLED=1 go test -tags fts5 ./cmd/agentsview/ -run TestFormatDailyUsageJSON -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/wesm/code/agentsview
git add cmd/agentsview/usage.go cmd/agentsview/usage_test.go cmd/agentsview/main.go
git commit -m "feat: add 'agentsview usage' CLI subcommand"
```

______________________________________________________________________

### Task 6: Integration Test — End-to-End

**Files:**

- Create: `internal/db/usage_integration_test.go`

- [ ] **Step 1: Write integration test with multiple days, models, agents**

```go
// internal/db/usage_integration_test.go
package db

import (
	"context"
	"encoding/json"
	"math"
	"testing"
)

func TestGetDailyUsageMultipleDaysAndModels(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	// Pricing for two models
	requireNoError(t, d.UpsertModelPricing([]ModelPricing{
		{
			ModelPattern:         "claude-sonnet-4-20250514",
			InputPerMTok:         3.0,
			OutputPerMTok:        15.0,
			CacheCreationPerMTok: 3.75,
			CacheReadPerMTok:     0.30,
		},
		{
			ModelPattern:  "claude-opus-4-20250514",
			InputPerMTok:  15.0,
			OutputPerMTok: 75.0,
		},
	}), "upsert pricing")

	// Day 1: two messages, two models
	requireNoError(t, d.UpsertSession(Session{
		ID: "s1", Project: "proj", Machine: "local",
		Agent: "claude", StartedAt: Ptr("2024-06-15T10:00:00Z"),
	}), "session s1")
	insertMessages(t, d,
		Message{
			SessionID: "s1", Ordinal: 0, Role: "assistant",
			Content: "a", ContentLength: 1,
			Timestamp: "2024-06-15T10:00:00Z",
			Model:     "claude-sonnet-4-20250514",
			TokenUsage: json.RawMessage(
				`{"input_tokens":1000,"output_tokens":500}`),
			ContextTokens: 1000, OutputTokens: 500,
			HasContextTokens: true, HasOutputTokens: true,
		},
		Message{
			SessionID: "s1", Ordinal: 1, Role: "assistant",
			Content: "b", ContentLength: 1,
			Timestamp: "2024-06-15T11:00:00Z",
			Model:     "claude-opus-4-20250514",
			TokenUsage: json.RawMessage(
				`{"input_tokens":500,"output_tokens":200}`),
			ContextTokens: 500, OutputTokens: 200,
			HasContextTokens: true, HasOutputTokens: true,
		},
	)

	// Day 2: codex session
	requireNoError(t, d.UpsertSession(Session{
		ID: "s2", Project: "proj2", Machine: "local",
		Agent: "codex", StartedAt: Ptr("2024-06-16T09:00:00Z"),
	}), "session s2")
	insertMessages(t, d, Message{
		SessionID: "s2", Ordinal: 0, Role: "assistant",
		Content: "c", ContentLength: 1,
		Timestamp: "2024-06-16T09:00:00Z",
		Model:     "claude-sonnet-4-20250514",
		TokenUsage: json.RawMessage(
			`{"input_tokens":3000,"output_tokens":1000}`),
		ContextTokens: 3000, OutputTokens: 1000,
		HasContextTokens: true, HasOutputTokens: true,
	})

	// Query all
	result, err := d.GetDailyUsage(ctx, UsageFilter{
		From: "2024-06-01", To: "2024-06-30",
	})
	requireNoError(t, err, "GetDailyUsage all")

	if len(result.Daily) != 2 {
		t.Fatalf("expected 2 daily entries, got %d",
			len(result.Daily))
	}

	// Day 1 should have 2 models
	day1 := result.Daily[0]
	if day1.Date != "2024-06-15" {
		t.Errorf("day1.Date = %q, want 2024-06-15", day1.Date)
	}
	if len(day1.ModelsUsed) != 2 {
		t.Errorf("day1 models = %v, want 2 models",
			day1.ModelsUsed)
	}

	// Day 2
	day2 := result.Daily[1]
	if day2.Date != "2024-06-16" {
		t.Errorf("day2.Date = %q, want 2024-06-16", day2.Date)
	}

	// Totals should sum both days
	if result.Totals.InputTokens != 4500 {
		t.Errorf("total InputTokens = %d, want 4500",
			result.Totals.InputTokens)
	}
	if result.Totals.TotalCost <= 0 {
		t.Error("total cost should be > 0")
	}
}

func TestGetDailyUsageNoPricingStillWorks(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	// No pricing inserted — cost should be 0 but tokens counted
	requireNoError(t, d.UpsertSession(Session{
		ID: "s1", Project: "proj", Machine: "local",
		Agent: "claude", StartedAt: Ptr("2024-06-15T10:00:00Z"),
	}), "session")
	insertMessages(t, d, Message{
		SessionID: "s1", Ordinal: 0, Role: "assistant",
		Content: "a", ContentLength: 1,
		Timestamp: "2024-06-15T10:00:00Z",
		Model:     "unknown-model",
		TokenUsage: json.RawMessage(
			`{"input_tokens":1000,"output_tokens":500}`),
		ContextTokens: 1000, OutputTokens: 500,
		HasContextTokens: true, HasOutputTokens: true,
	})

	result, err := d.GetDailyUsage(ctx, UsageFilter{
		From: "2024-06-01", To: "2024-06-30",
	})
	requireNoError(t, err, "GetDailyUsage no pricing")

	if len(result.Daily) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result.Daily))
	}
	if result.Daily[0].InputTokens != 1000 {
		t.Errorf("InputTokens = %d, want 1000",
			result.Daily[0].InputTokens)
	}
	if math.Abs(result.Daily[0].TotalCost) > 0.0001 {
		t.Errorf("TotalCost = %f, want ~0 (no pricing)",
			result.Daily[0].TotalCost)
	}
}
```

- [ ] **Step 2: Run tests**

Run:
`cd /Users/wesm/code/agentsview && CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestGetDailyUsageMultiple -v && CGO_ENABLED=1 go test -tags fts5 ./internal/db/ -run TestGetDailyUsageNoPricing -v`
Expected: PASS

- [ ] **Step 3: Run full test suite to check for regressions**

Run: `cd /Users/wesm/code/agentsview && CGO_ENABLED=1 go test -tags fts5 ./...`
Expected: All pass

- [ ] **Step 4: Commit**

```bash
cd /Users/wesm/code/agentsview
git add internal/db/usage_integration_test.go
git commit -m "test: add integration tests for daily usage cost queries"
```

______________________________________________________________________

### Task 7: Manual Smoke Test

**Files:** None (verification only)

- [ ] **Step 1: Build and run with real data**

```bash
cd /Users/wesm/code/agentsview
go build -tags fts5 -o /tmp/agentsview-usage ./cmd/agentsview/
```

- [ ] **Step 2: Test daily JSON output**

```bash
/tmp/agentsview-usage usage daily --json --since 2026-04-01 --until 2026-04-11
```

Verify: JSON output with `daily` array and `totals` object. Dates present, costs
\> 0.

- [ ] **Step 3: Test daily table output**

```bash
/tmp/agentsview-usage usage daily --since 2026-04-01 --until 2026-04-11 --breakdown
```

Verify: Formatted table with columns, per-model breakdown rows.

- [ ] **Step 4: Test statusline output**

```bash
/tmp/agentsview-usage usage statusline
```

Verify: Single line like `$12.34 today`.

- [ ] **Step 5: Test agent filter**

```bash
/tmp/agentsview-usage usage daily --json --agent claude --since 2026-04-01
```

Verify: Only Claude sessions in output.

- [ ] **Step 6: Test offline mode**

```bash
/tmp/agentsview-usage usage daily --json --offline --since 2026-04-01 --until 2026-04-11
```

Verify: Works without network, uses fallback pricing.

- [ ] **Step 7: Run linter**

```bash
cd /Users/wesm/code/agentsview && golangci-lint run ./...
```

Fix any issues found.

______________________________________________________________________

### Task 8: Cross-Validation Against ccusage

**Files:** None (verification only)

This task compares agentsview output against ccusage to ensure cost/token
numbers match for both Claude Code and Codex.

- [ ] **Step 1: Capture ccusage Claude Code baseline**

```bash
npx --yes ccusage@latest daily --json > /tmp/ccusage-claude.json
```

- [ ] **Step 2: Capture ccusage Codex baseline**

```bash
npx --yes @ccusage/codex@latest daily --json --locale en-CA > /tmp/ccusage-codex.json
```

- [ ] **Step 3: Capture agentsview Claude output**

```bash
/tmp/agentsview-usage usage daily --json --agent claude > /tmp/av-claude.json
```

- [ ] **Step 4: Capture agentsview Codex output**

```bash
/tmp/agentsview-usage usage daily --json --agent codex > /tmp/av-codex.json
```

- [ ] **Step 5: Compare Claude daily totals**

Write a comparison script or manually compare key fields:

```bash
# Compare per-day totalCost values
jq -r '.daily[] | "\(.date) \(.totalCost)"' /tmp/ccusage-claude.json > /tmp/cc-claude-costs.txt
jq -r '.daily[] | "\(.date) \(.totalCost)"' /tmp/av-claude.json > /tmp/av-claude-costs.txt
diff /tmp/cc-claude-costs.txt /tmp/av-claude-costs.txt
```

Compare:

- Per-day `inputTokens`, `outputTokens`, `cacheCreationTokens`,
  `cacheReadTokens`
- Per-day `totalCost` (expect match within 1% tolerance due to floating point
  and pricing source timing)
- `totals.totalCost` aggregate
- `modelsUsed` arrays

Document any discrepancies. Likely sources of difference:

- **Date bucketing**: ccusage uses message timestamp, agentsview uses message
  timestamp with session fallback — should match for messages that have
  timestamps

- **Deduplication**: ccusage deduplicates by messageId:requestId, agentsview
  deduplicates by session file parsing — could cause minor count differences

- **Pricing source timing**: if LiteLLM updated between runs

- **Session filtering**: ccusage reads raw JSONL, agentsview filters out
  deleted/subagent sessions

- [ ] **Step 6: Compare Codex daily totals**

```bash
# ccusage/codex uses "costUSD" not "totalCost"
jq -r '.daily[] | "\(.date) \(.costUSD // .totalCost)"' /tmp/ccusage-codex.json > /tmp/cc-codex-costs.txt
jq -r '.daily[] | "\(.date) \(.totalCost)"' /tmp/av-codex.json > /tmp/av-codex-costs.txt
diff /tmp/cc-codex-costs.txt /tmp/av-codex-costs.txt
```

Same comparison as Step 5 but for Codex sessions.

- [ ] **Step 7: Investigate and fix discrepancies**

For each discrepancy found:

1. Identify root cause (date bucketing, deduplication, pricing, session
   filtering)
1. Decide if agentsview behavior is correct or needs fixing
1. Fix if needed, add test covering the case
1. Re-run comparison to verify

- [ ] **Step 8: Document known differences**

If intentional differences exist (e.g., agentsview excludes subagent sessions,
ccusage doesn't), document them in the usage help text or a comment in usage.go
so future users understand why numbers may differ slightly.
