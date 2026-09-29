package settings

import (
	"context"
	"strings"
	"testing"

	"goeat/config"
	"goeat/cryptbox"
	"goeat/db"
)

// TestSecretSettingsSealedAtRest: an API key saved from the Settings page, or
// seeded from .env, never sits in the settings table as plaintext, and Apply
// still hands the real value to the config.
func TestSecretSettingsSealedAtRest(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A plaintext admin row left over from before sealing existed.
	if err := store.SetSetting(ctx, "OPENAI_API_KEY", "sk-legacy"); err != nil {
		t.Fatal(err)
	}

	UseBox(cryptbox.New(strings.Repeat("k", 32)))
	t.Cleanup(func() { UseBox(nil) })

	if err := SealStoredSecrets(ctx, store); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AnthropicAPIKey: "sk-from-env"}
	if err := Seed(ctx, store, cfg); err != nil {
		t.Fatal(err)
	}

	d, _ := ByKey("LLM_API_KEY")
	sealed, err := SealValue(d, "sk-saved")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(ctx, "LLM_API_KEY", sealed); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "LLM_API_KEY"} {
		row, _ := store.GetSetting(ctx, key)
		if row == nil || !cryptbox.IsSealed(row.Value) {
			t.Errorf("%s stored as %v, want sealed", key, row)
		}
	}
	// Non-secret settings stay readable.
	if row, _ := store.GetSetting(ctx, "APP_NAME"); row != nil && cryptbox.IsSealed(row.Value) {
		t.Error("APP_NAME was sealed")
	}

	out := &config.Config{}
	if err := Apply(ctx, store, out, t.Logf); err != nil {
		t.Fatal(err)
	}
	if out.OpenAIAPIKey != "sk-legacy" || out.LLMAPIKey != "sk-saved" {
		t.Errorf("Apply opened OpenAI=%q LLM=%q", out.OpenAIAPIKey, out.LLMAPIKey)
	}

	// A rotated SESSION_SECRET can't open the old values: Apply keeps the
	// .env default instead of feeding ciphertext to the provider.
	UseBox(cryptbox.New(strings.Repeat("z", 32)))
	rotated := &config.Config{LLMAPIKey: "env-default"}
	if err := Apply(ctx, store, rotated, t.Logf); err != nil {
		t.Fatal(err)
	}
	if rotated.LLMAPIKey != "env-default" {
		t.Errorf("rotated secret: LLMAPIKey = %q, want the .env default", rotated.LLMAPIKey)
	}
}
