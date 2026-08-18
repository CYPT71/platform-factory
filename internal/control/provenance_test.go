package control

import (
	"testing"

	"github.com/CYPT71/platform-factory/internal/provenance"
)

func TestVerifyCompletionProvenanceUnaffectedWithoutRegisteredKey(t *testing.T) {
	c := NewControlPlane(0)
	if err := c.RegisterWorkerWithOptions("worker-1", WorkerRegistration{Platform: "linux/amd64", MaxParallel: 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.VerifyCompletionProvenance("worker-1", "lease-1", nil); err != nil {
		t.Fatalf("worker without a registered key must never be blocked: %v", err)
	}
}

func TestVerifyCompletionProvenanceRequiresSignatureOnceKeyRegistered(t *testing.T) {
	identity, privateKey, err := provenance.GenerateWorkloadIdentity("worker-signed")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := provenance.NewWorkloadSigner(identity, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity, otherPrivateKey, err := provenance.GenerateWorkloadIdentity("attacker")
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := provenance.NewWorkloadSigner(otherIdentity, otherPrivateKey)
	if err != nil {
		t.Fatal(err)
	}

	newPlane := func(t *testing.T) *ControlPlane {
		t.Helper()
		c := NewControlPlane(0)
		if err := c.RegisterWorkerWithOptions("worker-signed", WorkerRegistration{
			Platform: "linux/amd64", MaxParallel: 1, PublicKey: identity.PublicKey,
		}); err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("unsigned completion is refused", func(t *testing.T) {
		c := newPlane(t)
		if err := c.VerifyCompletionProvenance("worker-signed", "lease-1", nil); err == nil {
			t.Fatal("expected an error for a missing record")
		}
	})

	t.Run("a signature from a different identity is refused", func(t *testing.T) {
		c := newPlane(t)
		record := &provenance.ProvenanceRecord{BuildID: "lease-1", WorkerID: "worker-signed"}
		if err := otherSigner.Sign(record); err != nil {
			t.Fatal(err)
		}
		if err := c.VerifyCompletionProvenance("worker-signed", "lease-1", record); err == nil {
			t.Fatal("expected an error for a signature from a different identity")
		}
	})

	t.Run("a record for a different lease is refused", func(t *testing.T) {
		c := newPlane(t)
		record := &provenance.ProvenanceRecord{BuildID: "some-other-lease", WorkerID: "worker-signed"}
		if err := signer.Sign(record); err != nil {
			t.Fatal(err)
		}
		if err := c.VerifyCompletionProvenance("worker-signed", "lease-1", record); err == nil {
			t.Fatal("expected an error for a record naming a different lease")
		}
	})

	t.Run("a correctly signed record for the right worker and lease is accepted", func(t *testing.T) {
		c := newPlane(t)
		record := &provenance.ProvenanceRecord{BuildID: "lease-1", WorkerID: "worker-signed"}
		if err := signer.Sign(record); err != nil {
			t.Fatal(err)
		}
		if err := c.VerifyCompletionProvenance("worker-signed", "lease-1", record); err != nil {
			t.Fatalf("expected a correctly signed record to be accepted: %v", err)
		}
	})
}
