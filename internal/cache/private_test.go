package cache

import (
	"bytes"
	"os"
	"testing"
)

func TestPrivateStoreAdapterUsesPrivateModes(t *testing.T) {
	root := t.TempDir() + "/artifacts"
	s, err := OpenPrivateAdapter(root)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Put(bytes.NewReader([]byte("secret-sentinel")))
	if err != nil {
		t.Fatal(err)
	}
	hexDigest, _ := parseDigest(d.Digest)
	for _, p := range []string{root, s.store.blobs, s.store.blobPath(hexDigest)} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o700)
		if info.Mode().IsRegular() {
			want = 0o600
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%o want=%o", p, info.Mode().Perm(), want)
		}
	}
}
