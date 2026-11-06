package auth

import "testing"

func TestHashVerify(t *testing.T) {
	h := Hash("correct horse")
	if !Verify(h, "correct horse") || Verify(h, "wrong") || Verify("junk", "x") || Verify("", "") {
		t.Fatal("password check")
	}
	if Hash("correct horse") == h {
		t.Fatal("hashes must be salted")
	}
	if Fingerprint(h) == Fingerprint(Hash("correct horse")) {
		t.Fatal("fingerprint follows the hash")
	}
}

func TestPermissions(t *testing.T) {
	if Can(User, Health) || Can(User, AllProfiles) || Can(User, Settings) {
		t.Fatal("a plain user has no extra permissions")
	}
	if !Can(Admin, Health) || !Can(Admin, Sources) || !Can(Admin, AllProfiles) || Can(Admin, Settings) || Can(Admin, Users) {
		t.Fatal("an admin sees everything except settings and users")
	}
	if !Can(Superuser, Settings) || !Can(Superuser, Users) {
		t.Fatal("the superuser can do everything")
	}
	if ValidRole(Superuser) || !ValidRole(Admin) || !ValidRole(User) || ValidRole("root") {
		t.Fatal("the superuser role cannot be handed out")
	}
	if CheckUsername("ab") == nil || CheckUsername("a b c") == nil || CheckUsername("ok.name-1") != nil {
		t.Fatal("user names")
	}
}
