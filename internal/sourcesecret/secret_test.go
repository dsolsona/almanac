package sourcesecret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/proto/tink_go_proto"
	"google.golang.org/protobuf/proto"
)

const testKeysetID = "c79c723a-81a2-4e0f-ae09-f2a071b2ef45"

func testBinding() Binding {
	return Binding{
		InstallationID: "7d1b861f-21b4-49b5-a891-3d3ae72db2e1",
		ResourceKind:   "datasource",
		ResourceID:     "a55332e8-b2b9-4a2b-ae89-141a326bfe70",
		Field:          "auth.password",
		Revision:       3,
	}
}

func TestEncryptDecryptEmptyAndMaximumPlaintext(t *testing.T) {
	adapter := testAdapter(t, testHandle(t))
	for _, plaintext := range [][]byte{{}, bytes.Repeat([]byte("x"), MaxPlaintextBytes)} {
		envelope, err := adapter.Encrypt(testBinding(), plaintext)
		if err != nil {
			t.Fatalf("Encrypt() length=%d: %v", len(plaintext), err)
		}
		got, err := adapter.Decrypt(testBinding(), envelope)
		if err != nil || !bytes.Equal(got, plaintext) {
			t.Fatalf("Decrypt() length=%d matched_expected=%t err=%v", len(plaintext), bytes.Equal(got, plaintext), err)
		}
	}
}

func TestEncryptRejectsOversizedPlaintextAndDecryptRejectsOversizedEnvelope(t *testing.T) {
	adapter := testAdapter(t, testHandle(t))
	if _, err := adapter.Encrypt(testBinding(), make([]byte, MaxPlaintextBytes+1)); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("oversized plaintext error=%v", err)
	}
	envelope, err := adapter.Encrypt(testBinding(), []byte("secret"))
	if err != nil {
		t.Fatal("Encrypt() failed")
	}
	envelope.Ciphertext = make([]byte, MaxEnvelopeBytes+1)
	if _, err := adapter.Decrypt(testBinding(), envelope); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("oversized envelope error=%v", err)
	}
}

func TestDecryptRejectsCiphertextAndTrustedBindingSubstitution(t *testing.T) {
	adapter := testAdapter(t, testHandle(t))
	envelope, err := adapter.Encrypt(testBinding(), []byte("fixture secret"))
	if err != nil {
		t.Fatal("Encrypt() failed")
	}
	tests := map[string]func(*Envelope, *Binding){
		"ciphertext":    func(e *Envelope, _ *Binding) { e.Ciphertext[len(e.Ciphertext)-1] ^= 1 },
		"installation":  func(_ *Envelope, b *Binding) { b.InstallationID = "e5d96fc1-5416-4a17-a5f4-ec0bb061f07c" },
		"resource kind": func(_ *Envelope, b *Binding) { b.ResourceKind = "notification" },
		"resource":      func(_ *Envelope, b *Binding) { b.ResourceID = "e5d96fc1-5416-4a17-a5f4-ec0bb061f07c" },
		"field":         func(_ *Envelope, b *Binding) { b.Field = "auth.token" },
		"revision":      func(_ *Envelope, b *Binding) { b.Revision++ },
		"keyset header": func(e *Envelope, _ *Binding) { e.KeysetID = "e5d96fc1-5416-4a17-a5f4-ec0bb061f07c" },
		"key ID header": func(e *Envelope, _ *Binding) { e.KeyID++ },
		"format header": func(e *Envelope, _ *Binding) { e.FormatVersion++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := cloneEnvelope(envelope)
			binding := testBinding()
			mutate(&changed, &binding)
			if _, err := adapter.Decrypt(binding, changed); !errors.Is(err, ErrInvalidSecret) {
				t.Fatalf("Decrypt() error=%v", err)
			}
		})
	}
}

