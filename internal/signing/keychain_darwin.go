//go:build darwin && cgo

// This file binds to a deliberately small slice of Security.framework:
// create/open/unlock one explicit keychain file, and get/set an opaque
// generic-password secret in it. The Ed25519 math itself stays in Go
// (crypto/ed25519); the keychain is used purely as secure storage for the
// private key seed, not as a signing oracle via SecKeyCreateSignature —
// Apple's native SecKey Ed25519 support is inconsistent across macOS
// versions, while "store/retrieve an opaque secret" is a small,
// well-documented, low-risk surface.
package signing

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security

#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <string.h>

// The SecKeychain* API has been marked deprecated since macOS 10.10 with
// no direct replacement for this exact use case (an explicit, separate
// keychain file holding opaque secrets); the modern item-based API
// (kSecUseKeychain etc.) is a materially larger surface for the same
// result. Silence the warning deliberately rather than pretend it isn't
// there.
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"

static const char *serviceName = "platform-factory-signing";

static OSStatus scOpenAndUnlock(const char *path, const char *password, SecKeychainRef *keychain) {
	OSStatus status = SecKeychainOpen(path, keychain);
	if (status != errSecSuccess) {
		return status;
	}
	return SecKeychainUnlock(*keychain, (UInt32)strlen(password), password, TRUE);
}

static OSStatus scCreate(const char *path, const char *password, SecKeychainRef *keychain) {
	return SecKeychainCreate(path, (UInt32)strlen(password), password, FALSE, NULL, keychain);
}

static OSStatus scFindSecret(SecKeychainRef keychain, const char *account, void **secret, UInt32 *secretLen) {
	return SecKeychainFindGenericPassword(keychain,
		(UInt32)strlen(serviceName), serviceName,
		(UInt32)strlen(account), account,
		secretLen, secret, NULL);
}

static OSStatus scAddSecret(SecKeychainRef keychain, const char *account, const void *secret, UInt32 secretLen) {
	return SecKeychainAddGenericPassword(keychain,
		(UInt32)strlen(serviceName), serviceName,
		(UInt32)strlen(account), account,
		secretLen, secret, NULL);
}

static void scFreeContent(void *secret) {
	SecKeychainItemFreeContent(NULL, secret);
}

#pragma clang diagnostic pop
*/
import "C"

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"
)

// KeychainKeyStore stores Ed25519 key seeds as generic-password items in
// one explicit macOS keychain file (never the user's login keychain).
type KeychainKeyStore struct {
	keychain C.SecKeychainRef
	mu       sync.Mutex
}

// NewKeychainKeyStore opens the keychain at path, creating it (protected
// by password) if it does not already exist.
func NewKeychainKeyStore(path, password string) (KeyStore, error) {
	if path == "" || password == "" {
		return nil, errors.New("signing: keychain path and password are required")
	}
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	cPassword := C.CString(password)
	defer C.free(unsafe.Pointer(cPassword))

	var keychain C.SecKeychainRef
	var status C.OSStatus
	if _, err := os.Stat(path); err == nil {
		status = C.scOpenAndUnlock(cPath, cPassword, &keychain)
	} else if os.IsNotExist(err) {
		status = C.scCreate(cPath, cPassword, &keychain)
	} else {
		return nil, fmt.Errorf("signing: stat keychain path: %w", err)
	}
	if status != C.errSecSuccess {
		return nil, fmt.Errorf("signing: open/create keychain: OSStatus %d", int(status))
	}
	return &KeychainKeyStore{keychain: keychain}, nil
}

// PublicKey implements KeyStore.
func (k *KeychainKeyStore) PublicKey(name string) (ed25519.PublicKey, error) {
	priv, err := k.loadOrGenerate(name)
	if err != nil {
		return nil, err
	}
	public, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("signing: unexpected public key type")
	}
	return public, nil
}

// Sign implements KeyStore.
func (k *KeychainKeyStore) Sign(name string, message []byte) ([]byte, error) {
	priv, err := k.loadOrGenerate(name)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, message), nil
}

func (k *KeychainKeyStore) loadOrGenerate(name string) (ed25519.PrivateKey, error) {
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("signing: invalid key name %q", name)
	}
	k.mu.Lock()
	defer k.mu.Unlock()

	cAccount := C.CString(name)
	defer C.free(unsafe.Pointer(cAccount))

	var secret unsafe.Pointer
	var secretLen C.UInt32
	status := C.scFindSecret(k.keychain, cAccount, &secret, &secretLen)
	if status == C.errSecSuccess {
		seed := C.GoBytes(secret, C.int(secretLen))
		C.scFreeContent(secret)
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("signing: keychain item %q is not a valid Ed25519 seed", name)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if status != C.errSecItemNotFound {
		return nil, fmt.Errorf("signing: read keychain item %q: OSStatus %d", name, int(status))
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("signing: generate key %q: %w", name, err)
	}
	cSeed := C.CBytes(seed)
	defer C.free(cSeed)
	status = C.scAddSecret(k.keychain, cAccount, cSeed, C.UInt32(len(seed)))
	if status != C.errSecSuccess {
		return nil, fmt.Errorf("signing: store keychain item %q: OSStatus %d", name, int(status))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
