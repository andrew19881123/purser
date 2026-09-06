package pki

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncryptDecryptKey_RoundTrip(t *testing.T) {
	plainPEM := []byte("-----BEGIN EC PRIVATE KEY-----\nABCDEFGH\n-----END EC PRIVATE KEY-----\n")
	encrypted, err := encryptKeyPEM(plainPEM, "test-passphrase")
	if err != nil {
		t.Fatalf("encryptKeyPEM: %v", err)
	}
	if bytes.Equal(plainPEM, encrypted) {
		t.Fatal("encrypted output must differ from plaintext")
	}
	if !strings.HasPrefix(string(encrypted[:4]), purkMagic) {
		t.Fatalf("expected PURK magic prefix, got %q", encrypted[:4])
	}

	decrypted, err := decryptKeyPEM(encrypted, "test-passphrase")
	if err != nil {
		t.Fatalf("decryptKeyPEM: %v", err)
	}
	if !bytes.Equal(plainPEM, decrypted) {
		t.Errorf("round-trip mismatch: got %q, want %q", decrypted, plainPEM)
	}
}

func TestDecryptKey_WrongPassphrase_Fails(t *testing.T) {
	plainPEM := []byte("-----BEGIN EC PRIVATE KEY-----\ntest\n-----END EC PRIVATE KEY-----\n")
	encrypted, err := encryptKeyPEM(plainPEM, "correct-pass")
	if err != nil {
		t.Fatalf("encryptKeyPEM: %v", err)
	}
	_, err = decryptKeyPEM(encrypted, "wrong-pass")
	if err == nil {
		t.Error("expected error with wrong passphrase, got nil")
	}
}

func TestDecryptKey_UnencryptedKey_PassesThrough(t *testing.T) {
	legacyPEM := []byte("-----BEGIN EC PRIVATE KEY-----\nlegacy-key-data\n-----END EC PRIVATE KEY-----\n")
	result, err := decryptKeyPEM(legacyPEM, "any-passphrase")
	if err != nil {
		t.Fatalf("decryptKeyPEM on unencrypted key: %v", err)
	}
	if !bytes.Equal(legacyPEM, result) {
		t.Errorf("passthrough mismatch: got %q, want %q", result, legacyPEM)
	}
}

func TestDecryptKey_EncryptedWithoutPassphrase_Fails(t *testing.T) {
	plainPEM := []byte("-----BEGIN EC PRIVATE KEY-----\ntest\n-----END EC PRIVATE KEY-----\n")
	encrypted, err := encryptKeyPEM(plainPEM, "some-pass")
	if err != nil {
		t.Fatalf("encryptKeyPEM: %v", err)
	}
	_, err = decryptKeyPEM(encrypted, "") // no passphrase
	if err == nil {
		t.Error("expected error when passphrase is missing for encrypted key, got nil")
	}
}

func TestEncryptKeyPEM_NoPassphrase_Noop(t *testing.T) {
	plainPEM := []byte("-----BEGIN EC PRIVATE KEY-----\ntest\n-----END EC PRIVATE KEY-----\n")
	result, err := encryptKeyPEM(plainPEM, "")
	if err != nil {
		t.Fatalf("encryptKeyPEM with empty passphrase: %v", err)
	}
	if !bytes.Equal(plainPEM, result) {
		t.Error("empty passphrase must return input unchanged")
	}
}

func TestEncryptKeyPEM_Nondeterministic(t *testing.T) {
	// Two encryptions of the same key with the same passphrase must produce
	// different ciphertext (due to random salt + nonce).
	plainPEM := []byte("-----BEGIN EC PRIVATE KEY-----\ntest\n-----END EC PRIVATE KEY-----\n")
	enc1, _ := encryptKeyPEM(plainPEM, "pass")
	enc2, _ := encryptKeyPEM(plainPEM, "pass")
	if bytes.Equal(enc1, enc2) {
		t.Error("two encryptions of the same key must not produce identical output")
	}
}
