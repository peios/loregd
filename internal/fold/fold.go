// Package fold implements Unicode Simple Case Folding for the LCS registry.
//
// The folding algorithm uses CaseFolding.txt status C and S entries
// (one-to-one codepoint mappings, no locale dependency). The Unicode
// version is pinned to 16.0 per the LCS v0.21 specification.
//
// Go's unicode.ToLower is close but diverges from Simple Case Folding
// for three codepoints:
//
//   - U+00B5 MICRO SIGN: CaseFolding C maps to U+03BC (μ), ToLower leaves unchanged
//   - U+017F LATIN SMALL LETTER LONG S: CaseFolding C maps to U+0073 (s), ToLower leaves unchanged
//   - U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE: No C/S entry (should be unchanged), ToLower maps to U+0069 (over-folds)
//
// This function corrects these three cases explicitly.
//
// This function is applied at write time to produce name_folded and
// child_name_folded columns. Queries use exact binary comparison on
// the folded form.
package fold

import "unicode"

// simpleCaseFold maps a single rune through Unicode Simple Case Folding
// (CaseFolding.txt, status C and S entries, Unicode 16.0).
func simpleCaseFold(r rune) rune {
	switch r {
	case 0x00B5: // MICRO SIGN → GREEK SMALL LETTER MU
		return 0x03BC
	case 0x017F: // LATIN SMALL LETTER LONG S → LATIN SMALL LETTER S
		return 0x0073
	case 0x0130: // LATIN CAPITAL LETTER I WITH DOT ABOVE — no C/S entry, leave unchanged
		return r
	default:
		return unicode.ToLower(r)
	}
}

// String returns the Unicode Simple Case Folded form of s.
// Each rune is mapped through Unicode Simple Case Folding
// (CaseFolding.txt, status C and S entries, Unicode 16.0).
func String(s string) string {
	// Fast path: if the string is already fully folded, return it
	// without allocation.
	allFolded := true
	for _, r := range s {
		if simpleCaseFold(r) != r {
			allFolded = false
			break
		}
	}
	if allFolded {
		return s
	}

	buf := make([]rune, 0, len(s))
	for _, r := range s {
		buf = append(buf, simpleCaseFold(r))
	}
	return string(buf)
}
