// Package model defines the node types, tree rules and content hashing
// for the trellis spec graph.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type Kind string

const (
	KindStory           Kind = "story"
	KindActivity        Kind = "activity"
	KindAcceptanceTest  Kind = "acceptance_test"
	KindArch            Kind = "arch"
	KindIntegrationTest Kind = "integration_test"
	KindDetailDesign    Kind = "detail_design"
	KindUnitTest        Kind = "unit_test"
	KindCrossCutting    Kind = "cross_cutting"
)

// Story lifecycle states.
const (
	StatusTodo       = "todo"
	StatusRefined    = "refined"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
)

var prefixes = map[Kind]string{
	KindStory:           "US",
	KindActivity:        "UA",
	KindAcceptanceTest:  "AT",
	KindArch:            "AS",
	KindIntegrationTest: "IT",
	KindDetailDesign:    "DD",
	KindUnitTest:        "UT",
	KindCrossCutting:    "CC",
}

// parentKind maps each kind to the kind its parent must have.
// Kinds absent from the map are roots (no parent allowed).
var parentKind = map[Kind]Kind{
	KindAcceptanceTest:  KindStory,
	KindArch:            KindStory,
	KindIntegrationTest: KindArch,
	KindDetailDesign:    KindArch,
	KindUnitTest:        KindDetailDesign,
}

// TestSpecKinds are the kinds that must be backed by real, passing tests.
var TestSpecKinds = map[Kind]bool{
	KindAcceptanceTest:  true,
	KindIntegrationTest: true,
	KindUnitTest:        true,
}

func Prefix(k Kind) string { return prefixes[k] }

func ValidKind(k Kind) bool { _, ok := prefixes[k]; return ok }

// ParentKind returns the required parent kind and whether the kind needs a parent.
func ParentKind(k Kind) (Kind, bool) {
	p, ok := parentKind[k]
	return p, ok
}

type Node struct {
	ID         string
	ProjectID  string
	Kind       Kind
	ParentID   string // empty for story, activity and cross_cutting
	Title      string
	Body       string
	Covers     []string // acceptance_test only: AC ids this test proves
	Paths      []string // story only: repo-relative files/folders realizing it; metadata, never hashed
	Status     string   // story only
	Position   int      // activity only: story map order; metadata, never hashed
	ActivityID string   // story only: activity in placement; metadata, never hashed
	// Approval bookkeeping. Empty = never approved.
	ApprovedContentHash string
	ApprovedParentHash  string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type AC struct {
	ID       string
	StoryID  string
	Given    string
	When     string
	Then     string
	Position int
}

type Dep struct {
	NodeID     string
	TargetID   string
	PinnedHash string
}

// hashDoc is the canonical serialization used for content hashing.
// Field order is fixed by struct declaration; encoding/json is deterministic
// for structs, so identical content always yields the identical hash.
type hashDoc struct {
	Kind   string   `json:"kind"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Covers []string `json:"covers,omitempty"`
	ACs    []acDoc  `json:"acs,omitempty"`
}

type acDoc struct {
	ID    string `json:"id"`
	Given string `json:"given"`
	When  string `json:"when"`
	Then  string `json:"then"`
}

// ContentHash computes the canonical hash of a node. For stories the
// acceptance criteria are part of the content: changing an AC changes the
// story hash and thereby invalidates every child approval.
func ContentHash(n *Node, acs []AC) string {
	doc := hashDoc{Kind: string(n.Kind), Title: n.Title, Body: n.Body}
	if len(n.Covers) > 0 {
		doc.Covers = append([]string(nil), n.Covers...)
		sort.Strings(doc.Covers)
	}
	if n.Kind == KindStory {
		sorted := append([]AC(nil), acs...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
		for _, ac := range sorted {
			doc.ACs = append(doc.ACs, acDoc{ID: ac.ID, Given: ac.Given, When: ac.When, Then: ac.Then})
		}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("hash serialization: %v", err)) // cannot happen for these types
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
