package auth

import (
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	password := "SecretPassword123!"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if len(hash) == 0 {
		t.Fatalf("expected non-empty hash")
	}

	// Verify valid password
	valid, err := VerifyPassword(password, hash)
	if err != nil {
		t.Fatalf("VerifyPassword failed with error: %v", err)
	}
	if !valid {
		t.Errorf("expected password to verify successfully")
	}

	// Verify invalid password
	valid, err = VerifyPassword("WrongPassword!", hash)
	if err != nil {
		t.Fatalf("VerifyPassword failed with error on wrong password: %v", err)
	}
	if valid {
		t.Errorf("expected wrong password to fail verification")
	}

	// Verify invalid hash format
	_, err = VerifyPassword(password, "invalid-hash-string")
	if err == nil {
		t.Errorf("expected error for invalid hash format")
	}
}

func TestHashAPIKey(t *testing.T) {
	key1 := "sk-live-1234567890abcdef"
	hash1 := HashAPIKey(key1)
	hash2 := HashAPIKey(key1)

	if hash1 != hash2 {
		t.Errorf("expected identical hashes for same key, got %s and %s", hash1, hash2)
	}

	key2 := "sk-live-0987654321fedcba"
	hash3 := HashAPIKey(key2)
	if hash1 == hash3 {
		t.Errorf("expected different hashes for different keys")
	}
}

func TestGenerateRandomSecret(t *testing.T) {
	s1, err := GenerateRandomSecret(16)
	if err != nil {
		t.Fatalf("GenerateRandomSecret failed: %v", err)
	}
	if len(s1) != 32 { // 16 bytes = 32 hex chars
		t.Errorf("expected length 32 hex string, got %d", len(s1))
	}

	s2, err := GenerateRandomSecret(16)
	if err != nil {
		t.Fatalf("GenerateRandomSecret failed: %v", err)
	}
	if s1 == s2 {
		t.Errorf("expected randomly generated secrets to be unique")
	}
}

