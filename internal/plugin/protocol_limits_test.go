package plugin

import (
	"bufio"
	"strings"
	"testing"
)

func TestHostReadMessageBoundsHeadersAndUnterminatedLines(t *testing.T) {
	for _, input := range []string{"X-Long: " + strings.Repeat("a", maxHeaderLineBytes+1), strings.Repeat("X: a\r\n", maxHeaderBytes/5) + "\r\n"} {
		if _, err := ReadMessage(bufio.NewReaderSize(strings.NewReader(input), maxHeaderBytes*2)); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("oversized header accepted: err=%v", err)
		}
	}
}
