package model

import "testing"

func TestSegments(t *testing.T) {
	for _, s := range []string{"abc", "...", "a..b", "A_0-x"} {
		if !ValidSegment(s) {
			t.Errorf("rejected %q", s)
		}
	}
	for _, s := range []string{"", ".", "..", "a/b", `a\b`, "a:b"} {
		if ValidSegment(s) {
			t.Errorf("accepted %q", s)
		}
	}
	if _, err := (Repository{Owner: "..", Name: "x"}).RepoDir("root"); err == nil {
		t.Fatal("accepted traversal")
	}
}
