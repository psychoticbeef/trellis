package board

import (
	"strings"
	"testing"

	"trellis/internal/store"
)

// TestTermify_UT_22 proves UT-22 (DD-22 "Glossary storage and term marking"):
// whole-word case-insensitive matching, longest-first precedence, escaping
// of hostile terms and definitions.
func TestTermify_UT_22(t *testing.T) {
	tf := newTermifier([]store.TermDef{
		{Term: "gate", Definition: "guard that blocks a transition"},
		{Term: "merge gate", Definition: `finish requires base "in" branch <up-to-date>`},
		{Term: "a<b", Definition: "hostile term"},
	})

	// Whole word, case-insensitive.
	out := string(tf.markup("The GATE decides. Delegate nothing."))
	if !strings.Contains(out, `>GATE</a>`) {
		t.Fatalf("case-insensitive match missing: %s", out)
	}
	if strings.Contains(out, "Dele<a") || strings.Contains(out, `>gate</a> nothing`) {
		t.Fatalf("substring inside 'Delegate' must not match: %s", out)
	}

	// Longest term wins for overlaps.
	out = string(tf.markup("the merge gate stands"))
	if !strings.Contains(out, `#gloss-merge-gate"`) || strings.Count(out, "<a") != 1 {
		t.Fatalf("longest-first precedence failed: %s", out)
	}

	// Hostile definition is escaped inside the title attribute.
	if strings.Contains(out, `<up-to-date>`) {
		t.Fatalf("definition leaked unescaped markup: %s", out)
	}

	// Hostile term matches its escaped occurrence, everything stays escaped.
	out = string(tf.markup("compare a<b here"))
	if !strings.Contains(out, "a&lt;b</a>") || strings.Contains(out, "<b ") {
		t.Fatalf("hostile term handling: %s", out)
	}

	// No terms: plain escaping only.
	empty := newTermifier(nil)
	if got := string(empty.markup("<x> & y")); got != "&lt;x&gt; &amp; y" {
		t.Fatalf("plain escape: %s", got)
	}
}
