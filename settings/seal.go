package settings

import (
	"context"
	"log"
	"sync/atomic"

	"goeat/cryptbox"
	"goeat/db"
)

// Secret settings (KindSecret) in the plaintext `settings` table - the AI API
// keys, the Kroger client secret, the render token - are stored sealed with
// cryptbox, so a copy of the database file alone doesn't hand out working
// credentials (QSS security design §5.3). HA_TOKEN predates this and keeps
// its own row in the `secrets` table (Definition.Encrypted).
//
// The box is process-wide and set once at startup by UseBox. With no box (a
// SESSION_SECRET that is regenerated every boot, or tests) values are stored
// and read as plaintext: sealing under a key that dies with the process
// would lose every saved key at the next restart.
var box atomic.Pointer[cryptbox.Box]

// UseBox sets the box secret settings are sealed with. nil disables sealing.
func UseBox(b *cryptbox.Box) { box.Store(b) }

// SealValue returns the value to store for d: sealed for a secret setting
// when a box is set, otherwise v unchanged.
func SealValue(d Definition, v string) (string, error) {
	b := box.Load()
	if d.Kind != KindSecret || d.Encrypted || b == nil || v == "" || cryptbox.IsSealed(v) {
		return v, nil
	}
	return b.Seal(v)
}

// openValue reverses SealValue. Plaintext passes through unchanged; a sealed
// value that can't be opened (no box, or a rotated SESSION_SECRET) reports
// ok=false so the caller keeps its .env default.
func openValue(v string) (string, bool) {
	if !cryptbox.IsSealed(v) {
		return v, true
	}
	b := box.Load()
	if b == nil {
		return "", false
	}
	out, err := b.Open(v)
	if err != nil {
		return "", false
	}
	return out, true
}

// SealStoredSecrets seals any admin-edited secret setting still stored as
// plaintext (saved before sealing existed). env-sourced rows are rewritten
// by Seed on every boot, so they need nothing here. Call once at startup,
// after UseBox and before Apply.
func SealStoredSecrets(ctx context.Context, store db.Store) error {
	if box.Load() == nil {
		return nil
	}
	rows, err := store.ListSettings(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		d, ok := ByKey(r.Key)
		if !ok || r.Source != "admin" || r.Value == "" || cryptbox.IsSealed(r.Value) {
			continue
		}
		sealed, err := SealValue(d, r.Value)
		if err != nil || sealed == r.Value {
			continue
		}
		if err := store.SetSetting(ctx, r.Key, sealed); err != nil {
			return err
		}
		log.Printf("settings: sealed stored %s", r.Key)
	}
	return nil
}
