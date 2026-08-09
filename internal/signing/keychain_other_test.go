//go:build !(darwin && cgo)

package signing

import "testing"

func TestNewKeychainKeyStoreUnsupportedOnThisPlatform(t *testing.T) {
	if _, err := NewKeychainKeyStore("path", "password"); err == nil {
		t.Fatal("NewKeychainKeyStore succeeded on a platform without keychain support")
	}
}
