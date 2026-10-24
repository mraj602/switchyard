package main

import (
	"reflect"
	"testing"
)

func TestShortIDs(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"5e1f0a7c-04cd", "9b3b5fb5-1111"}, []string{"5e1f0a7c", "9b3b5fb5"}},
		{[]string{"abcdefgh1234", "abcdefgh5678", "zzzzzzzz0000"}, []string{"abcdefgh1", "abcdefgh5", "zzzzzzzz"}},
		{[]string{"short", "abcdefgh"}, []string{"short", "abcdefgh"}},
		{[]string{"abcdefgh1234", "abcdefgh1234"}, []string{"abcdefgh", "abcdefgh"}},
	} {
		if got := shortIDs(c.in, 8); !reflect.DeepEqual(got, c.want) {
			t.Errorf("shortIDs(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
