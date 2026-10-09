// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package keys

import (
	"errors"

	"github.com/keybase/go-keychain"
)

const keychainService = "dev.portenv.repository"

// Keychain keeps keys in the user's macOS Keychain, readable only when the
// device is unlocked and never synced to other devices.
type Keychain struct{}

// Get implements Store.
func (Keychain) Get(boxID string) ([]byte, error) {
	b, err := keychain.GetGenericPassword(keychainService, boxID, "", "")
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrNotFound
	}
	return b, nil
}

// Put implements Store. It never overwrites an existing key.
func (Keychain) Put(boxID string, key []byte) error {
	item := keychain.NewGenericPassword(keychainService, boxID, "Portenv repository key ("+boxID+")", key, "")
	item.SetSynchronizable(keychain.SynchronizableNo)
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	err := keychain.AddItem(item)
	if errors.Is(err, keychain.ErrorDuplicateItem) {
		return errors.New("a key for box " + boxID + " already exists in the Keychain")
	}
	return err
}

// Default is the platform's key store.
func Default(string) Store { return Keychain{} }
