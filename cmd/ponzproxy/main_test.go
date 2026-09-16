package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ponzproxy/ponzproxy/internal/api"
	"github.com/ponzproxy/ponzproxy/internal/domain"
	"github.com/ponzproxy/ponzproxy/internal/platform/config"
	"github.com/ponzproxy/ponzproxy/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// newDataDir returns a config pointing at an empty data directory holding one
// admin account, and the store so a test can read back what was written.
func newDataDir(t *testing.T) (*config.Config, *store.Store) {
	t.Helper()
	cfg := &config.Config{DataDir: t.TempDir()}

	db, err := store.Open(context.Background(), cfg.DBPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// bcrypt.MinCost keeps the fixture cheap; the reset itself still runs at
	// the real cost, which is the part worth measuring.
	if _, err := api.EnsureBootstrapUser(context.Background(), db.Users(),
		"the original password", bcrypt.MinCost); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return cfg, db
}

// captureStdout runs fn with os.Stdout replaced, and returns what it wrote.
// The password is the command's machine-readable output, so what lands on
// stdout is part of the contract rather than an implementation detail.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.Bytes()
	}()

	fn()

	os.Stdout = saved
	w.Close()
	out := <-done
	r.Close()
	return string(out)
}

func TestResetPasswordPrintsAWorkingPassword(t *testing.T) {
	cfg, db := newDataDir(t)
	ctx := context.Background()

	before, err := db.Users().GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("read the account: %v", err)
	}

	var resetErr error
	out := captureStdout(t, func() {
		resetErr = resetPassword(ctx, cfg, "admin")
	})
	if resetErr != nil {
		t.Fatalf("resetPassword: %v", resetErr)
	}

	// Exactly one line, so `--reset-password admin | pbcopy` copies a password
	// and not a banner.
	password := strings.TrimSuffix(out, "\n")
	if strings.ContainsAny(password, "\n ") || password == "" {
		t.Fatalf("stdout should be the bare password on one line, got %q", out)
	}

	after, err := db.Users().GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("re-read the account: %v", err)
	}
	if after.PasswordHash == before.PasswordHash {
		t.Error("the stored hash did not change")
	}
	if err := bcrypt.CompareHashAndPassword(
		[]byte(after.PasswordHash), []byte(password)); err != nil {
		t.Errorf("the printed password does not match the stored hash: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword(
		[]byte(after.PasswordHash), []byte("the original password")); err == nil {
		t.Error("the old password still works")
	}
}

func TestResetPasswordIsCaseInsensitive(t *testing.T) {
	cfg, db := newDataDir(t)
	ctx := context.Background()

	var resetErr error
	out := captureStdout(t, func() {
		resetErr = resetPassword(ctx, cfg, "  ADMIN ")
	})
	if resetErr != nil {
		t.Fatalf("resetPassword: %v", resetErr)
	}

	user, err := db.Users().GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("read the account: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword(
		[]byte(user.PasswordHash), []byte(strings.TrimSpace(out))); err != nil {
		t.Errorf("reset did not apply to the normalised username: %v", err)
	}
}

func TestResetPasswordNamesTheAccountsItKnows(t *testing.T) {
	cfg, db := newDataDir(t)
	ctx := context.Background()

	if err := db.Users().Create(ctx, &domain.User{
		Username: "ops", PasswordHash: "x", Role: domain.RoleViewer,
	}); err != nil {
		t.Fatalf("create a second account: %v", err)
	}

	out := captureStdout(t, func() {
		err := resetPassword(ctx, cfg, "adnim")
		if err == nil {
			t.Error("resetting an unknown account should fail")
			return
		}
		// A typo in the username is the likeliest way to reach this, and the
		// operator running it may have forgotten the username too.
		for _, want := range []string{`"adnim"`, "admin", "ops"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should mention %q", err, want)
			}
		}
	})
	if out != "" {
		t.Errorf("nothing should reach stdout on failure, got %q", out)
	}
}

func TestResetPasswordDoesNotCreateMissingAccounts(t *testing.T) {
	cfg, db := newDataDir(t)
	ctx := context.Background()

	captureStdout(t, func() {
		if err := resetPassword(ctx, cfg, "newcomer"); err == nil {
			t.Error("resetting an unknown account should fail")
		}
	})

	if _, err := db.Users().GetByUsername(ctx, "newcomer"); err == nil {
		t.Error("the account was created; reset must never be a back door to a new login")
	}
}
