package microvm_test

import (
	"testing"

	legacy "github.com/CYPT71/secure-oci-base/api/microvm"
	sdk "github.com/CYPT71/secure-oci-base/sdk/microvm"
)

func TestLegacyTypesRemainSDKCompatible(t *testing.T) {
	var current sdk.Spec = legacy.Spec{Name: "demo"}
	var old legacy.Spec = current
	var currentForward sdk.Forward = legacy.Forward{HostPort: 8080, GuestPort: 80}
	var oldForward legacy.Forward = currentForward
	if old.Name != "demo" || oldForward.HostPort != 8080 {
		t.Fatal("legacy aliases changed values")
	}
	got, err := legacy.ParseForward("8080:80")
	if err != nil || got != (sdk.Forward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}) {
		t.Fatalf("legacy ParseForward() = %#v, %v", got, err)
	}
}
