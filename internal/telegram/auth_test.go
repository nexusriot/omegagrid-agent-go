package telegram

import (
	"path/filepath"
	"strconv"
	"testing"
)

// newAuthStore builds an enabled, file-backed store the way AuthFromEnv does.
func newAuthStore(t *testing.T, adminID int64) *AuthStore {
	t.Helper()
	t.Setenv("BOT_AUTH_ENABLED", "true")
	t.Setenv("BOT_ADMIN_ID", strconv.FormatInt(adminID, 10))
	t.Setenv("BOT_AUTH_DB", filepath.Join(t.TempDir(), "auth.sqlite3"))

	a, err := AuthFromEnv()
	if err != nil {
		t.Fatalf("AuthFromEnv: %v", err)
	}
	t.Cleanup(func() {
		if a.db != nil {
			_ = a.db.Close()
		}
	})
	return a
}

// With auth off the bot is open to everyone, and none of the store methods may
// touch the (nil) database.
func TestAuthDisabledAcceptsEveryone(t *testing.T) {
	t.Setenv("BOT_AUTH_ENABLED", "")
	a, err := AuthFromEnv()
	if err != nil {
		t.Fatalf("AuthFromEnv: %v", err)
	}
	if a.IsEnabled() {
		t.Fatal("auth reported enabled without BOT_AUTH_ENABLED")
	}
	if !a.IsAuthorized(12345) {
		t.Fatal("a disabled allowlist must authorize every chat")
	}
	if a.IsAdmin(12345) {
		t.Fatal("nobody is admin while auth is disabled")
	}
	// These would nil-panic if they reached the database.
	if err := a.AddUser(1); err != nil {
		t.Fatalf("AddUser with auth disabled: %v", err)
	}
	a.Touch(1)
	users, err := a.ListUsers(10)
	if err != nil || users != nil {
		t.Fatalf("ListUsers = %v, %v; want nil, nil", users, err)
	}
}

// Enabling auth without an admin is a misconfiguration: nobody could ever
// allowlist anyone, so the bot must refuse to start rather than lock itself out.
func TestAuthEnabledRequiresAdminID(t *testing.T) {
	t.Setenv("BOT_AUTH_ENABLED", "true")
	t.Setenv("BOT_ADMIN_ID", "0")
	if _, err := AuthFromEnv(); err == nil {
		t.Fatal("expected an error when auth is enabled with BOT_ADMIN_ID=0")
	}

	t.Setenv("BOT_ADMIN_ID", "")
	if _, err := AuthFromEnv(); err == nil {
		t.Fatal("expected an error when auth is enabled with no BOT_ADMIN_ID")
	}
}

func TestAuthAdminIsAlwaysAuthorized(t *testing.T) {
	const admin = 999
	a := newAuthStore(t, admin)

	if !a.IsEnabled() {
		t.Fatal("store reported disabled")
	}
	if a.AdminID() != admin {
		t.Fatalf("AdminID = %d, want %d", a.AdminID(), admin)
	}
	if !a.IsAdmin(admin) {
		t.Fatal("admin not recognised")
	}
	// The admin is authorized without ever being inserted into the table.
	if !a.IsAuthorized(admin) {
		t.Fatal("admin is not authorized")
	}
	if users, _ := a.ListUsers(10); len(users) != 0 {
		t.Fatalf("admin leaked into the users table: %v", users)
	}
}

func TestAuthAllowlistRoundTrip(t *testing.T) {
	a := newAuthStore(t, 1)

	if a.IsAuthorized(4242) {
		t.Fatal("an unknown chat is authorized")
	}
	if a.IsAdmin(4242) {
		t.Fatal("a non-admin chat reported as admin")
	}

	if err := a.AddUser(4242); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if !a.IsAuthorized(4242) {
		t.Fatal("an allowlisted chat is not authorized")
	}

	users, err := a.ListUsers(10)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].TelegramID != 4242 {
		t.Fatalf("ListUsers = %v, want the one added user", users)
	}
	if users[0].CreatedAt == "" || users[0].LastActivity == "" {
		t.Fatalf("timestamps not populated: %+v", users[0])
	}
}

// /auth_add on an existing user must refresh them, not fail on the primary key.
func TestAuthAddUserIsIdempotent(t *testing.T) {
	a := newAuthStore(t, 1)

	for i := 0; i < 3; i++ {
		if err := a.AddUser(7); err != nil {
			t.Fatalf("AddUser attempt %d: %v", i, err)
		}
	}
	users, err := a.ListUsers(10)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("re-adding one user produced %d rows", len(users))
	}
}

func TestAuthTouchUpdatesLastActivity(t *testing.T) {
	a := newAuthStore(t, 1)
	if err := a.AddUser(7); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	// Touch must not create rows for unknown chats, and must not touch the admin.
	a.Touch(12345)
	a.Touch(a.AdminID())
	a.Touch(7)

	users, err := a.ListUsers(10)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].TelegramID != 7 {
		t.Fatalf("Touch changed the allowlist: %v", users)
	}
}

func TestAuthListUsersRespectsLimit(t *testing.T) {
	a := newAuthStore(t, 1)
	for _, id := range []int64{10, 11, 12, 13} {
		if err := a.AddUser(id); err != nil {
			t.Fatalf("AddUser(%d): %v", id, err)
		}
	}
	users, err := a.ListUsers(2)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("ListUsers(2) returned %d rows", len(users))
	}
}

func TestBoolEnv(t *testing.T) {
	cases := map[string]bool{
		"true": true, "TRUE": true, "1": true, "yes": true, " true ": true,
		"false": false, "0": false, "no": false, "FALSE": false,
	}
	for in, want := range cases {
		t.Setenv("TEST_BOOL_ENV", in)
		if got := boolEnv("TEST_BOOL_ENV", !want); got != want {
			t.Fatalf("boolEnv(%q) = %v, want %v", in, got, want)
		}
	}
	// Unset and unrecognised values keep the caller's default.
	t.Setenv("TEST_BOOL_ENV", "")
	if !boolEnv("TEST_BOOL_ENV", true) {
		t.Fatal("boolEnv discarded the default for an empty value")
	}
	t.Setenv("TEST_BOOL_ENV", "maybe")
	if !boolEnv("TEST_BOOL_ENV", true) {
		t.Fatal("boolEnv discarded the default for an unrecognised value")
	}
}
