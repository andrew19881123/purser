package pki

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

// purkMagic is the four-byte header that identifies an encrypted key file
// produced by encryptKeyPEM.  "PURK" = Purser Key.
const purkMagic = "PURK"

// encryptKeyPEM encrypts a PEM-encoded private key with AES-256-GCM using
// Argon2id for key derivation.  The output format is:
//
//	magic(4) | salt(16) | nonce(12) | ciphertext
//
// If passphrase is empty the input is returned unchanged (backward compatibility
// with unencrypted on-disk keys).
func encryptKeyPEM(keyPEM []byte, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return keyPEM, nil
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("pki: generate salt: %w", err)
	}

	// Argon2id: memory=64 MiB, time=3, threads=4, output=32 bytes.
	dk := argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, 32)

	block, err := aes.NewCipher(dk)
	if err != nil {
		return nil, fmt.Errorf("pki: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pki: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("pki: generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, keyPEM, nil)

	// Layout: magic(4) | salt(16) | nonce(gcm.NonceSize()) | ciphertext
	out := make([]byte, 4+16+len(nonce)+len(ciphertext))
	copy(out[0:4], purkMagic)
	copy(out[4:20], salt)
	copy(out[20:20+len(nonce)], nonce)
	copy(out[20+len(nonce):], ciphertext)
	return out, nil
}

// decryptKeyPEM decrypts a key produced by encryptKeyPEM.
//
// If the input does not start with the PURK magic the data is assumed to be a
// legacy unencrypted PEM key and is returned as-is (backward compatibility).
// If the input IS encrypted but passphrase is empty, an error is returned.
func decryptKeyPEM(data []byte, passphrase string) ([]byte, error) {
	if len(data) < 4 || string(data[:4]) != purkMagic {
		// Legacy unencrypted key — pass through unchanged.
		return data, nil
	}
	if passphrase == "" {
		return nil, fmt.Errorf("pki: key is encrypted but PURSER_PKI_KEY_PASSPHRASE is not set")
	}
	if len(data) < 20 {
		return nil, fmt.Errorf("pki: encrypted key too short")
	}
	salt := data[4:20]
	rest := data[20:]

	dk := argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, 32)

	block, err := aes.NewCipher(dk)
	if err != nil {
		return nil, fmt.Errorf("pki: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pki: gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(rest) < nonceSize {
		return nil, fmt.Errorf("pki: invalid encrypted key format")
	}
	nonce := rest[:nonceSize]
	ciphertext := rest[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("pki: decrypt key: %w (wrong passphrase?)", err)
	}
	return plaintext, nil
}
