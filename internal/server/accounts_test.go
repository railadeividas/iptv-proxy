package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestFirstStartSetup(t *testing.T) {
	h, _ := testServer(t)
	var s struct {
		SetupNeeded bool            `json:"setup_needed"`
		User        json.RawMessage `json:"user"`
	}
	json.Unmarshal(call(h, "GET", "/api/session", nil).Body.Bytes(), &s)
	if !s.SetupNeeded || string(s.User) != "null" {
		t.Fatalf("fresh start must ask for setup: %+v", s)
	}
	if call(h, "POST", "/api/login", map[string]string{"username": "root", "password": "rootpassword"}).Code != 401 {
		t.Fatal("no login before setup")
	}
	if code := call(h, "POST", "/api/setup", map[string]string{"username": "r", "password": "rootpassword"}).Code; code != 400 {
		t.Fatalf("a bad user name is refused: %d", code)
	}
	if code := call(h, "POST", "/api/setup", map[string]string{"username": "root", "password": "short"}).Code; code != 400 {
		t.Fatalf("a short password is refused: %d", code)
	}
	ck := signIn(t, h) // setup
	if code := call(h, "POST", "/api/setup", map[string]string{"username": "again", "password": "rootpassword"}).Code; code != 409 {
		t.Fatalf("setup works only once: %d", code)
	}
	json.Unmarshal(call(h, "GET", "/api/session", nil, ck).Body.Bytes(), &s)
	if s.SetupNeeded || string(s.User) == "null" {
		t.Fatalf("after setup: %+v", s)
	}
	if call(h, "POST", "/api/login", map[string]string{"username": "ROOT", "password": "rootpassword"}).Code != 200 {
		t.Fatal("user names are not case sensitive")
	}
}

type account struct{ cookie *http.Cookie }

func addUser(t *testing.T, h http.Handler, root *http.Cookie, name, role string) account {
	t.Helper()
	if code := call(h, "POST", "/api/users", map[string]string{"username": name, "password": "password-" + name, "role": role}, root).Code; code != 200 {
		t.Fatalf("create %s: %d", name, code)
	}
	return account{sessionOf(t, call(h, "POST", "/api/login", map[string]string{"username": name, "password": "password-" + name}))}
}

func TestRolesAndUsers(t *testing.T) {
	h, _ := testServer(t)
	root := signIn(t, h)
	admin := addUser(t, h, root, "ann", "admin").cookie
	user := addUser(t, h, root, "bob", "user").cookie

	status := func(c *http.Cookie, method, path string) int { return call(h, method, path, nil, c).Code }
	for _, c := range []struct {
		method, path       string
		root, admin, plain int
	}{
		{"GET", "/api/channels", 200, 200, 200},
		{"GET", "/api/profiles", 200, 200, 200},
		{"GET", "/api/health", 200, 200, 403},
		{"GET", "/api/custom", 200, 200, 403},
		{"GET", "/api/skipped", 200, 200, 403},
		{"GET", "/api/settings", 200, 403, 403},
		{"POST", "/api/sync", 202, 403, 403},
		{"GET", "/api/users", 200, 403, 403},
	} {
		got := [3]int{status(root, c.method, c.path), status(admin, c.method, c.path), status(user, c.method, c.path)}
		if got != [3]int{c.root, c.admin, c.plain} && !(c.path == "/api/sync" && got[0] != 403) {
			t.Errorf("%s %s: root/admin/user = %v, want %d/%d/%d", c.method, c.path, got, c.root, c.admin, c.plain)
		}
	}

	// the superuser cannot be handed out, changed or removed
	if call(h, "POST", "/api/users", map[string]string{"username": "eve", "password": "password-eve", "role": "superuser"}, root).Code != 400 {
		t.Fatal("no second superuser")
	}
	if call(h, "POST", "/api/users", map[string]string{"username": "BOB", "password": "password-bob", "role": "user"}, root).Code != 409 {
		t.Fatal("user names are unique, ignoring case")
	}
	if call(h, "PATCH", "/api/users/1", map[string]string{"role": "user"}, root).Code != 400 {
		t.Fatal("the superuser keeps the role")
	}
	if call(h, "DELETE", "/api/users/1", nil, root).Code != 400 {
		t.Fatal("the superuser cannot be deleted")
	}

	// promote bob: the next request is already allowed
	if call(h, "PATCH", "/api/users/3", map[string]string{"role": "admin"}, root).Code != 200 || status(user, "GET", "/api/health") != 200 {
		t.Fatal("a role change applies at once")
	}

	// a new password signs the old session out; deleting a user does too
	call(h, "PATCH", "/api/users/3", map[string]string{"password": "another-password"}, root)
	if status(user, "GET", "/api/channels") != 401 {
		t.Fatal("old session must stop working after a password change")
	}
	if call(h, "DELETE", "/api/users/2", nil, root).Code != 200 || status(admin, "GET", "/api/channels") != 401 {
		t.Fatal("a deleted user is signed out")
	}
}

