package sign

import (
	"testing"
	"time"
)

func TestSignDocumentRoundTrip(t *testing.T) {
	s := New("secret-key", 24*time.Hour)
	tok, exp := s.SignDocument("dc_1")
	if tok == "" {
		t.Fatal("empty token")
	}
	if exp.IsZero() {
		t.Fatal("empty exp")
	}
	p, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.Kind != KindDocument || p.ID != "dc_1" {
		t.Fatalf("bad parsed: %+v", p)
	}
}

func TestVerifyTampered(t *testing.T) {
	s := New("k", time.Hour)
	tok, _ := s.SignJob("jb_1")
	tampered := tok[:len(tok)-2] + "xx"
	if _, err := s.Verify(tampered); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestVerifyExpired(t *testing.T) {
	s := New("k", -time.Hour) // 已过期
	tok, _ := s.SignDocument("dc_1")
	if _, err := s.Verify(tok); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestWrongSecret(t *testing.T) {
	s1 := New("k1", time.Hour)
	tok, _ := s1.SignDocument("dc_1")
	s2 := New("k2", time.Hour)
	if _, err := s2.Verify(tok); err == nil {
		t.Fatal("cross-secret token accepted")
	}
}
