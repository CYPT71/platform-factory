package plugin

import (
	"bufio"
	"strings"
	"testing"
)

// FuzzReadMessage feeds arbitrary bytes at the plugin RPC wire framing -
// the header-framed (Content-Type/Content-Length) protocol every
// out-of-process plugin, in any of the five supported languages, speaks
// over stdin/stdout. A plugin is attacker- or bug-controlled by
// construction, and this is the lowest-level parser standing between raw
// bytes on a pipe and a decoded message.
func FuzzReadMessage(f *testing.F) {
	f.Add("Content-Type: application/vnd.platform-factory.rpc.v1+json\r\nContent-Length: 2\r\n\r\n{}")
	f.Add("Content-Length: 5\r\n\r\nhello")
	f.Add("")
	f.Add("Content-Length: 5")
	f.Add("Content-Length: -1\r\n\r\n")
	f.Add("Content-Length: 999999999999\r\n\r\n")
	f.Add("Content-Type: text/plain\r\nContent-Length: 0\r\n\r\n")
	f.Add("garbage-header-no-colon\r\n\r\n")

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		_, _ = ReadMessage(bufio.NewReader(strings.NewReader(input)))
	})
}
