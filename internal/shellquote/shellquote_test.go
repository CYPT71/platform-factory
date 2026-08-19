package shellquote

import "testing"

func TestCommandQuotesArgumentsThatNeedIt(t *testing.T) {
	if got := Command("tool", []string{"plain", "two words", "it's"}); got != `tool plain 'two words' 'it'\''s'` {
		t.Fatalf("got=%q", got)
	}
}

func TestCommandQuotesEmptyArguments(t *testing.T) {
	if got := Command("tool", []string{""}); got != "tool ''" {
		t.Fatalf("got=%q", got)
	}
}

func TestCommandLeavesSimpleArgumentsUnquoted(t *testing.T) {
	if got := Command("go", []string{"build", "./..."}); got != "go build ./..." {
		t.Fatalf("got=%q", got)
	}
}