func TestDecryptRetainedKeyAndEncryptUsesCurrentPrimary(t *testing.T) {
	handle := testHandle(t)
	old, err := New(testKeysetID, handle)
	if err != nil {
		t.Fatal("create initial adapter failed")
	}
	oldEnvelope, err := old.Encrypt(testBinding(), []byte("old secret"))
	if err != nil {
		t.Fatal("old Encrypt() failed")
	}

	manager := keyset.NewManagerFromHandle(handle)
	newID, err := manager.Add(aead.AES256GCMKeyTemplate())
	if err != nil {
		t.Fatal("add replacement key failed")
	}
	if err := manager.SetPrimary(newID); err != nil {
		t.Fatal("set current primary failed")
	}
	rotated, err := manager.Handle()
	if err != nil {
		t.Fatal("read rotated keyset failed")
	}
	retained, err := New(testKeysetID, rotated)
	if err != nil {
		t.Fatalf("New() with retained key failed: %v", err)
	}
	if got, err := retained.Decrypt(testBinding(), oldEnvelope); err != nil || string(got) != "old secret" {
		t.Fatalf("Decrypt() retained key matched_expected=%t err=%v", string(got) == "old secret", err)
	}
	newEnvelope, err := retained.Encrypt(testBinding(), []byte("new secret"))
	if err != nil || newEnvelope.KeyID != newID {
		t.Fatalf("Encrypt() key_id_matches_primary=%t err=%v", newEnvelope.KeyID == newID, err)
	}
}

func TestDecryptRejectsUnrelatedKeysetWithSameConfiguredUUID(t *testing.T) {
	original := testHandle(t)
	originalAdapter, err := New(testKeysetID, original)
	if err != nil {
		t.Fatal("create original adapter failed")
	}
	envelope, err := originalAdapter.Encrypt(testBinding(), []byte("bound value"))
	if err != nil {
		t.Fatal("encrypt original value failed")
	}

	unrelated := testHandle(t)
	originalData := insecurecleartextkeyset.KeysetMaterial(original).GetKey()[0].GetKeyData().GetValue()
	unrelatedMaterial := proto.Clone(insecurecleartextkeyset.KeysetMaterial(unrelated)).(*tink_go_proto.Keyset)
	if bytes.Equal(originalData, unrelatedMaterial.GetKey()[0].GetKeyData().GetValue()) {
		t.Fatal("independent test keysets unexpectedly have equal key material")
	}
	unrelatedMaterial.PrimaryKeyId = envelope.KeyID
	unrelatedMaterial.Key[0].KeyId = envelope.KeyID
	unrelatedHandle := readRawKeyset(t, unrelatedMaterial)
	unrelatedAdapter, err := New(testKeysetID, unrelatedHandle)
	if err != nil {
		t.Fatal("create unrelated adapter with matching key ID failed")
	}
	if _, err := unrelatedAdapter.Decrypt(testBinding(), envelope); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Decrypt() unrelated key material error=%v", err)
	}
}

func TestDisabledRetainedKeyCannotDecryptButPrimaryRemainsUsable(t *testing.T) {
	handle := testHandle(t)
	originalAdapter, err := New(testKeysetID, handle)
	if err != nil {
		t.Fatal("create original adapter failed")
	}
	oldEnvelope, err := originalAdapter.Encrypt(testBinding(), []byte("retained value"))
	if err != nil {
		t.Fatal("encrypt retained value failed")
	}

	manager := keyset.NewManagerFromHandle(handle)
	primaryID, err := manager.Add(aead.AES256GCMKeyTemplate())
	if err != nil {
		t.Fatal("add current primary failed")
	}
	if err := manager.SetPrimary(primaryID); err != nil {
		t.Fatal("promote current primary failed")
	}
	if err := manager.Disable(oldEnvelope.KeyID); err != nil {
		t.Fatal("disable retained key failed")
	}
	rotated, err := manager.Handle()
	if err != nil {
		t.Fatal("read rotated keyset failed")
	}
	adapter, err := New(testKeysetID, rotated)
	if err != nil {
		t.Fatalf("New() with disabled retained key failed: %v", err)
	}
	if _, err := adapter.Decrypt(testBinding(), oldEnvelope); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Decrypt() disabled retained key error=%v", err)
	}
	newEnvelope, err := adapter.Encrypt(testBinding(), []byte("current value"))
	if err != nil || newEnvelope.KeyID != primaryID {
		t.Fatalf("Encrypt() current primary selected=%t err=%v", newEnvelope.KeyID == primaryID, err)
	}
	if got, err := adapter.Decrypt(testBinding(), newEnvelope); err != nil || string(got) != "current value" {
		t.Fatalf("Decrypt() current primary matched_expected=%t err=%v", string(got) == "current value", err)
	}
}

