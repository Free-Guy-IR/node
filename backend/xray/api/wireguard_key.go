package api

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

func WireguardKeyToHex(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("empty wireguard key")
	}

	if len(key) == 64 {
		if _, err := hex.DecodeString(key); err == nil {
			return strings.ToLower(key), nil
		}
	}

	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return "", fmt.Errorf("invalid wireguard key encoding: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("invalid wireguard key length: %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}
