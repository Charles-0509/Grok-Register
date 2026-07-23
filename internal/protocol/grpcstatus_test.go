package protocol

import "testing"

func TestParseGRPCStatusFromBody(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"grpc-status:0\r\ngrpc-message:\r\n", "0"},
		{"data...grpc-status:7\ngrpc-message:boom\n", "7"},
		{"no status here", ""},
		{"prefix grpc-status:0", "0"},
	}
	for _, tc := range cases {
		got := parseGRPCStatusFromBody([]byte(tc.in))
		if got != tc.want {
			t.Fatalf("in=%q got=%q want=%q", tc.in, got, tc.want)
		}
	}
}
