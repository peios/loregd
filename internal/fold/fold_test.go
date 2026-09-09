package fold

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

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
		{"kelvin sign", "K", "k"},
		{"angstrom sign", "Å", "å"},
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

// TestLowercasingIsNotFolding pins the codepoints where Go's
// unicode.ToLower and Simple Case Folding disagree. The old
// implementation was ToLower plus three fixes and diverged from the
// kernel's table on 222 codepoints; every class of that divergence has a
// representative here.
func TestLowercasingIsNotFolding(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		// The three the old implementation already corrected.
		{"micro sign folds to mu", "µ", "μ"},
		{"long s folds to s", "ſ", "s"},
		{"I with dot above unchanged", "İ", "İ"},
		// Cherokee folds towards uppercase: the 172-codepoint block
		// ToLower sent the wrong way.
		{"cherokee small folds to capital", "ꭰ", "Ꭰ"},
		{"cherokee capital unchanged", "Ꭰ", "Ꭰ"},
		{"cherokee small y folds up", "ᏸ", "Ᏸ"},
		// Greek: final sigma and the symbol variants fold to the base
		// letter; ToLower leaves them alone.
		{"final sigma folds to sigma", "ς", "σ"},
		{"ypogegrammeni folds to iota", "ͅ", "ι"},
		{"beta symbol folds to beta", "ϐ", "β"},
		{"theta symbol folds to theta", "ϑ", "θ"},
		{"prosgegrammeni folds to iota", "ι", "ι"},
		// Cyrillic Extended-C historic letters fold to the modern ones.
		{"cyrillic small rounded ve folds", "ᲀ", "в"},
		{"cyrillic small tall te folds", "ᲄ", "т"},
		{"cyrillic small tje (16.0) folds", "Ᲊ", "ᲊ"},
		// Garay, new in Unicode 16.0, absent from Go's 15.0 tables.
		{"garay capital folds to small", "\U00010D50", "\U00010D70"},
		// Latin Extended-D additions in 16.0.
		{"capital rams horn folds", "Ɤ", "ɤ"},
		{"capital lambda with stroke folds", "Ƛ", "ƛ"},
		// Ligature: st folds to the long-s variant's target.
		{"long s t ligature folds", "ﬅ", "ﬆ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String(tt.in)
			if got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	// The whole point: two spellings of one key reach one folded form.
	for _, pair := range [][2]string{
		{"µ", "μ"},
		{"ſ", "s"},
		{"Σ", "ς"},                   // Σ and final ς
		{"Ꭰ", "ꭰ"},                   // Cherokee capital and small
		{"В", "ᲀ"},                   // В and rounded ве
		{"\U00010D50", "\U00010D70"}, // Garay capital and small
	} {
		if String(pair[0]) != String(pair[1]) {
			t.Errorf("%q and %q should fold together: %q vs %q",
				pair[0], pair[1], String(pair[0]), String(pair[1]))
		}
	}
	if String("İ") == String("i") {
		t.Errorf("I with dot above must not collide with i")
	}
}

// TestTableIsComplete checks the generated table against the count the
// generator recorded from the source file, so a truncated or hand-edited
// table fails here rather than at a lookup.
func TestTableIsComplete(t *testing.T) {
	n := len(caseFoldPairs)
	for _, rg := range caseFoldRanges {
		n += int(rg.end-rg.start) + 1
	}
	if n != caseFoldMappings {
		t.Fatalf("table expands to %d mappings, header says %d", n, caseFoldMappings)
	}
	for i := 1; i < len(caseFoldRanges); i++ {
		if caseFoldRanges[i].start <= caseFoldRanges[i-1].end {
			t.Errorf("ranges not sorted/disjoint at %d", i)
		}
	}
	for i := 1; i < len(caseFoldPairs); i++ {
		if caseFoldPairs[i].source <= caseFoldPairs[i-1].source {
			t.Errorf("pairs not sorted at %d", i)
		}
	}
}

// TestFoldIsIdempotentForEveryScalar is the property Simple Case Folding
// guarantees: a folded codepoint folds to itself. Any table entry whose
// target is itself a source of a different mapping would break it.
func TestFoldIsIdempotentForEveryScalar(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		once := Rune(r)
		if twice := Rune(once); twice != once {
			t.Fatalf("Rune(%U) = %U, but Rune(%U) = %U", r, once, once, twice)
		}
	}
}

// TestMatchesCaseFoldingTxt checks the table against the Unicode source
// file itself, for every scalar. It runs only when CASEFOLDING_TXT names
// a CaseFolding.txt, since the file is not vendored:
//
//	CASEFOLDING_TXT=/path/to/CaseFolding.txt go test ./internal/fold
func TestMatchesCaseFoldingTxt(t *testing.T) {
	path := os.Getenv("CASEFOLDING_TXT")
	if path == "" {
		t.Skip("CASEFOLDING_TXT not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want := map[rune]rune{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		parts := strings.Split(line, ";")
		if len(parts) < 3 {
			continue
		}
		status := strings.TrimSpace(parts[1])
		if status != "C" && status != "S" {
			continue
		}
		src, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		dst, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		want[rune(src)] = rune(dst)
	}
	if len(want) != caseFoldMappings {
		t.Fatalf("%s has %d C+S rows, table was generated from %d", path, len(want), caseFoldMappings)
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		w, ok := want[r]
		if !ok {
			w = r
		}
		if got := Rune(r); got != w {
			t.Errorf("Rune(%U) = %U, CaseFolding.txt says %U", r, got, w)
		}
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
		"ΟΔΥΣΣΕΎΣ",
		"Ꭰꭰ",
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
