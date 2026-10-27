// Package sourcesecret encrypts recoverable datasource secrets using a protected Tink keyset.
package sourcesecret

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"regexp"
	"strings"
	"uuid"

	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/aead/aesgcm"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/proto/tink_go_proto"
)

const (
	FormatVersion     uint32 = 1
	MaxPlaintextBytes        = 16 << 10
	MaxEnvelopeBytes         = 20 << 10
	MaxKeysetBytes           = 16 << 10
	maxKeyCount              = 16
	keysetTypeURL            = "type.googleapis.com/google.crypto.tink.AesGcmKey"
)

var (
	ErrInvalidKeyset = errors.New("source secret keyset is invalid")
	ErrInvalidSecret = errors.New("source secret operation failed")
	uuidPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	namePattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)
)

// Binding is trusted caller context. Values must come from authoritative application state.
type Binding struct {
	InstallationID string
	ResourceKind   string
	ResourceID     string
	Field          string
	Revision       uint64
}

// Envelope is untrusted storage metadata and ciphertext. Decrypt also requires the expected
// Binding supplied independently by the caller.
type Envelope struct {
	FormatVersion uint32 `json:"format_version"`
	KeysetID      string `json:"keyset_id"`
	KeyID         uint32 `json:"key_id"`
	Ciphertext    []byte `json:"ciphertext"`
}

// Adapter provides authenticated encryption with the configured Tink keyset.
type Adapter struct {
	keysetID  string
	primitive interface {
		Encrypt([]byte, []byte) ([]byte, error)
		Decrypt([]byte, []byte) ([]byte, error)
	}
	keyIDs    map[uint32]struct{}
	primaryID uint32
}

// New validates a loaded keyset and constructs an adapter. It never creates keys.
func New(keysetID string, handle *keyset.Handle) (*Adapter, error) {
	if !canonicalUUID(keysetID) || handle == nil || !validKeyset(handle) {
		return nil, ErrInvalidKeyset
	}
	primitive, err := aead.New(handle)
	if err != nil {
		return nil, ErrInvalidKeyset
	}
	keyIDs := make(map[uint32]struct{}, handle.Len())
	for _, info := range handle.KeysetInfo().GetKeyInfo() {
		if info.GetStatus() == tink_go_proto.KeyStatusType_ENABLED {
			keyIDs[info.GetKeyId()] = struct{}{}
		}
	}
	return &Adapter{keysetID: keysetID, primitive: primitive, keyIDs: keyIDs, primaryID: handle.KeysetInfo().GetPrimaryKeyId()}, nil
}

// Load reads and validates a cleartext Tink JSON keyset from a protected regular file.
// The mounted file is plaintext key material and must remain within the deployment-secret boundary.
func Load(path, keysetID string) (*Adapter, error) {
	if !canonicalUUID(keysetID) {
		return nil, ErrInvalidKeyset
	}
	data, err := secretfile.Read(path, MaxKeysetBytes)
	if err != nil {
		return nil, ErrInvalidKeyset
	}
	defer clear(data)
	handle, err := insecurecleartextkeyset.Read(keyset.NewJSONReader(bytes.NewReader(data)))
	if err != nil {
		return nil, ErrInvalidKeyset
	}
	return New(keysetID, handle)
}

// Encrypt encrypts a field with the current primary key.
func (a *Adapter) Encrypt(binding Binding, plaintext []byte) (Envelope, error) {
	if a == nil || a.primitive == nil || !validBinding(binding) || len(plaintext) > MaxPlaintextBytes {
		return Envelope{}, ErrInvalidSecret
	}
	associatedData, err := bindingData(binding, a.keysetID, a.primaryID)
	if err != nil {
		return Envelope{}, ErrInvalidSecret
	}
	ciphertext, err := a.primitive.Encrypt(plaintext, associatedData)
	if err != nil || len(ciphertext) < tinkPrefixBytes || len(ciphertext) > MaxEnvelopeBytes || ciphertext[0] != tinkOutputPrefix {
		return Envelope{}, ErrInvalidSecret
	}
	keyID := binary.BigEndian.Uint32(ciphertext[1:tinkPrefixBytes])
	if keyID == 0 {
		return Envelope{}, ErrInvalidSecret
	}
	if keyID != a.primaryID {
		clear(ciphertext)
		return Envelope{}, ErrInvalidSecret
	}
	return Envelope{FormatVersion: FormatVersion, KeysetID: a.keysetID, KeyID: keyID, Ciphertext: ciphertext}, nil
}

