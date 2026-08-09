package networking

import "testing"

// ParseForward itself is tested in sdk/microvm, where it is defined; this
// only checks the alias resolves to a working function.
func TestParseForwardAlias(t *testing.T) {
	if _, err := ParseForward("8080"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseForward(""); err == nil {
		t.Fatal("accepted empty value")
	}
}

func TestNetworkValidation(t *testing.T) {
	for _, value := range []string{"none", "bridge", "production_net"} {
		if err := ValidateNetwork(value, false); err != nil {
			t.Fatal(err)
		}
	}
	if ValidateNetwork("host", false) == nil || ValidateNetwork("host", true) != nil {
		t.Fatal("host network opt-in not enforced")
	}
	if ValidateDNS("2001:4860:4860::8888") != nil || ValidateDNS("resolver") == nil {
		t.Fatal("DNS validation mismatch")
	}
	if ValidateHostname("app.example") != nil || ValidateHostname("../bad") == nil {
		t.Fatal("hostname validation mismatch")
	}
	if ValidateAddHost("database:127.0.0.1") != nil || ValidateAddHost("bad") == nil {
		t.Fatal("add-host validation mismatch")
	}
}
