package migration

import (
	"testing"

	apimigration "github.com/CYPT71/secure-oci-base/api/migration"
)

func TestNewPlanSealVerify(t *testing.T) {
	p := NewPlan(apimigration.DiscoveryStatusComplete)
	if err := Seal(&p); err != nil {
		t.Fatal(err)
	}
	if err := Verify(&p); err != nil {
		t.Fatal(err)
	}
	if p.Digest == "" {
		t.Fatal("Seal did not install digest")
	}
}
