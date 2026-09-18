package items

import "testing"

func TestLooksLikeImage(t *testing.T) {
	jpegMagic := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0}
	htmlBody := []byte("<!DOCTYPE html><html><body>Just a moment...</body></html>")

	cases := []struct {
		name        string
		contentType string
		data        []byte
		want        bool
	}{
		{"declared image content-type", "image/jpeg", nil, true},
		{"declared image content-type with charset-like params", "image/jpeg; charset=binary", nil, true},
		{"no content-type, real jpeg bytes", "", jpegMagic, true},
		{"declared html - a challenge page substituted for the image", "text/html; charset=utf-8", htmlBody, false},
		{"no content-type, html bytes", "", htmlBody, false},
		{"no content-type, empty body", "", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeImage(c.contentType, c.data); got != c.want {
				t.Errorf("looksLikeImage(%q, %v) = %v, want %v", c.contentType, c.data, got, c.want)
			}
		})
	}
}

func TestLatin1BytesRoundTripsArbitraryBytes(t *testing.T) {
	// Every byte value 0-255 must survive the string->[]byte round trip
	// unchanged - this is what makes recovering FlareSolverr's decoded
	// binary response viable at all (see fetchImageViaFlareSolverr).
	orig := make([]byte, 256)
	for i := range orig {
		orig[i] = byte(i)
	}
	// Decode the way FlareSolverr's JSON response would have arrived: each
	// byte as its own Latin-1 rune.
	runes := make([]rune, len(orig))
	for i, b := range orig {
		runes[i] = rune(b)
	}
	s := string(runes)

	got := latin1Bytes(s)
	if len(got) != len(orig) {
		t.Fatalf("length = %d, want %d", len(got), len(orig))
	}
	for i := range orig {
		if got[i] != orig[i] {
			t.Fatalf("byte %d = %d, want %d", i, got[i], orig[i])
		}
	}
}
