package slug

import "testing"

func TestMake(t *testing.T) {
	cases := []struct{ in, sep, want string }{
		{"Dark Mode", "_", "dark_mode"},
		{"Account-Löschung", "_", "account_loeschung"},
		{"Account-Löschung", "-", "account-loeschung"},
		{"Größe & Straße", "-", "groesse-strasse"},
		{"  trim  me  ", "-", "trim-me"},
		{"UPPER Ä Ö Ü", "-", "upper-ae-oe-ue"},
		{"a!!!b", "_", "a_b"},
		{"...", "-", ""},
		{"日本語", "-", ""},
		{"", "-", ""},
	}
	for _, c := range cases {
		if got := Make(c.in, c.sep); got != c.want {
			t.Errorf("Make(%q, %q) = %q, want %q", c.in, c.sep, got, c.want)
		}
	}
}
