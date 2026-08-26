package main

import "testing"

// Minting made exactly two admin keys, so both config writers used keys[0] and
// keys[1] literally. A card-backed mesh has one, and joining it crashed the
// joining device — after writing its config and before storing its credential,
// which leaves a machine holding a network key and no membership.
func TestAdminKeysAreWrittenHoweverManyThereAre(t *testing.T) {
	for _, c := range []struct {
		keys []string
		want string
	}{
		{[]string{"AAA"}, `["AAA"]`},
		{[]string{"AAA", "BBB"}, `["AAA", "BBB"]`},
		{[]string{"AAA", "BBB", "CCC"}, `["AAA", "BBB", "CCC"]`},
		{nil, `[]`},
	} {
		if got := quotedList(c.keys); got != c.want {
			t.Errorf("%d keys wrote %s, want %s", len(c.keys), got, c.want)
		}
	}
}
