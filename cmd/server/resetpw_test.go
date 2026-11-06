package main

import (
	"path/filepath"
	"testing"

	"iptv-proxy/internal/auth"
	"iptv-proxy/internal/store"
)

func TestResetPassword(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := resetPassword(filepath.Join(dir, "nowhere"), ""); err == nil {
		t.Fatal("a wrong data directory must not create a database")
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resetPassword(dir, ""); err == nil {
		t.Fatal("no superuser yet")
	}
	root, _ := st.CreateUser("root", auth.Hash("old-password"), auth.Superuser)
	bob, _ := st.CreateUser("bob", auth.Hash("bobs-password"), auth.User)
	st.Close()

	u, pw, err := resetPassword(dir, "")
	if err != nil || u.ID != root.ID || len(pw) != 16 {
		t.Fatalf("reset the superuser: %v %v %q", u, err, pw)
	}
	st, _ = store.Open(dir)
	defer st.Close()
	after, _ := st.GetUser(root.ID)
	if auth.Verify(after.Password, "old-password") || !auth.Verify(after.Password, pw) {
		t.Fatal("the old password is gone, the printed one works")
	}
	if auth.Fingerprint(after.Password) == auth.Fingerprint(root.Password) {
		t.Fatal("existing sessions must stop working")
	}
	if b, _ := st.GetUser(bob.ID); !auth.Verify(b.Password, "bobs-password") {
		t.Fatal("other users are untouched")
	}
	st.Close()
	if u, _, err := resetPassword(dir, "bob"); err != nil || u.ID != bob.ID {
		t.Fatalf("any user can be reset by name: %v", err)
	}
	if _, _, err := resetPassword(dir, "nobody"); err == nil {
		t.Fatal("unknown user")
	}
}
