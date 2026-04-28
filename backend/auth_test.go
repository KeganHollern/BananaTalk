package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/api/idtoken"
)

// stubValidator swaps tokenValidate for the duration of the test.
func stubValidator(t *testing.T, fn func(ctx context.Context, token, audience string) (*idtoken.Payload, error)) {
	t.Helper()
	orig := tokenValidate
	tokenValidate = fn
	t.Cleanup(func() { tokenValidate = orig })
}

func TestVerifyToken_Valid(t *testing.T) {
	stubValidator(t, func(_ context.Context, _, _ string) (*idtoken.Payload, error) {
		return &idtoken.Payload{
			Subject: "user-123",
			Expires: time.Now().Add(time.Hour).Unix(),
		}, nil
	})

	sub, code, err := verifyToken(context.Background(), "eyJhbGciOi...real-looking-token")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if code != "" {
		t.Fatalf("code: want empty, got %q", code)
	}
	if sub != "user-123" {
		t.Fatalf("sub: want user-123, got %q", sub)
	}
}

func TestVerifyToken_MissingToken(t *testing.T) {
	// tokenValidate must NOT be called when the token string is empty.
	stubValidator(t, func(context.Context, string, string) (*idtoken.Payload, error) {
		t.Fatalf("validator should not be invoked for empty token")
		return nil, nil
	})

	_, code, err := verifyToken(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != tokenErrMissing {
		t.Fatalf("code: want %q, got %q", tokenErrMissing, code)
	}
}

func TestVerifyToken_Malformed(t *testing.T) {
	wantErr := errors.New("bad jwt")
	stubValidator(t, func(context.Context, string, string) (*idtoken.Payload, error) {
		return nil, wantErr
	})

	_, code, err := verifyToken(context.Background(), "garbage")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err: want %v, got %v", wantErr, err)
	}
	if code != tokenErrInvalid {
		t.Fatalf("code: want %q, got %q", tokenErrInvalid, code)
	}
}

func TestVerifyToken_Expired(t *testing.T) {
	stubValidator(t, func(context.Context, string, string) (*idtoken.Payload, error) {
		return &idtoken.Payload{
			Subject: "user-123",
			Expires: time.Now().Add(-time.Second).Unix(),
		}, nil
	})

	_, code, err := verifyToken(context.Background(), "stale-token")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != tokenErrExpired {
		t.Fatalf("code: want %q, got %q", tokenErrExpired, code)
	}
}

func TestVerifyToken_MissingSubject(t *testing.T) {
	stubValidator(t, func(context.Context, string, string) (*idtoken.Payload, error) {
		return &idtoken.Payload{
			Subject: "",
			Expires: time.Now().Add(time.Hour).Unix(),
		}, nil
	})

	_, code, err := verifyToken(context.Background(), "no-sub-token")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if code != tokenErrNoSub {
		t.Fatalf("code: want %q, got %q", tokenErrNoSub, code)
	}
}

func TestBearerToken_PrefersQueryParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws?token=q-token", nil)
	req.Header.Set("Authorization", "Bearer h-token")
	if got := bearerToken(req); got != "q-token" {
		t.Fatalf("want q-token, got %q", got)
	}
}

func TestBearerToken_FallsBackToHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer h-token")
	if got := bearerToken(req); got != "h-token" {
		t.Fatalf("want h-token, got %q", got)
	}
}

func TestBearerToken_NoCredential(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if got := bearerToken(req); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestBearerToken_IgnoresNonBearerScheme(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Basic xyz==")
	if got := bearerToken(req); got != "" {
		t.Fatalf("want empty for non-Bearer scheme, got %q", got)
	}
}

func TestTokenErrMessage_Mapping(t *testing.T) {
	for _, code := range []string{tokenErrMissing, tokenErrInvalid, tokenErrExpired, tokenErrNoSub} {
		if tokenErrMessage(code) == "" {
			t.Fatalf("code %q produced empty message", code)
		}
	}
	if got := tokenErrMessage("unknown"); got != "unauthorized" {
		t.Fatalf("default: want unauthorized, got %q", got)
	}
}
