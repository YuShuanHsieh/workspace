package natscreds

import "testing"

func TestJWTCallbackReturnsCachedJWT(t *testing.T) {
	p, err := New(validConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.jwt = "cached.jwt.token"

	got, err := p.JWT()
	if err != nil {
		t.Fatalf("JWT: %v", err)
	}
	if got != "cached.jwt.token" {
		t.Fatalf("JWT() = %q, want cached.jwt.token", got)
	}
}

func TestSignCallbackVerifiesWithKeyPair(t *testing.T) {
	p, err := New(validConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	nonce := []byte("server-nonce-bytes")

	sig, err := p.Sign(nonce)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := p.kp.Verify(nonce, sig); err != nil {
		t.Fatalf("signature did not verify against the key pair: %v", err)
	}
}
