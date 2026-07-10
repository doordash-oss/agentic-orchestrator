package server

import "testing"

func TestRedactedRequestURLStripsAccessToken(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/api/v1/events?access_token=secret&after=5", "/api/v1/events?after=5"},
		{"/api/v1/sessions/sess-1/output/stream?access_token=secret", "/api/v1/sessions/sess-1/output/stream"},
		{"/api/v1/features", "/api/v1/features"},
	}
	for _, tc := range cases {
		if got := redactedRequestURL(tc.in); got != tc.want {
			t.Errorf("redactedRequestURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
