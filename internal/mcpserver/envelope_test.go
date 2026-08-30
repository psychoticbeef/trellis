package mcpserver

import (
	"encoding/json"
	"testing"

	"trellis/internal/core"
)

// TestEnvelopeSerialization_UT_31 proves UT-31 (DD-33 "Envelope structs"):
// empty results serialize as empty arrays inside the envelope, never null.
func TestEnvelopeSerialization_UT_31(t *testing.T) {
	b, err := json.Marshal(searchOut{Hits: []core.SearchHit{}})
	if err != nil || string(b) != `{"hits":[]}` {
		t.Fatalf("searchOut: %s %v", b, err)
	}
	b, err = json.Marshal(storiesOut{Stories: []core.StorySummary{}})
	if err != nil || string(b) != `{"stories":[]}` {
		t.Fatalf("storiesOut: %s %v", b, err)
	}
	// nil slices would serialize as null — the handlers normalize first.
	if b, _ := json.Marshal(searchOut{}); string(b) == `{"hits":[]}` {
		t.Fatal("nil hits must NOT equal empty envelope without normalization — this guard documents why handlers normalize")
	}
}
