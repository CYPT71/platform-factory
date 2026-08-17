package microvm

import "testing"

// FuzzParseForward feeds arbitrary strings at the --publish/-p/--port
// value parser (PORT, HOST:GUEST, or IP:HOST:GUEST, optional /tcp or
// /udp) - direct, unvalidated CLI/config input for platform-factory run
// --isolation microvm.
func FuzzParseForward(f *testing.F) {
	f.Add("8080")
	f.Add("127.0.0.1:8080:80")
	f.Add("127.0.0.1:8080:80/tcp")
	f.Add("8080:80/udp")
	f.Add("")
	f.Add(":::")
	f.Add("999999999999:80/tcp")
	f.Add("127.0.0.1:8080:80/sctp")

	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			t.Skip()
		}
		_, _ = ParseForward(value)
	})
}
