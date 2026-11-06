package version

import "testing"

func TestString(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v1.2.0"
	if String() != "v1.2.0" {
		t.Fatal("a release shows its tag")
	}
	Version = "dev"
	if s := String(); s == "" || s[:3] != "dev" {
		t.Fatalf("a development build starts with dev: %q", s)
	}
}
