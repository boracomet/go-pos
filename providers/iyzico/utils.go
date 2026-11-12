package iyzico

import (
	"fmt"
	"strings"
	"time"

	"github.com/boracomet/go-pos/core"
)

// GenerateAuthStringV2 builds the IYZWSv2 Authorization header value. The
// signature is hex(HMAC-SHA256(secretKey, randomString + uri + requestBody)),
// so the URI path and the exact JSON body sent must both be covered.
func GenerateAuthStringV2(apiKey, secretKey, randomString, uri, requestBody string) (string, error) {
	mac, err := core.HMAC(core.SHA256, []byte(secretKey), []byte(randomString+uri+requestBody))
	if err != nil {
		return "", err
	}
	params := strings.Join([]string{
		"apiKey:" + apiKey,
		"randomKey:" + randomString,
		"signature:" + core.ToHex(mac),
	}, "&")
	return "IYZWSv2 " + core.ToBase64([]byte(params)), nil
}

// GenerateAuthStringV1 builds the legacy IYZWS Authorization header value.
func GenerateAuthStringV1(apiKey, secretKey, randomString, pkiString string) (string, error) {
	sum, err := core.Digest(core.SHA1, []byte(apiKey+randomString+secretKey+pkiString))
	if err != nil {
		return "", err
	}
	return "IYZWS " + apiKey + ":" + core.ToBase64(sum), nil
}

// GenerateRandomString returns the x-iyzi-rnd value: a millisecond timestamp
// followed by random characters.
func GenerateRandomString() (string, error) {
	suffix, err := core.RandomHex(8)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d%s", time.Now().UnixMilli(), suffix), nil
}

// CreateHeaders builds the signed headers for one API request. pkiString is
// optional; when set, the legacy Authorization_Fallback header is added too.
func CreateHeaders(apiKey, secretKey, uri, requestBody, pkiString string) (map[string]string, error) {
	randomString, err := GenerateRandomString()
	if err != nil {
		return nil, err
	}
	authV2, err := GenerateAuthStringV2(apiKey, secretKey, randomString, uri, requestBody)
	if err != nil {
		return nil, err
	}

	headers := map[string]string{
		"Accept":                "application/json",
		"Content-Type":          "application/json",
		"Authorization":         authV2,
		"x-iyzi-rnd":            randomString,
		"x-iyzi-client-version": "go-pos-" + core.Version,
	}

	if pkiString != "" {
		authV1, err := GenerateAuthStringV1(apiKey, secretKey, randomString, pkiString)
		if err != nil {
			return nil, err
		}
		headers["Authorization_Fallback"] = authV1
	}

	return headers, nil
}
