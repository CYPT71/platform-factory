//go:build !(darwin && cgo)

package signing

import "errors"

// NewKeychainKeyStore is not supported on this platform.
func NewKeychainKeyStore(path, password string) (KeyStore, error) {
	return nil, errors.New("signing: keychain key storage is only supported on macOS with cgo enabled")
}
