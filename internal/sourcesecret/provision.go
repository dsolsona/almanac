package sourcesecret

import (
	"bytes"
	"errors"

	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
)

var ErrProvisioning = errors.New("source secret keyset provisioning failed")

// Provision creates a first AES256GCM/TINK keyset with exclusive creation and
// owner-only file permissions. It never replaces an existing destination.
func Provision(path, keysetID string) error {
	if path == "" || !canonicalUUID(keysetID) {
		return ErrProvisioning
	}
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	if err != nil {
		return ErrProvisioning
	}
	var encoded bytes.Buffer
	if err := insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&encoded)); err != nil || encoded.Len() == 0 || encoded.Len() > MaxKeysetBytes {
		clear(encoded.Bytes())
		return ErrProvisioning
	}
	defer clear(encoded.Bytes())
	if err := secretfile.WriteNew(path, encoded.Bytes()); err != nil {
		return ErrProvisioning
	}
	if _, err := Load(path, keysetID); err != nil {
		return ErrProvisioning
	}
	return nil
}
