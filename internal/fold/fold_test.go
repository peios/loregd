package fold

import "testing"

func TestASCII(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"hello", "hello"},
		{"HELLO", "hello"},
		{"Hello", "hello"},
		{"Machine", "machine"},
		{"SYSTEM", "system"},
		{"key_name", "key_name"},
		{"KEY_NAME", "key_name"},
		{"MixedCase123", "mixedcase123"},
	}
	for _, tt := range tests {
		got := String(tt.in)
		if got != tt.want {
			t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUnicode(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		{"greek uppercase", "ΩΜΕΓΑ", "ωμεγα"},
		{"cyrillic", "КИРИЛЛИЦА", "кириллица"},
		{"german sharp s unchanged", "straße", "straße"},
		{"capital sharp s folds", "STRAẞE", "straße"},
		{"kelvin sign", "\u212A", "k"},
		{"angstrom sign", "\u212B", "\u00e5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String(tt.in)
			if got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCaseFoldingDivergences(t *testing.T) {
	// These three codepoints diverge between unicode.ToLower and
	// CaseFolding.txt C+S entries. Our implementation must match
	// CaseFolding.txt exactly.
	tests := []struct {
		name     string
		in, want string
	}{
		{"micro sign folds to mu", "\u00B5", "\u03BC"},
		{"long s folds to s", "\u017F", "s"},
		{"I with dot above unchanged", "\u0130", "\u0130"},
		{"micro sign in context", "a\u00B5b", "a\u03BCb"},
		{"long s in context", "a\u017Fb", "asb"},
		{"I-dot in context", "A\u0130B", "a\u0130b"},
		{"micro and mu collide", "\u00B5key", "\u03BCkey"},
		{"mu stays mu", "\u03BCkey", "\u03BCkey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String(tt.in)
			if got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	// Verify collision: MICRO SIGN and GREEK SMALL MU must fold to same value.
	micro := String("\u00B5")
	mu := String("\u03BC")
	if micro != mu {
		t.Errorf("MICRO SIGN and MU should collide: %q vs %q", micro, mu)
	}

	// Verify collision: LONG S and LATIN SMALL S must fold to same value.
	longS := String("\u017F")
	latinS := String("s")
	if longS != latinS {
		t.Errorf("LONG S and s should collide: %q vs %q", longS, latinS)
	}

	// Verify non-collision: I-DOT must NOT collide with plain i.
	iDot := String("\u0130")
	plainI := String("i")
	if iDot == plainI {
		t.Errorf("I-DOT and i should NOT collide but both fold to %q", iDot)
	}
}

func TestAlreadyFolded(t *testing.T) {
	// Already folded strings should be returned without allocation.
	s := "already_folded_123"
	got := String(s)
	if got != s {
		t.Errorf("String(%q) = %q, want same string", s, got)
	}
}

func TestIdempotent(t *testing.T) {
	inputs := []string{
		"Hello World",
		"SYSTEM\\Registry\\Keys",
		"Ωmega",
		"straße",
		"İstanbul",
	}
	for _, in := range inputs {
		once := String(in)
		twice := String(once)
		if once != twice {
			t.Errorf("not idempotent: String(%q) = %q, String(%q) = %q",
				in, once, once, twice)
		}
	}
}

func TestSpecialCharactersPreserved(t *testing.T) {
	// Backslash, forward slash, spaces, etc. are preserved.
	tests := []struct {
		in, want string
	}{
		{"Machine\\System\\Registry", "machine\\system\\registry"},
		{"path/with/slashes", "path/with/slashes"},
		{"name with spaces", "name with spaces"},
		{"value.name", "value.name"},
		{"null\x00byte", "null\x00byte"},
	}
	for _, tt := range tests {
		got := String(tt.in)
		if got != tt.want {
			t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
