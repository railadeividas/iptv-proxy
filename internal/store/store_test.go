package store

import "testing"

func TestEntriesInProfileFilter(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.DB.Exec(`INSERT INTO entries(ck,name,nsfw,closed,src) VALUES('A','A',0,0,0),('B','B',0,0,0)`)
	st.DB.Exec(`INSERT INTO users(username,password,role,created_at) VALUES('u','x','user',0),('v','x','user',0)`)
	st.DB.Exec(`INSERT INTO profiles(owner_id,name,token) VALUES(1,'p','t')`)
	st.DB.Exec(`INSERT INTO profile_channels(profile_id,ck,position) VALUES(1,'B',0)`)
	items, total, err := st.QueryEntries(EntryQuery{})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("all: %d %v", total, err)
	}
	items, total, _ = st.QueryEntries(EntryQuery{InProfile: true})
	if total != 1 || items[0].CK != "B" {
		t.Fatalf("in a profile: %d %+v", total, items)
	}
	// only the owner's profiles count for the owner
	if _, total, _ = st.QueryEntries(EntryQuery{InProfile: true, Owner: 2}); total != 0 {
		t.Fatalf("another user's profile must not count: %d", total)
	}
	items, total, _ = st.QueryEntries(EntryQuery{InProfile: true, Owner: 1})
	if total != 1 || len(items[0].Profiles) != 1 {
		t.Fatalf("own profile: %d %+v", total, items)
	}
	items, _, _ = st.QueryEntries(EntryQuery{Owner: 2})
	for _, e := range items {
		if len(e.Profiles) != 0 {
			t.Fatalf("profile ids of other users leak: %+v", e)
		}
	}
}

func TestEntriesSortAndAll(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.DB.Exec(`INSERT INTO entries(ck,name,country,nsfw,closed,src) VALUES('A','Alpha','LT',0,0,0),('B','Bravo','AL',0,0,0),('C','Charlie','',0,0,0)`)
	st.DB.Exec(`INSERT INTO streams(ck,url,status) VALUES('A','u1',2),('B','u2',1),('B','u3',1),('C','u4',0)`)
	names := func(sort string) string {
		items, _, _ := st.QueryEntries(EntryQuery{Sort: sort})
		var out string
		for _, e := range items {
			out += e.CK
		}
		return out
	}
	for sort, want := range map[string]string{"": "ABC", "name": "ABC", "name_desc": "CBA", "country": "BAC", "streams": "BAC", "status": "BCA", "bogus": "ABC"} {
		if got := names(sort); got != want {
			t.Errorf("sort %q: %s, want %s", sort, got, want)
		}
	}
	items, total, _ := st.QueryEntries(EntryQuery{All: true, Limit: 1})
	if total != 3 || len(items) != 3 {
		t.Fatalf("All ignores paging: %d of %d", len(items), total)
	}
}
