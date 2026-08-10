package state

import "testing"

// The rename must not cost a node its identity: /var/lib/logos-vpn holds the
// device key, and silently creating a fresh one would give the node a new
// overlay address and make it a stranger to every peer it knows.
func TestPickPath(t *testing.T) {
	none := func(string) bool { return false }
	only := func(want string) func(string) bool {
		return func(p string) bool { return p == want }
	}

	cases := []struct {
		name                        string
		explicit, preferred, legacy string
		exists                      func(string) bool
		want                        string
	}{
		{"neither exists: a fresh install lands on the new path",
			"/new", "/new", "/old", none, "/new"},
		{"only the pre-rename path exists: keep using it",
			"/new", "/new", "/old", only("/old"), "/old"},
		{"the new path exists: prefer it even if the old one lingers",
			"/new", "/new", "/old", func(string) bool { return true }, "/new"},
		{"an explicit path is never rewritten",
			"/somewhere", "/new", "/old", func(string) bool { return true }, "/somewhere"},
	}
	for _, c := range cases {
		if got := pickPath(c.explicit, c.preferred, c.legacy, c.exists); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
