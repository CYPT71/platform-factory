package v1_test

import (
	"testing"

	api "github.com/CYPT71/platform-factory/api/microvm/v1"
	sdk "github.com/CYPT71/platform-factory/sdk/microvm"
)

func TestLegacyTypesRemainSDKCompatible(t *testing.T) {
	var current sdk.Spec = api.Spec{Name: "demo"}
	var old api.Spec = current
	var currentForward sdk.Forward = api.Forward{HostPort: 8080, GuestPort: 80}
	var oldForward api.Forward = currentForward
	if old.Name != "demo" || oldForward.HostPort != 8080 {
		t.Fatal("legacy aliases changed values")
	}
	got, err := api.ParseForward("8080:80")
	if err != nil || got != (sdk.Forward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}) {
		t.Fatalf("legacy ParseForward() = %#v, %v", got, err)
	}
}
