package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"strings"
)

// DigestAlgorithm selects the hash used by Digest and HMAC.
type DigestAlgorithm string

const (
	SHA1   DigestAlgorithm = "SHA-1"
	SHA256 DigestAlgorithm = "SHA-256"
	SHA512 DigestAlgorithm = "SHA-512"
)

func (a DigestAlgorithm) newHash() (hash.Hash, error) {
	switch a {
	case SHA1:
		return sha1.New(), nil
	case SHA256:
		return sha256.New(), nil
	case SHA512:
		return sha512.New(), nil
	default:
		return nil, NewError("unsupported digest algorithm: "+string(a), "CONFIGURATION_ERROR")
	}
}

// UTF8 returns the UTF-8 bytes of a string.
func UTF8(value string) []byte { return []byte(value) }

// ToBase64 encodes bytes as standard base64.
func ToBase64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// Base64ToBytes decodes base64, ignoring whitespace.
func Base64ToBytes(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(value), ""))
}

// FromBase64 decodes base64 to a UTF-8 string.
func FromBase64(value string) (string, error) {
	b, err := Base64ToBytes(value)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ToHex encodes bytes as lowercase hex.
func ToHex(b []byte) string { return hex.EncodeToString(b) }

// Digest hashes data with the given algorithm.
func Digest(algorithm DigestAlgorithm, data []byte) ([]byte, error) {
	h, err := algorithm.newHash()
	if err != nil {
		return nil, err
	}
	h.Write(data)
	return h.Sum(nil), nil
}

// HMAC signs data with key using the given algorithm.
func HMAC(algorithm DigestAlgorithm, key, data []byte) ([]byte, error) {
	// hmac.New invokes the constructor twice (inner and outer hash), so each
	// call must return a fresh instance; sharing one corrupts the signature.
	constructor := func() hash.Hash {
		h, _ := algorithm.newHash()
		return h
	}
	if _, err := algorithm.newHash(); err != nil {
		return nil, err
	}
	mac := hmac.New(constructor, key)
	mac.Write(data)
	return mac.Sum(nil), nil
}

// AESCBCEncrypt encrypts with AES-CBC and PKCS#7 padding. The key length
// (16, 24 or 32 bytes) selects AES-128/192/256.
func AESCBCEncrypt(key, iv, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, NewError("AES-CBC IV must be 16 bytes", "CONFIGURATION_ERROR")
	}
	padded := pkcs7Pad(data, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

// AESCBCDecrypt reverses AESCBCEncrypt. It fails when the key, IV or padding is wrong.
func AESCBCDecrypt(key, iv, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, NewError("AES-CBC IV must be 16 bytes", "CONFIGURATION_ERROR")
	}
	if len(data) == 0 || len(data)%block.BlockSize() != 0 {
		return nil, NewError("AES-CBC ciphertext is not a multiple of the block size", "CONFIGURATION_ERROR")
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return pkcs7Unpad(out, block.BlockSize())
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	n := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+n)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty plaintext")
	}
	n := int(data[len(data)-1])
	if n == 0 || n > blockSize || n > len(data) {
		return nil, errors.New("invalid PKCS#7 padding")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errors.New("invalid PKCS#7 padding")
		}
	}
	return data[:len(data)-n], nil
}

// RandomHex returns cryptographically random bytes as lowercase hex.
func RandomHex(byteLength int) (string, error) {
	b := make([]byte, byteLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return ToHex(b), nil
}

// SafeEqual compares signatures in constant time. Empty values never match.
func SafeEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
