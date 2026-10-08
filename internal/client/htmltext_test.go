package client

import "testing"

func TestHasHTMLClassToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		classes string
		want    string
		match   bool
	}{
		{name: "success with layout class", classes: "flex text-success", want: "text-success", match: true},
		{name: "error", classes: "text-error", want: "text-error", match: true},
		{name: "task preview", classes: "text-sm truncate", want: "truncate", match: true},
		{name: "substring is not a token", classes: "text-successful", want: "text-success"},
		{name: "unrelated classes", classes: "font-medium text-sm", want: "truncate"},
		{name: "case sensitive", classes: "Text-success", want: "text-success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasHTMLClassToken(tc.classes, tc.want); got != tc.match {
				t.Fatalf("hasHTMLClassToken(%q, %q) = %t, want %t", tc.classes, tc.want, got, tc.match)
			}
		})
	}
}
