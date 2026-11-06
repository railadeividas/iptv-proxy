package store

import (
	"fmt"
	"testing"
)

func addEntry(t *testing.T, st *Store, ck string, statuses ...int) {
	t.Helper()
	st.DB.Exec(`INSERT INTO entries(ck,name,nsfw,closed,src) VALUES(?,?,0,0,0)`, ck, ck)
	for i, s := range statuses {
		if _, err := st.DB.Exec(`INSERT INTO streams(ck,url,status) VALUES(?,?,?)`, ck, fmt.Sprintf("http://%s/%d", ck, i), s); err != nil {
			t.Fatal(err)
		}
	}
}

// The Channels list sorts working, partly working, no verdict, not working; "Working only" keeps channels where
// something plays; excluded streams are counted apart and do not make a channel partly working.
func TestEntriesPartlyWorkingAndExcluded(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	addEntry(t, st, "A-down", StatusDead, StatusDead)
	addEntry(t, st, "B-partly", StatusOK, StatusDead)
	addEntry(t, st, "C-new", StatusUnknown)
	addEntry(t, st, "D-working", StatusOK, StatusUnknown)

	order := func(q EntryQuery) string {
		t.Helper()
		q.Sort = "status"
		es, _, err := st.QueryEntries(q)
		if err != nil {
			t.Fatal(err)
		}
		out := ""
		for _, e := range es {
			out += fmt.Sprintf("%s(%d/%d/%d/%d) ", e.CK, e.Streams, e.Working, e.Dead, e.Excluded)
		}
		return out
	}
	if got := order(EntryQuery{}); got != "D-working(2/1/0/0) B-partly(2/1/1/0) C-new(1/0/0/0) A-down(2/0/2/0) " {
		t.Fatalf("status order: %s", got)
	}
	if got := order(EntryQuery{WorkingOnly: true}); got != "D-working(2/1/0/0) B-partly(2/1/1/0) " {
		t.Fatalf("working only: %s", got)
	}
	// excluding the failing stream: B is working; its excluded stream is counted apart
	st.SetExcluded("http://B-partly/1", true)
	// excluding every stream of A: its last results still count
	st.SetExcluded("http://A-down/0", true)
	st.SetExcluded("http://A-down/1", true)
	if got := order(EntryQuery{}); got != "B-partly(1/1/0/1) D-working(2/1/0/0) C-new(1/0/0/0) A-down(2/0/2/2) " {
		t.Fatalf("with excluded streams: %s", got)
	}
}

// A finished round stores the stream totals with excluded streams apart from their last result.
func TestRoundTotalsCountExcludedApart(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	addEntry(t, st, "A", StatusOK, StatusDead, StatusDead, StatusUnknown)
	st.SetExcluded("http://A/2", true)
	id := st.StartRound("manual", "x")
	st.FinishRound(id, 4, 1, 2, false)
	r, ok := st.GetRound(id)
	if !ok || r.TotalOK != 1 || r.TotalBad != 1 || r.TotalNew != 1 || r.TotalExcluded != 1 {
		t.Fatalf("round totals: %+v", r)
	}
}
