package auth

import "testing"

func TestArgon2RoundTrip(t *testing.T) {
	hash, err := HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := VerifyPassword("secret123", hash); !ok {
		t.Fatal("verify failed")
	}
	if ok, _ := VerifyPassword("wrong123", hash); ok {
		t.Fatal("wrong password accepted")
	}
}

func TestPasswordRule(t *testing.T) {
	if ValidatePasswordRule("abc12345") != "" {
		t.Error("valid password rejected")
	}
	if ValidatePasswordRule("abc") == "" {
		t.Error("too short accepted")
	}
	if ValidatePasswordRule("onlyletters") == "" {
		t.Error("no digit accepted")
	}
	if ValidatePasswordRule("12345678") == "" {
		t.Error("no letter accepted")
	}
}

func TestUsernameRule(t *testing.T) {
	if ValidateUsername("user_01") != "" {
		t.Error("valid username rejected")
	}
	if ValidateUsername("ab") == "" {
		t.Error("short username accepted")
	}
	if ValidateUsername("bad name!") == "" {
		t.Error("illegal username accepted")
	}
}

func TestLoginGuardLockout(t *testing.T) {
	g := NewLoginGuard()
	for i := 0; i < 5; i++ {
		g.RecordFailure("alice")
	}
	if g.LockedRemaining("alice") <= 0 {
		t.Fatal("account should be locked after 5 fails")
	}
}

func TestLoginGuardIPLimit(t *testing.T) {
	g := NewLoginGuard()
	ok := true
	for i := 0; i < 21; i++ {
		ok = g.AllowIP("1.2.3.4")
	}
	if ok {
		t.Fatal("21st request should be 429")
	}
}
