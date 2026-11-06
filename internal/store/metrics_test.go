package store

import "testing"

func TestMetricsQueries(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.DB.Exec(`INSERT INTO users(id,username,password,role,created_at) VALUES(1,'root','x','superuser',1),(2,'ann','x','user',1),(3,'bob','x','user',1)`)
	st.DB.Exec(`INSERT INTO profiles(id,owner_id,name,token,created_at) VALUES(1,2,'A','t1',1),(2,3,'B','t2',1)`)
	for _, ck := range []string{"ok", "partly", "down", "new", "nostreams"} {
		st.DB.Exec(`INSERT INTO entries(ck,name,nsfw,closed,src) VALUES(?,?,0,0,0)`, ck, ck)
	}
	st.DB.Exec(`INSERT INTO streams(ck,url,status) VALUES('ok','http://a/1',1),('partly','http://b/1',1),('partly','http://b/2',2),('down','http://c/1',2),('new','http://d/1',0)`)
	st.DB.Exec(`INSERT INTO profile_channels(profile_id,ck,position) VALUES(1,'ok',0),(1,'partly',1),(1,'down',2),(1,'new',3),(1,'nostreams',4),(2,'ok',0)`)
	st.DB.Exec(`INSERT INTO custom_sources(id,name,url,created_at,last_ok,last_error,last_try) VALUES(5,'Mine','http://x',1,100,'',200),(6,'Broken','http://y',1,0,'HTTP 500',300)`)
	st.DB.Exec(`INSERT INTO skipped(source,ref,reason) VALUES('s','r','bad')`)

	c := st.CatalogueCounts()
	if c.Channels != 5 || c.Streams != 5 || c.Skipped != 1 || c.CustomSources != 2 || c.Profiles != 2 {
		t.Fatalf("counts: %+v", c)
	}
	if u := st.UsersByRole(); u["superuser"] != 1 || u["user"] != 2 || u["admin"] != 0 {
		t.Fatalf("users: %+v", u)
	}
	rows := st.CustomSourceRows()
	if len(rows) != 2 || rows[0].Name != "Mine" || rows[0].Failed || rows[0].LastOK != 100 || !rows[1].Failed {
		t.Fatalf("sources: %+v", rows)
	}
	s := st.ProfileChannelStates()
	if got := s[1]; got != (ProfileStates{Working: 1, Partly: 1, NotWorking: 1, Pending: 2}) {
		t.Fatalf("profile 1: %+v", got)
	}
	if got := s[2]; got != (ProfileStates{Working: 1}) {
		t.Fatalf("profile 2: %+v", got)
	}
}
