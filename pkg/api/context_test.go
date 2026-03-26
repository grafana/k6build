package api

import (
	"context"
	"testing"
)

func TestNoCache(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title    string
		setVal   *bool // nil means don't set
		expected bool
	}{
		{
			title:    "not set returns false",
			setVal:   nil,
			expected: false,
		},
		{
			title:    "set to true returns true",
			setVal:   boolPtr(true),
			expected: true,
		},
		{
			title:    "set to false returns false",
			setVal:   boolPtr(false),
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if tc.setVal != nil {
				ctx = WithNoCache(ctx, *tc.setVal)
			}
			got := NoCache(ctx)
			if got != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}