func TestDecryptRejectsEnvelopeKeyMissingFromConfiguredKeyset(t *testing.T) {
	original := testAdapter(t, testHandle(t))
	envelope, err := original.Encrypt(testBinding(), []byte("value"))
	if err != nil {
		t.Fatal("encrypt value failed")
	}
	missingID := envelope.KeyID + 1
	if missingID == 0 {
		missingID = envelope.KeyID - 1
	}
	unrelatedMaterial := proto.Clone(insecurecleartextkeyset.KeysetMaterial(testHandle(t))).(*tink_go_proto.Keyset)
	unrelatedMaterial.PrimaryKeyId = missingID
	unrelatedMaterial.Key[0].KeyId = missingID
	missingHandle := readRawKeyset(t, unrelatedMaterial)
	adapter, err := New(testKeysetID, missingHandle)
	if err != nil {
		t.Fatal("create keyset without envelope key failed")
	}
	if _, err := adapter.Decrypt(testBinding(), envelope); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Decrypt() missing key error=%v", err)
	}
}

func TestLoadRequiresProtectedRegularBoundedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keyset.json")
	writeKeyset(t, path, testHandle(t), 0o600)
	if _, err := Load(path, testKeysetID); err != nil {
		t.Fatalf("Load() protected keyset: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, testKeysetID); !errors.Is(err, ErrInvalidKeyset) {
		t.Fatalf("Load() permissive file error=%v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), MaxKeysetBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, testKeysetID); !errors.Is(err, ErrInvalidKeyset) {
		t.Fatalf("Load() oversized file error=%v", err)
	}
	if _, err := Load(dir, testKeysetID); !errors.Is(err, ErrInvalidKeyset) {
		t.Fatalf("Load() directory error=%v", err)
	}
}

func TestNewRejectsUnsupportedAndOversizedKeysets(t *testing.T) {
	for name, template := range map[string]func() *tink_go_proto.KeyTemplate{
		"AES-128":         func() *tink_go_proto.KeyTemplate { return aead.AES128GCMKeyTemplate() },
		"non-TINK prefix": func() *tink_go_proto.KeyTemplate { return aead.AES256GCMNoPrefixKeyTemplate() },
	} {
		t.Run(name, func(t *testing.T) {
			handle, err := keyset.NewHandle(template())
			if err != nil {
				t.Fatal("create unsupported test keyset failed")
			}
			if _, err := New(testKeysetID, handle); !errors.Is(err, ErrInvalidKeyset) {
				t.Fatalf("New() error=%v", err)
			}
		})
	}

	manager := keyset.NewManagerFromHandle(testHandle(t))
	for range maxKeyCount {
		if _, err := manager.Add(aead.AES256GCMKeyTemplate()); err != nil {
			t.Fatal("add test key failed")
		}
	}
	oversized, err := manager.Handle()
	if err != nil {
		t.Fatal("read oversized test keyset failed")
	}
	if _, err := New(testKeysetID, oversized); !errors.Is(err, ErrInvalidKeyset) {
		t.Fatalf("New() oversized keyset error=%v", err)
	}
}

func TestLoadRejectsDuplicateIDsDisabledPrimaryAndMalformedKeysets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keyset.json")
	base := testHandle(t)
	material := insecurecleartextkeyset.KeysetMaterial(base)

	duplicateManager := keyset.NewManagerFromHandle(base)
	if _, err := duplicateManager.Add(aead.AES256GCMKeyTemplate()); err != nil {
		t.Fatal("add duplicate ID test key failed")
	}
	duplicateHandle, err := duplicateManager.Handle()
	if err != nil {
		t.Fatal("read duplicate ID test keyset failed")
	}
	duplicate := proto.Clone(insecurecleartextkeyset.KeysetMaterial(duplicateHandle)).(*tink_go_proto.Keyset)
	duplicate.Key[1].KeyId = duplicate.Key[0].KeyId
	writeRawKeyset(t, path, duplicate)
	if _, err := Load(path, testKeysetID); !errors.Is(err, ErrInvalidKeyset) {
		t.Fatalf("Load() duplicate key ID error=%v", err)
	}

	disabledPrimary := proto.Clone(material).(*tink_go_proto.Keyset)
	disabledPrimary.Key[0].Status = tink_go_proto.KeyStatusType_DISABLED
	writeRawKeyset(t, path, disabledPrimary)
	if _, err := Load(path, testKeysetID); !errors.Is(err, ErrInvalidKeyset) {
		t.Fatalf("Load() disabled primary error=%v", err)
	}

	if err := os.WriteFile(path, []byte("not a keyset"), 0o600); err != nil {
		t.Fatal("write malformed test keyset failed")
	}
	if _, err := Load(path, testKeysetID); !errors.Is(err, ErrInvalidKeyset) {
		t.Fatalf("Load() malformed keyset error=%v", err)
	}
}

