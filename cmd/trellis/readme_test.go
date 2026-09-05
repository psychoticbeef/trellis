package main

import (
	"os"
	"strings"
	"testing"
)

// TestHumanMaintainedReadme_AT_68_IT_58_UT_68 proves AT-68, IT-58 and UT-68.
func TestHumanMaintainedReadme_AT_68_IT_58_UT_68(t *testing.T) {
	content, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}

	maintained := strings.Join([]string{
		"```<human-maintained>```",
		"",
		"## ai;dr",
		"",
		"Mit diesem Projekt verfolge ich folgende Ziele:",
		"",
		"- Mehr Zeit ins Nachdenken investieren, was genau das Projekt leisten soll.",
		"  - Das LLM dient als Requirements Engineer.",
		"- Spezifikation tracken.",
		"  - Die Spezifikation ist als Kanban-Board, User Story Mapping vorhanden. Keine Umsetzung ohne Spezifikation.",
		"- Gezieltere Modifikationen am Projekt.",
		"  - Specs nachträglich modifizieren.",
		"- Relevanten Kontext knapp und präsent halten.",
		"  - Projektfeatures und andere Details werden, teils auch während des Laufs, in den Kontext injiziert.",
		"- Code und Spec in Sync halten.",
		"  - Reviewer versuchen den typischen LLM-Rot einzudämmen.",
		"- Testing, Linting vor Feature-Merge, immer.",
		"  - Determinismus durch State Machine, Konfiguration von Linting / Testing.",
		"- Typische Begriffsdrifts minimieren.",
		"  - Glossar",
		"- Klare Feature-Commits, einheitlich.",
		"  - Commits übernimmt trellis.",
		"- Minimale manuelle Arbeit nach der Feature-Erstellung.",
		"  - /trellis:auto on setzt alle refined Features um. Derzeit mit /compact bei 70% Kontextverbrauch nach Featureabschluss.",
		"  - Features werden automatisch auf develop gemerged.",
		"  - Release-Merge mit ```trellis release```.",
		"- Anreiz, sich eher an nicht / nur sehr schwierig testbare Vorgaben zu halten.",
		"  - Cross-Cutting Concerns, die dem Kontext bereitgestellt werden.",
		"",
		"Natürlich kann nichts hiervon garantieren, dass das LLM keinen Schwachsinn treibt. Am Ende des Tages hat man zumindest eine Spezifikation, die theoretisch genutzt werden kann, um a) das Projekt nochmal ohne Slop umzusetzen oder b) es nochmal von einem mächtigeren Modell umsetzen zu lassen.",
		"",
		"Das Projekt verbrennt durch die Reviews Tokens ohne Ende.",
		"",
		"Pi Coding Agent Integration hier: [pi-trellis](https://github.com/psychoticbeef/pi-trellis)",
		"",
		"```</human-maintained>```",
	}, "\n")

	wantPrefix := "# trellis\n\n" + maintained + "\n\n--\n\nDeterministic spec tracking and story gating for LLM-driven development."
	if !strings.HasPrefix(string(content), wantPrefix) {
		t.Fatal("README.md maintained introduction or technical-description boundary changed")
	}
}
