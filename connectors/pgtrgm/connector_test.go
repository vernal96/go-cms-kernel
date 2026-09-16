package pgtrgm

import "testing"

func TestContainsPatternEscapesMetacharacters(t *testing.T) {
	if got := ContainsPattern(`a%b_c\d`); got != `%a\%b\_c\\d%` {
		t.Fatalf("pattern = %q", got)
	}
}
