package ratelimit

import "testing"

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "five bytes is one token plus overhead and the default completion",
			body: `{"messages":[{"role":"user","content":"hello"}]}`,
			want: 5/4 + 4 + 1024,
		},
		{
			name: "seven bytes divide to one, not two",
			body: `{"messages":[{"content":"1234567"}]}`,
			want: 7/4 + 4 + 1024,
		},
		{
			name: "each message adds four, then max_tokens replaces the default",
			body: `{"messages":[{"content":"hello"},{"content":"hi"}],"max_tokens":16}`,
			want: (5+2)/4 + 8 + 16,
		},
		{
			name: "missing messages still reserves the default completion",
			body: `{}`,
			want: 1024,
		},
		{
			name: "invalid json uses the default completion",
			body: `not-json`,
			want: 1024,
		},
		{
			name: "empty content still counts the message",
			body: `{"messages":[{"content":""}],"max_tokens":8}`,
			want: 4 + 8,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateTokens([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("EstimateTokens = %d, want %d", got, tc.want)
			}
		})
	}
}