func TestAssociatedDataHasStableFieldOrder(t *testing.T) {
	data, err := bindingData(testBinding(), testKeysetID, 42)
	if err != nil {
		t.Fatalf("bindingData() failed: %v", err)
	}
	want := `["almanac.secret",1,"7d1b861f-21b4-49b5-a891-3d3ae72db2e1","datasource","a55332e8-b2b9-4a2b-ae89-141a326bfe70","auth.password",3,"c79c723a-81a2-4e0f-ae09-f2a071b2ef45",42]`
	if string(data) != want {
		t.Fatalf("associated data order is not canonical: matched_expected=%t", string(data) == want)
	}
}

func TestInvalidInputsAndErrorsDoNotExposeSecretsOrMalformedInput(t *testing.T) {
	adapter := testAdapter(t, testHandle(t))
	for _, binding := range []Binding{
		{InstallationID: strings.ToUpper(testBinding().InstallationID), ResourceKind: "datasource", ResourceID: testBinding().ResourceID, Field: "auth.password", Revision: 1},
		{InstallationID: testBinding().InstallationID, ResourceKind: "../datasource", ResourceID: testBinding().ResourceID, Field: "auth.password", Revision: 1},
		{InstallationID: testBinding().InstallationID, ResourceKind: "datasource", ResourceID: testBinding().ResourceID, Field: "auth.password", Revision: 0},
	} {
		if _, err := adapter.Encrypt(binding, []byte("do-not-leak-plaintext")); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("Encrypt() invalid binding error=%v", err)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "bad-keyset"), testKeysetID); err == nil || strings.Contains(err.Error(), "bad-keyset") {
		t.Fatalf("Load() error leaked path: %v", err)
	}
	bad := Envelope{FormatVersion: 1, KeysetID: testKeysetID, KeyID: 1, Ciphertext: []byte("do-not-leak-ciphertext")}
	if _, err := adapter.Decrypt(testBinding(), bad); err == nil || strings.Contains(err.Error(), "do-not-leak-ciphertext") {
		t.Fatalf("Decrypt() error leaked malformed input: %v", err)
	}
}

func testAdapter(t *testing.T, handle *keyset.Handle) *Adapter {
	t.Helper()
	adapter, err := New(testKeysetID, handle)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	return adapter
}

func testHandle(t *testing.T) *keyset.Handle {
	t.Helper()
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	if err != nil {
		t.Fatal("create Tink handle failed")
	}
	return handle
}

func writeKeyset(t *testing.T, path string, handle *keyset.Handle, mode os.FileMode) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		t.Fatal("create test keyset file failed")
	}
	if err := insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(file)); err != nil {
		_ = file.Close()
		t.Fatal("write test keyset file failed")
	}
	if err := file.Close(); err != nil {
		t.Fatal("close test keyset file failed")
	}
}

func writeRawKeyset(t *testing.T, path string, material *tink_go_proto.Keyset) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal("create raw test keyset failed")
	}
	if err := keyset.NewJSONWriter(file).Write(material); err != nil {
		_ = file.Close()
		t.Fatal("write raw test keyset failed")
	}
	if err := file.Close(); err != nil {
		t.Fatal("close raw test keyset failed")
	}
}

func readRawKeyset(t *testing.T, material *tink_go_proto.Keyset) *keyset.Handle {
	t.Helper()
	var encoded bytes.Buffer
	if err := keyset.NewJSONWriter(&encoded).Write(material); err != nil {
		t.Fatal("encode raw test keyset failed")
	}
	handle, err := insecurecleartextkeyset.Read(keyset.NewJSONReader(&encoded))
	if err != nil {
		t.Fatal("read raw test keyset failed")
	}
	return handle
}

func cloneEnvelope(envelope Envelope) Envelope {
	envelope.Ciphertext = bytes.Clone(envelope.Ciphertext)
	return envelope
}
