package version

import "testing"

func TestString(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "ef711d2"
	if String() != "ef711d2" {
		t.Fatal("an image build shows its commit")
	}
	Version = "dev"
	if s := String(); s == "" || s[:3] != "dev" {
		t.Fatalf("a development build starts with dev: %q", s)
	}
}
