package microvm

import "testing"

func TestParseForwardValid(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  Forward
	}{
		{"guest port only", "8080", Forward{GuestPort: 8080, HostPort: 8080, Protocol: "tcp"}},
		{"guest port with udp", "53/udp", Forward{GuestPort: 53, HostPort: 53, Protocol: "udp"}},
		{"host and guest port", "8443:443", Forward{HostPort: 8443, GuestPort: 443, Protocol: "tcp"}},
		{"host and guest port udp", "8443:443/udp", Forward{HostPort: 8443, GuestPort: 443, Protocol: "udp"}},
		{"ip host and guest port", "127.0.0.1:8443:443", Forward{HostIP: "127.0.0.1", HostPort: 8443, GuestPort: 443, Protocol: "tcp"}},
		{"bracketed ipv6 host", "[::1]:8443:443", Forward{HostIP: "::1", HostPort: 8443, GuestPort: 443, Protocol: "tcp"}},
		{"bracketed ipv6 host udp", "[::1]:53:53/udp", Forward{HostIP: "::1", HostPort: 53, GuestPort: 53, Protocol: "udp"}},
		{"boundary port 1", "1", Forward{GuestPort: 1, HostPort: 1, Protocol: "tcp"}},
		{"boundary port 65535", "65535", Forward{GuestPort: 65535, HostPort: 65535, Protocol: "tcp"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseForward(test.value)
			if err != nil {
				t.Fatalf("ParseForward(%q) unexpected error: %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("ParseForward(%q)=%#v want=%#v", test.value, got, test.want)
			}
		})
	}
}

func TestParseForwardInvalid(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{"empty value", ""},
		{"contains space", "84 43:443"},
		{"contains null byte", "8443:44\x003"},
		{"contains tab", "8443:4\t43"},
		{"invalid protocol", "8080/http"},
		{"non numeric port", "abc"},
		{"port zero", "0"},
		{"port too large", "65536"},
		{"host port too large", "70000:443"},
		{"guest port too large in pair", "443:70000"},
		{"too many parts", "1:2:3:4"},
		{"invalid host ip", "999.999.999.999:8443:443"},
		{"unterminated bracket", "[::1:8443:443"},
		{"bracket without following colon", "[::1]8443:443"},
		{"bracket at end of string", "[::1]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := ParseForward(test.value); err == nil {
				t.Fatalf("ParseForward(%q) accepted, got %#v", test.value, got)
			}
		})
	}
}
