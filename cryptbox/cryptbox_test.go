package cryptbox

import "testing"

func TestSealOpenRoundTrip(t *testing.T) {
	b := New("a-test-session-secret-at-least-32-chars-long")
	for _, pt := range []string{"", "x", "llat_abc123.def456", "unicode ✓ and spaces"} {
		sealed, err := b.Seal(pt)
		if err != nil {
			t.Fatalf("seal %q: %v", pt, err)
		}
		if pt != "" && !IsSealed(sealed) {
			t.Fatalf("sealed %q missing prefix: %q", pt, sealed)
		}
		got, err := b.Open(sealed)
		if err != nil {
			t.Fatalf("open %q: %v", pt, err)
		}
		if got != pt {
			t.Fatalf("round trip: got %q want %q", got, pt)
		}
	}
}

func TestOpenPlaintextPassthrough(t *testing.T) {
	b := New("secret")
	got, err := b.Open("not-encrypted-yet")
	if err != nil || got != "not-encrypted-yet" {
		t.Fatalf("plaintext passthrough failed: %q %v", got, err)
	}
}

func TestOpenWrongKey(t *testing.T) {
	sealed, _ := New("secret-one-goes-here-and-is-long-enough").Seal("hunter2")
	if _, err := New("a-totally-different-secret-value-here").Open(sealed); err == nil {
		t.Fatal("expected decryption failure with wrong key")
	}
}

func TestSealIsNondeterministic(t *testing.T) {
	b := New("secret")
	a, _ := b.Seal("same")
	c, _ := b.Seal("same")
	if a == c {
		t.Fatal("seal should use a fresh nonce each call")
	}
}
