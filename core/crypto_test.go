package core

import (
	"bytes"
	"crypto/aes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// RFC 4231 test case 2.
const (
	cryptoTestVectorKey  = "Jefe"
	cryptoTestVectorData = "what do ya want for nothing?"
)

func TestHMACMatchesKnownVector(t *testing.T) {
	cases := []struct {
		algorithm DigestAlgorithm
		want      string
	}{
		{SHA256, "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"},
		{SHA512, "164b7a7bfcf819e2e395fbe73b56e0a387bd64222e831fd610270cd7ea2505549758bf75c05a994a6d034f65f8f0e6fdcaeab1a34d4a6b4b636e070a38bce737"},
		{SHA1, "effcdf6ae5eb2fa2d27416d5f184df9c259a7c79"},
	}
	for _, tc := range cases {
		t.Run(string(tc.algorithm), func(t *testing.T) {
			got, err := HMAC(tc.algorithm, UTF8(cryptoTestVectorKey), UTF8(cryptoTestVectorData))
			if err != nil {
				t.Fatalf("HMAC: %v", err)
			}
			if ToHex(got) != tc.want {
				t.Errorf("HMAC = %s, want %s", ToHex(got), tc.want)
			}
		})
	}
}

func TestHMACIsStableAcrossCalls(t *testing.T) {
	// The hash constructor is invoked twice per HMAC; a shared instance would
	// make the second signature differ from the first.
	key, data := UTF8("key"), UTF8("The quick brown fox jumps over the lazy dog")
	first, err := HMAC(SHA256, key, data)
	if err != nil {
		t.Fatalf("HMAC: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := HMAC(SHA256, key, data)
		if err != nil {
			t.Fatalf("HMAC: %v", err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("call %d = %x, want %x", i, again, first)
		}
	}
}

func TestHMACEmptyKeyAndData(t *testing.T) {
	// RFC 4231 test case 1 with an empty data block.
	got, err := HMAC(SHA256, bytes.Repeat([]byte{0x0b}, 20), nil)
	if err != nil {
		t.Fatalf("HMAC: %v", err)
	}
	want := "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"
	if ToHex(got) != want {
		t.Errorf("HMAC = %s, want %s", ToHex(got), want)
	}
}

func TestHMACRejectsUnsupportedAlgorithm(t *testing.T) {
	if _, err := HMAC(DigestAlgorithm("MD5"), UTF8("k"), UTF8("d")); err == nil {
		t.Fatal("HMAC with an unsupported algorithm: want an error")
	}
}

func TestDigestMatchesKnownVectors(t *testing.T) {
	cases := []struct {
		algorithm DigestAlgorithm
		data      string
		want      string
	}{
		{SHA1, "abc", "a9993e364706816aba3e25717850c26c9cd0d89d"},
		{SHA256, "abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{SHA512, "abc", "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"},
		{SHA256, "", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
	}
	for _, tc := range cases {
		t.Run(string(tc.algorithm)+"/"+tc.data, func(t *testing.T) {
			got, err := Digest(tc.algorithm, UTF8(tc.data))
			if err != nil {
				t.Fatalf("Digest: %v", err)
			}
			if ToHex(got) != tc.want {
				t.Errorf("Digest = %s, want %s", ToHex(got), tc.want)
			}
		})
	}
}

func TestDigestRejectsUnsupportedAlgorithm(t *testing.T) {
	_, err := Digest(DigestAlgorithm("SHA-384"), UTF8("abc"))
	if err == nil {
		t.Fatal("Digest with an unsupported algorithm: want an error")
	}
	var base *BaseError
	if !errors.As(err, &base) || base.Code != "CONFIGURATION_ERROR" {
		t.Errorf("error = %v, want a CONFIGURATION_ERROR", err)
	}
}

func TestAESCBCRoundTrip(t *testing.T) {
	iv := bytes.Repeat([]byte{0x07}, aes.BlockSize)
	lengths := []int{0, 1, 15, 16, 17, 31, 32, 33, 100}
	for _, keyLength := range []int{16, 24, 32} {
		key := cryptoTestKey(keyLength)
		for _, length := range lengths {
			data := bytes.Repeat([]byte{'a'}, length)
			ciphertext, err := AESCBCEncrypt(key, iv, data)
			if err != nil {
				t.Fatalf("key %d, data %d: encrypt: %v", keyLength, length, err)
			}
			if len(ciphertext)%aes.BlockSize != 0 {
				t.Errorf("key %d, data %d: ciphertext length %d is not a block multiple", keyLength, length, len(ciphertext))
			}
			wantLength := (length/aes.BlockSize + 1) * aes.BlockSize
			if len(ciphertext) != wantLength {
				t.Errorf("key %d, data %d: ciphertext length = %d, want %d", keyLength, length, len(ciphertext), wantLength)
			}
			plaintext, err := AESCBCDecrypt(key, iv, ciphertext)
			if err != nil {
				t.Fatalf("key %d, data %d: decrypt: %v", keyLength, length, err)
			}
			if !bytes.Equal(plaintext, data) {
				t.Errorf("key %d, data %d: round trip = %q, want %q", keyLength, length, plaintext, data)
			}
		}
	}
}

func TestAESCBCRejectsInvalidKeyLength(t *testing.T) {
	iv := bytes.Repeat([]byte{0x01}, aes.BlockSize)
	for _, keyLength := range []int{0, 8, 15, 17, 31, 33, 64} {
		key := cryptoTestKey(keyLength)
		if _, err := AESCBCEncrypt(key, iv, UTF8("data")); err == nil {
			t.Errorf("encrypt with a %d-byte key: want an error", keyLength)
		}
		if _, err := AESCBCDecrypt(key, iv, bytes.Repeat([]byte{0}, aes.BlockSize)); err == nil {
			t.Errorf("decrypt with a %d-byte key: want an error", keyLength)
		}
	}
}

func TestAESCBCRejectsInvalidIV(t *testing.T) {
	key := cryptoTestKey(32)
	for _, ivLength := range []int{0, 8, 15, 17, 32} {
		iv := bytes.Repeat([]byte{0x02}, ivLength)
		if _, err := AESCBCEncrypt(key, iv, UTF8("data")); err == nil {
			t.Errorf("encrypt with a %d-byte IV: want an error", ivLength)
		}
		if _, err := AESCBCDecrypt(key, iv, bytes.Repeat([]byte{0}, aes.BlockSize)); err == nil {
			t.Errorf("decrypt with a %d-byte IV: want an error", ivLength)
		}
	}
}

func TestAESCBCDecryptRejectsMalformedCiphertext(t *testing.T) {
	key := cryptoTestKey(16)
	iv := bytes.Repeat([]byte{0x03}, aes.BlockSize)
	cases := [][]byte{nil, {}, UTF8("short"), bytes.Repeat([]byte{0}, 17), bytes.Repeat([]byte{0}, 31)}
	for _, data := range cases {
		if _, err := AESCBCDecrypt(key, iv, data); err == nil {
			t.Errorf("decrypt of %d bytes: want an error", len(data))
		}
	}
}

func TestAESCBCDecryptWithWrongKeyFails(t *testing.T) {
	key, wrong := cryptoTestKey(16), cryptoTestKey(16)
	wrong[0] ^= 0xFF
	iv := bytes.Repeat([]byte{0x04}, aes.BlockSize)
	plaintext := UTF8("card data that must not leak")

	ciphertext, err := AESCBCEncrypt(key, iv, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := AESCBCDecrypt(wrong, iv, ciphertext)
	if err == nil && bytes.Equal(got, plaintext) {
		t.Fatal("decryption with the wrong key returned the plaintext")
	}
}

func TestAESCBCDecryptWithWrongIVFails(t *testing.T) {
	key := cryptoTestKey(24)
	iv := bytes.Repeat([]byte{0x05}, aes.BlockSize)
	other := bytes.Repeat([]byte{0x06}, aes.BlockSize)
	plaintext := UTF8("0123456789abcdef") // exactly one block, so the IV covers it all

	ciphertext, err := AESCBCEncrypt(key, iv, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := AESCBCDecrypt(key, other, ciphertext)
	if err == nil && bytes.Equal(got, plaintext) {
		t.Fatal("decryption with the wrong IV returned the plaintext")
	}
}

func TestAESCBCDecryptRejectsInvalidPadding(t *testing.T) {
	key := cryptoTestKey(16)
	iv := bytes.Repeat([]byte{0x08}, aes.BlockSize)
	cases := map[string][]byte{
		"zero pad byte":              append(bytes.Repeat([]byte{'x'}, 15), 0x00),
		"pad beyond block":           append(bytes.Repeat([]byte{'x'}, 15), 0x11),
		"inconsistent pad":           append(bytes.Repeat([]byte{'x'}, 14), 0x02, 0x03),
		"pad of 16 on a short block": {0x10},
	}
	for name, plain := range cases {
		t.Run(name, func(t *testing.T) {
			block := plain
			if len(block) != aes.BlockSize {
				// A one-byte plaintext cannot be a CBC ciphertext; pad it out to a
				// full block whose last byte is still the invalid pad length.
				block = append(bytes.Repeat([]byte{'x'}, 15), plain[0])
			}
			ciphertext := cryptoTestPlainBlock(t, key, iv, block)
			if _, err := AESCBCDecrypt(key, iv, ciphertext); err == nil {
				t.Error("want a padding error")
			}
		})
	}
}

func TestPKCS7Pad(t *testing.T) {
	cases := []struct {
		data      string
		blockSize int
		want      []byte
	}{
		{"", 16, bytes.Repeat([]byte{16}, 16)},
		{"a", 16, append(UTF8("a"), bytes.Repeat([]byte{15}, 15)...)},
		{"0123456789abcde", 16, append(UTF8("0123456789abcde"), 0x01)},
		{"0123456789abcdef", 16, append(UTF8("0123456789abcdef"), bytes.Repeat([]byte{16}, 16)...)},
		{"ab", 4, []byte{'a', 'b', 2, 2}},
	}
	for _, tc := range cases {
		got := pkcs7Pad(UTF8(tc.data), tc.blockSize)
		if !bytes.Equal(got, tc.want) {
			t.Errorf("pkcs7Pad(%q, %d) = %v, want %v", tc.data, tc.blockSize, got, tc.want)
		}
		if len(got)%tc.blockSize != 0 {
			t.Errorf("pkcs7Pad(%q, %d) length %d is not a block multiple", tc.data, tc.blockSize, len(got))
		}
		back, err := pkcs7Unpad(got, tc.blockSize)
		if err != nil {
			t.Fatalf("pkcs7Unpad(%q): %v", tc.data, err)
		}
		if string(back) != tc.data {
			t.Errorf("round trip = %q, want %q", back, tc.data)
		}
	}
}

func TestPKCS7UnpadRejectsInvalidPadding(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"zero":             {0x00},
		"too long":         {0x11},
		"longer than data": {'a', 0x05},
		"inconsistent":     {'a', 0x02, 0x03},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := pkcs7Unpad(data, 16); err == nil {
				t.Errorf("pkcs7Unpad(%v): want an error", data)
			}
		})
	}
}

func TestSafeEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "abc123", "abc123", true},
		{"equal long", strings.Repeat("f", 128), strings.Repeat("f", 128), true},
		{"different", "abc123", "abc124", false},
		{"longer b", "abc", "abcd", false},
		{"longer a", "abcd", "abc", false},
		{"both empty", "", "", false},
		{"empty a", "", "abc", false},
		{"empty b", "abc", "", false},
		{"case sensitive", "ABC", "abc", false},
		{"whitespace matters", "abc", "abc ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("SafeEqual(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := SafeEqual(tc.b, tc.a); got != tc.want {
				t.Errorf("SafeEqual(%q, %q) = %v, want %v (symmetric)", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

func TestBase64Helpers(t *testing.T) {
	if got := ToBase64(UTF8("hello")); got != "aGVsbG8=" {
		t.Errorf("ToBase64 = %q, want aGVsbG8=", got)
	}
	if got := ToBase64(nil); got != "" {
		t.Errorf("ToBase64(nil) = %q, want an empty string", got)
	}
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "aGVsbG8=", "hello"},
		{"leading and trailing space", "  aGVsbG8=  ", "hello"},
		{"newlines", "aGVs\nbG8=\n", "hello"},
		{"tabs", "aGVs\tbG8=", "hello"},
		{"empty", "", ""},
		{"only whitespace", " \n\t ", ""},
		{"utf8", ToBase64(UTF8("Ödeme tamamlandı")), "Ödeme tamamlandı"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Base64ToBytes(tc.input)
			if err != nil {
				t.Fatalf("Base64ToBytes(%q): %v", tc.input, err)
			}
			if string(got) != tc.want {
				t.Errorf("Base64ToBytes(%q) = %q, want %q", tc.input, got, tc.want)
			}
			text, err := FromBase64(tc.input)
			if err != nil {
				t.Fatalf("FromBase64(%q): %v", tc.input, err)
			}
			if text != tc.want {
				t.Errorf("FromBase64(%q) = %q, want %q", tc.input, text, tc.want)
			}
		})
	}
}

func TestBase64HelpersRejectInvalidInput(t *testing.T) {
	for _, input := range []string{"!!!", "a", "aGVsbG8"} {
		if _, err := Base64ToBytes(input); err == nil {
			t.Errorf("Base64ToBytes(%q): want an error", input)
		}
		if _, err := FromBase64(input); err == nil {
			t.Errorf("FromBase64(%q): want an error", input)
		}
	}
}

func TestToHexAndUTF8(t *testing.T) {
	if got := ToHex([]byte{0x00, 0x0f, 0xa5, 0xff}); got != "000fa5ff" {
		t.Errorf("ToHex = %q, want 000fa5ff", got)
	}
	if got := ToHex(nil); got != "" {
		t.Errorf("ToHex(nil) = %q, want an empty string", got)
	}
	if got := UTF8("Ödeme"); string(got) != "Ödeme" || len(got) != 6 {
		t.Errorf("UTF8 = %v, want the 6 UTF-8 bytes of the string", got)
	}
}

func TestRandomHex(t *testing.T) {
	for _, length := range []int{0, 1, 4, 16, 32} {
		got, err := RandomHex(length)
		if err != nil {
			t.Fatalf("RandomHex(%d): %v", length, err)
		}
		if len(got) != length*2 {
			t.Errorf("RandomHex(%d) length = %d, want %d", length, len(got), length*2)
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Errorf("RandomHex(%d) = %q is not hex: %v", length, got, err)
		}
		if got != strings.ToLower(got) {
			t.Errorf("RandomHex(%d) = %q, want lowercase hex", length, got)
		}
	}

	seen := make(map[string]bool, 256)
	for i := 0; i < 256; i++ {
		got, err := RandomHex(16)
		if err != nil {
			t.Fatalf("RandomHex: %v", err)
		}
		if seen[got] {
			t.Fatalf("RandomHex returned %q twice", got)
		}
		seen[got] = true
	}
}

// cryptoTestKey returns a deterministic key of the given length.
func cryptoTestKey(length int) []byte {
	key := make([]byte, length)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

// cryptoTestPlainBlock builds the single ciphertext block that CBC-decrypts to
// plain under key and iv, so padding failures can be asserted deterministically.
func cryptoTestPlainBlock(t *testing.T, key, iv, plain []byte) []byte {
	t.Helper()
	if len(plain) != aes.BlockSize {
		t.Fatalf("plain block must be %d bytes, got %d", aes.BlockSize, len(plain))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	xored := make([]byte, aes.BlockSize)
	for i := range plain {
		xored[i] = plain[i] ^ iv[i]
	}
	out := make([]byte, aes.BlockSize)
	block.Encrypt(out, xored)
	return out
}
