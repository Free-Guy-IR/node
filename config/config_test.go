package config

import (
	"testing"

	"github.com/google/uuid"
)

func TestLoadFailsWithoutAPIKey(t *testing.T) {
	t.Setenv("API_KEY", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load must fail when API_KEY is empty")
	}
}

func TestLoadFailsWithMalformedAPIKey(t *testing.T) {
	t.Setenv("API_KEY", "not-a-uuid")

	if _, err := Load(); err == nil {
		t.Fatal("Load must fail when API_KEY is not a valid UUID")
	}
}

func TestLoadFailsWithNilUUIDAPIKey(t *testing.T) {
	t.Setenv("API_KEY", uuid.Nil.String())

	if _, err := Load(); err == nil {
		t.Fatal("Load must fail when API_KEY is the nil UUID")
	}
}

func TestLoadSucceedsWithValidAPIKey(t *testing.T) {
	key := uuid.New()
	t.Setenv("API_KEY", key.String())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ApiKey != key {
		t.Fatalf("expected api key %s, got %s", key, cfg.ApiKey)
	}
}

func TestNewTestConfigAlwaysYieldsTheGivenKey(t *testing.T) {
	key := uuid.New()
	t.Setenv("API_KEY", "garbage")

	cfg := NewTestConfig(t.TempDir(), key)
	if cfg == nil {
		t.Fatal("NewTestConfig must not return nil")
	}
	if cfg.ApiKey != key {
		t.Fatalf("expected api key %s, got %s", key, cfg.ApiKey)
	}
}
