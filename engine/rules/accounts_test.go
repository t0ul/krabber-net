package rules

import "testing"

func TestNet(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":            "203.0.113.7",
		"2001:db8:aa:bb:1:2:3:4": "2001:db8:aa:bb::/64",
		"2001:db8:aa:bb:ffff::9": "2001:db8:aa:bb::/64",
		"::ffff:203.0.113.7":     "::ffff:203.0.113.7",
		"not an address":         "not an address",
		"":                       "",
	}
	for in, want := range cases {
		if got := Net(in); got != want {
			t.Errorf("Net(%q) = %q, want %q", in, got, want)
		}
	}
}
