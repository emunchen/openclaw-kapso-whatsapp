package routing

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"+5492615562747", "5492615562747"},
		{"+542615562747", "5492615562747"},
		{"5492615562747", "5492615562747"},
		{"542615562747", "5492615562747"},
		{"+54 9 261 556-2747", "5492615562747"},
		{"+1234567890", "1234567890"},
		{"+5491123456789", "5491123456789"},
		{"+541123456789", "5491123456789"},
		{"549", "549"},
		{"54", "54"},
		{"", ""},
	}
	for _, tc := range cases {
		got := normalize(tc.in)
		if got != tc.want {
			t.Errorf("normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
