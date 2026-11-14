package secrets

import (
	"errors"

	"github.com/zalando/go-keyring"
)

type OSKeyring struct{}

// Get reads an item.
func (OSKeyring) Get(service, key string) (string, error) {
	v, err := keyring.Get(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrKeyringItemNotFound
	}
	return v, err
}

// Set stores an item, replacing any existing value.
func (OSKeyring) Set(service, key, value string) error {
	return keyring.Set(service, key, value)
}