// Decrypt authenticates ciphertext against the expected caller binding and envelope metadata.
func (a *Adapter) Decrypt(binding Binding, envelope Envelope) ([]byte, error) {
	if a == nil || a.primitive == nil || !validBinding(binding) || envelope.FormatVersion != FormatVersion ||
		!canonicalUUID(envelope.KeysetID) || envelope.KeysetID != a.keysetID || envelope.KeyID == 0 ||
		len(envelope.Ciphertext) < tinkPrefixBytes+aesGCMOverhead || len(envelope.Ciphertext) > MaxEnvelopeBytes ||
		envelope.Ciphertext[0] != tinkOutputPrefix || binary.BigEndian.Uint32(envelope.Ciphertext[1:tinkPrefixBytes]) != envelope.KeyID {
		return nil, ErrInvalidSecret
	}
	if _, ok := a.keyIDs[envelope.KeyID]; !ok {
		return nil, ErrInvalidSecret
	}
	associatedData, err := bindingData(binding, envelope.KeysetID, envelope.KeyID)
	if err != nil {
		return nil, ErrInvalidSecret
	}
	plaintext, err := a.primitive.Decrypt(envelope.Ciphertext, associatedData)
	if err != nil || len(plaintext) > MaxPlaintextBytes {
		clear(plaintext)
		return nil, ErrInvalidSecret
	}
	return plaintext, nil
}

const (
	tinkPrefixBytes  = 5
	tinkOutputPrefix = byte(1)
	aesGCMOverhead   = 12 + 16
)

func validKeyset(handle *keyset.Handle) bool {
	if handle.Len() < 1 || handle.Len() > maxKeyCount {
		return false
	}
	info := handle.KeysetInfo()
	if info == nil || info.GetPrimaryKeyId() == 0 || len(info.GetKeyInfo()) != handle.Len() {
		return false
	}
	ids := make(map[uint32]struct{}, handle.Len())
	primaryCount := 0
	for i, keyInfo := range info.GetKeyInfo() {
		if keyInfo == nil || keyInfo.GetKeyId() == 0 || keyInfo.GetOutputPrefixType() != tink_go_proto.OutputPrefixType_TINK || keyInfo.GetTypeUrl() != keysetTypeURL {
			return false
		}
		if keyInfo.GetStatus() != tink_go_proto.KeyStatusType_ENABLED && keyInfo.GetStatus() != tink_go_proto.KeyStatusType_DISABLED {
			return false
		}
		if _, exists := ids[keyInfo.GetKeyId()]; exists {
			return false
		}
		ids[keyInfo.GetKeyId()] = struct{}{}
		if keyInfo.GetKeyId() == info.GetPrimaryKeyId() {
			primaryCount++
			if keyInfo.GetStatus() != tink_go_proto.KeyStatusType_ENABLED {
				return false
			}
		}
		entry, err := handle.Entry(i)
		if err != nil || entry == nil || entry.Key() == nil {
			return false
		}
		parameters, ok := entry.Key().Parameters().(*aesgcm.Parameters)
		if !ok || parameters.KeySizeInBytes() != 32 || parameters.Variant() != aesgcm.VariantTink {
			return false
		}
	}
	return primaryCount == 1
}

func bindingData(binding Binding, keysetID string, keyID uint32) ([]byte, error) {
	if !validBinding(binding) || !canonicalUUID(keysetID) || keyID == 0 {
		return nil, ErrInvalidSecret
	}
	return json.Marshal([]any{"almanac.secret", FormatVersion, binding.InstallationID, binding.ResourceKind, binding.ResourceID, binding.Field, binding.Revision, keysetID, keyID})
}

func validBinding(binding Binding) bool {
	return canonicalUUID(binding.InstallationID) && canonicalUUID(binding.ResourceID) &&
		validName(binding.ResourceKind, 64, false) && validName(binding.Field, 128, true) && binding.Revision > 0
}

func validName(value string, maxLength int, dotted bool) bool {
	if len(value) == 0 || len(value) > maxLength || !namePattern.MatchString(value) {
		return false
	}
	return dotted || !strings.ContainsRune(value, '.')
}

func canonicalUUID(value string) bool {
	if !uuidPattern.MatchString(value) {
		return false
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
