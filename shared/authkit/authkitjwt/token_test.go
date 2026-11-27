package authkitjwt

import (
	"testing"
	"time"

	"github.com/dreamoutbox/authkit/shared"
)

func TestGenerateAndParseLoginToken(t *testing.T) {
	secret := "login-token-secret"
	tok, exp, jti, err := GenerateLoginToken(42, "player@example.com", shared.UserRoleUser, secret, 60*time.Second)
	if err != nil {
		t.Fatalf("GenerateLoginToken failed: %v", err)
	}
	if tok == "" || jti == "" || exp.Before(time.Now()) {
		t.Fatalf("invalid login token output: tok=%q jti=%q exp=%v", tok, jti, exp)
	}

	claims, err := ParseAndValidateToken(tok, secret, TokenTypeLogin)
	if err != nil {
		t.Fatalf("ParseAndValidateToken failed: %v", err)
	}
	if claims.UserID != 42 || claims.ID != jti || claims.Email != "player@example.com" {
		t.Errorf("unexpected claims: %+v", claims)
	}

	if _, err := ParseAndValidateToken(tok, secret, TokenTypeAccess); err == nil {
		t.Error("expected login token to fail access token type validation")
	}
}

func TestAccessTokenTTLFromEnv(t *testing.T) {
	t.Run("default 15m when unset", func(t *testing.T) {
		t.Setenv(AccessTokenTTLEnvVar, "")
		ttl, err := AccessTokenTTLFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ttl != DefaultAccessTokenTTL {
			t.Fatalf("expected %s, got %s", DefaultAccessTokenTTL, ttl)
		}
	})

	t.Run("custom duration", func(t *testing.T) {
		t.Setenv(AccessTokenTTLEnvVar, "45m")
		ttl, err := AccessTokenTTLFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ttl != 45*time.Minute {
			t.Fatalf("expected 45m, got %s", ttl)
		}

		before := time.Now()
		pair, err := GenerateTokenPair(1, "ttl@example.com", shared.UserRoleUser, "secret")
		if err != nil {
			t.Fatalf("GenerateTokenPair failed: %v", err)
		}
		delta := pair.ExpiresAt.Sub(before)
		if delta < 44*time.Minute || delta > 46*time.Minute {
			t.Fatalf("expected access expiry ~45m from now, got %s", delta)
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		t.Setenv(AccessTokenTTLEnvVar, "not-a-duration")
		if _, err := AccessTokenTTLFromEnv(); err == nil {
			t.Fatal("expected error for invalid JWT_ACCESS_TTL")
		}
	})

	t.Run("non-positive duration", func(t *testing.T) {
		t.Setenv(AccessTokenTTLEnvVar, "-10m")
		if _, err := AccessTokenTTLFromEnv(); err == nil {
			t.Fatal("expected error for negative JWT_ACCESS_TTL")
		}
		if _, err := GenerateTokenPairWithTTL(1, "a@b.com", shared.UserRoleUser, "secret", 0); err == nil {
			t.Fatal("expected error for zero accessTTL")
		}
	})
}