func TestProfilesBelongToTheirOwner(t *testing.T) {
	h, _ := testServer(t)
	root := signIn(t, h)
	admin := addUser(t, h, root, "ann", "admin").cookie
	bob := addUser(t, h, root, "bob", "user").cookie
	cat := addUser(t, h, root, "cat", "user").cookie

	var p struct{ ID int }
	json.Unmarshal(call(h, "POST", "/api/profiles", map[string]string{"name": "Bob's"}, bob).Body.Bytes(), &p)
	call(h, "POST", "/api/profiles", map[string]string{"name": "Cat's"}, cat)

	count := func(c *http.Cookie) int {
		var l []map[string]any
		json.Unmarshal(call(h, "GET", "/api/profiles", nil, c).Body.Bytes(), &l)
		return len(l)
	}
	if count(bob) != 1 || count(cat) != 1 || count(admin) != 2 || count(root) != 2 {
		t.Fatalf("visible profiles: bob %d cat %d admin %d root %d", count(bob), count(cat), count(admin), count(root))
	}
	path := "/api/profiles/" + string(rune('0'+p.ID))
	if call(h, "GET", path, nil, cat).Code != 404 || call(h, "DELETE", path, nil, cat).Code != 404 ||
		call(h, "PATCH", path, map[string]string{"name": "mine now"}, cat).Code != 404 ||
		call(h, "POST", path+"/rotate-token", nil, cat).Code != 404 || call(h, "PUT", path+"/channels", map[string]any{"channels": []any{}}, cat).Code != 404 {
		t.Fatal("someone else's profile must look like it does not exist")
	}
	if call(h, "GET", path, nil, bob).Code != 200 || call(h, "GET", path, nil, admin).Code != 200 {
		t.Fatal("the owner and admins can open it")
	}

	// deleting a user deletes their profiles
	call(h, "DELETE", "/api/users/3", nil, root)
	if count(root) != 1 {
		t.Fatalf("profiles of a deleted user are removed: %d left", count(root))
	}
}

func TestChangeOwnPassword(t *testing.T) {
	h, _ := testServer(t)
	root := signIn(t, h)
	bob := addUser(t, h, root, "bob", "user").cookie
	if call(h, "POST", "/api/account/password", map[string]string{"current": "nope", "password": "new-password-1"}, bob).Code != 403 {
		t.Fatal("the current password must match")
	}
	rec := call(h, "POST", "/api/account/password", map[string]string{"current": "password-bob", "password": "new-password-1"}, bob)
	if rec.Code != 200 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if call(h, "GET", "/api/channels", nil, bob).Code != 401 {
		t.Fatal("the old cookie is dead")
	}
	if call(h, "GET", "/api/channels", nil, sessionOf(t, rec)).Code != 200 {
		t.Fatal("the new cookie from the change works")
	}
	if call(h, "POST", "/api/login", map[string]string{"username": "bob", "password": "new-password-1"}).Code != 200 {
		t.Fatal("sign in with the new password")
	}
}

func TestSessionReportsTheVersion(t *testing.T) {
	h, _ := testServer(t)
	var s struct {
		Version string `json:"version"`
	}
	json.Unmarshal(call(h, "GET", "/api/session", nil).Body.Bytes(), &s)
	if s.Version == "" {
		t.Fatal("the UI shows the version from the session")
	}
}
