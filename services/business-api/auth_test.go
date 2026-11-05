package main

import (
	"strings"
	"testing"
)

// Tes unit tanpa database: hashing token, validasi email, hashing password.

func TestHashTokenDeterministic(t *testing.T) {
	a := hashToken("abc123")
	b := hashToken("abc123")
	if a == "" || a != b {
		t.Fatalf("hashToken harus deterministik dan tidak kosong, got %q vs %q", a, b)
	}
	if a == "abc123" {
		t.Fatalf("hashToken tidak boleh mengembalikan token mentah")
	}
	if hashToken("abc123") == hashToken("abc124") {
		t.Fatalf("token berbeda harus menghasilkan hash berbeda")
	}
}

func TestValidateEmail(t *testing.T) {
	valid := []string{"a@b.co", "nama@usaha.id"}
	for _, e := range valid {
		if err := validateEmail(e); err != nil {
			t.Errorf("email %q seharusnya valid, got %v", e, err)
		}
	}
	invalid := []string{"", "tanpa-at", "a@b", "a b@c.id", "@b.co", "a@"}
	for _, e := range invalid {
		if err := validateEmail(e); err == nil {
			t.Errorf("email %q seharusnya ditolak", e)
		}
	}
}

func TestPasswordHashVerify(t *testing.T) {
	hash, err := hashPassword("rahasia-yang-cukup-panjang")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if hash == "" || strings.Contains(hash, "rahasia") {
		t.Fatalf("hash tidak boleh kosong atau mengandung password mentah")
	}
	if err := verifyPassword(hash, "rahasia-yang-cukup-panjang"); err != nil {
		t.Fatalf("password benar harus lolos: %v", err)
	}
	if err := verifyPassword(hash, "password-salah"); err == nil {
		t.Fatalf("password salah harus ditolak")
	}
	if err := hashPasswordCheck("pendek"); err == nil {
		t.Fatalf("password di bawah 8 karakter harus ditolak")
	}
}
