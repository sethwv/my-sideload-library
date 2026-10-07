package users

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/sethwv/my-sideload-library/internal/mail"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBootstrap_CreatesFirstAdmin(t *testing.T) {
	s := openTestStore(t)

	if err := s.Bootstrap("admin", "hunter2"); err != nil {
		t.Fatal(err)
	}

	if !s.CheckPassword("admin", "hunter2") {
		t.Error("expected bootstrap admin to be able to log in")
	}
	if !s.IsAdmin("admin") {
		t.Error("expected bootstrap user to be admin")
	}
}

func TestBootstrap_NoOpIfUsersExist(t *testing.T) {
	s := openTestStore(t)

	if err := s.Bootstrap("admin", "hunter2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Bootstrap("someone-else", "different"); err != nil {
		t.Fatal(err)
	}

	if s.CheckPassword("someone-else", "different") {
		t.Error("expected second bootstrap call to be a no-op")
	}
	if !s.CheckPassword("admin", "hunter2") {
		t.Error("expected original admin to still work")
	}
}

func TestSessionSecret_PersistsAndDoesNotChange(t *testing.T) {
	s := openTestStore(t)
	first, created, err := s.SessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !created || len(first) < 32 {
		t.Fatalf("first SessionSecret() = %q, %v", first, created)
	}
	second, created, err := s.SessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	if created || second != first {
		t.Errorf("second SessionSecret() = %q, %v; want %q, false", second, created, first)
	}
}

func TestCreateFirstAdmin_CreatesOnlyOnce(t *testing.T) {
	s := openTestStore(t)
	created, err := s.CreateFirstAdmin("admin", "first-password")
	if err != nil {
		t.Fatal(err)
	}
	if !created || !s.CheckPassword("admin", "first-password") || !s.IsAdmin("admin") {
		t.Fatal("expected first administrator to be created")
	}
	created, err = s.CreateFirstAdmin("other", "second-password")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("expected subsequent first-admin creation to be rejected")
	}
}

func TestCreateAndCheckPassword(t *testing.T) {
	s := openTestStore(t)

	if err := s.Create("bob", "s3cret", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	if !s.CheckPassword("bob", "s3cret") {
		t.Error("expected correct credentials to pass")
	}
	if s.CheckPassword("bob", "wrong") {
		t.Error("expected wrong password to fail")
	}
	if s.CheckPassword("nobody", "s3cret") {
		t.Error("expected unknown username to fail")
	}
	if s.IsAdmin("bob") {
		t.Error("expected non-admin user to not be admin")
	}
}

func TestConnectionItemsPage_SearchPaginationAndProviderOrder(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("reader", "password", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	items := []ConnectionItem{
		{RemoteShelfKey: "read", ExternalID: "third", Title: "Gamma", Author: "Author", SourcePosition: 2},
		{RemoteShelfKey: "read", ExternalID: "first", Title: "Alpha", Author: "Author", SourcePosition: 0},
		{RemoteShelfKey: "read", ExternalID: "second", Title: "Beta", Author: "Author", SourcePosition: 1},
	}
	if err := s.ReplaceConnectionSnapshot("reader", "goodreads", []ConnectionShelf{{RemoteKey: "read", Name: "Read"}}, items); err != nil {
		t.Fatal(err)
	}

	count, err := s.CountConnectionItems("reader", "goodreads", "read", "a")
	if err != nil || count != 3 {
		t.Fatalf("CountConnectionItems() = %d, %v; want 3, nil", count, err)
	}
	page, err := s.ConnectionItemsPage("reader", "goodreads", "read", "", "provider", false, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ExternalID != "second" {
		t.Fatalf("provider page = %#v, want second item", page)
	}
	page, err = s.ConnectionItemsPage("reader", "goodreads", "read", "beta", "provider", false, 1, 10)
	if err != nil || len(page) != 1 || page[0].ExternalID != "second" {
		t.Fatalf("search page = %#v, %v; want Beta", page, err)
	}
}

func TestReplaceConnectionSnapshot_PreservesEnrichmentAndRetryState(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("reader", "password", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	shelves := []ConnectionShelf{{RemoteKey: "read", Name: "Read"}}
	items := []ConnectionItem{{RemoteShelfKey: "read", ExternalID: "book", Title: "Original", Author: "Author"}}
	if err := s.ReplaceConnectionSnapshot("reader", "goodreads", shelves, items); err != nil {
		t.Fatal(err)
	}
	candidate := ConnectionEnrichmentCandidate{Username: "reader", Provider: "goodreads", RemoteShelfKey: "read", ExternalID: "book"}
	if err := s.SetConnectionItemEnrichment(candidate, "hc-1", "Enriched", "Better Author", "https://cover.example/book.jpg", "done"); err != nil {
		t.Fatal(err)
	}
	items[0].Title = "Updated Source Title"
	if err := s.ReplaceConnectionSnapshot("reader", "goodreads", shelves, items); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConnectionItems("reader", "goodreads", "read")
	if err != nil || len(got) != 1 {
		t.Fatalf("ConnectionItems() = %#v, %v", got, err)
	}
	if got[0].Title != "Updated Source Title" || got[0].HardcoverID != "hc-1" || got[0].EnrichedTitle != "Enriched" || got[0].CoverURL != "https://cover.example/book.jpg" || got[0].EnrichmentStatus != "done" {
		t.Errorf("snapshot item = %#v, want updated source fields and preserved enrichment", got[0])
	}
}

func TestConnectionEnrichmentCandidates_RetriesErrorsOnlyWhenDue(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("reader", "password", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceConnectionSnapshot("reader", "goodreads", []ConnectionShelf{{RemoteKey: "read", Name: "Read"}}, []ConnectionItem{{RemoteShelfKey: "read", ExternalID: "book", Title: "Book"}}); err != nil {
		t.Fatal(err)
	}
	candidate := ConnectionEnrichmentCandidate{Username: "reader", Provider: "goodreads", RemoteShelfKey: "read", ExternalID: "book"}
	if err := s.SetConnectionItemEnrichment(candidate, "", "", "", "", "error"); err != nil {
		t.Fatal(err)
	}
	if candidates, err := s.ConnectionEnrichmentCandidates(10); err != nil || len(candidates) != 0 {
		t.Fatalf("backed off candidates = %#v, %v; want none", candidates, err)
	}
	if _, err := s.sql.Exec(`UPDATE connection_items SET enrichment_next_retry_at = 0 WHERE provider = 'goodreads' AND external_id = 'book'`); err != nil {
		t.Fatal(err)
	}
	if candidates, err := s.ConnectionEnrichmentCandidates(10); err != nil || len(candidates) != 1 {
		t.Fatalf("due retry candidates = %#v, %v; want one", candidates, err)
	}
	if pending, err := s.ConnectionEnrichmentPending(); err != nil || pending != 1 {
		t.Fatalf("ConnectionEnrichmentPending() = %d, %v; want 1, nil", pending, err)
	}
}

func TestSetEnabledRevokesAccessAndPreservesAccount(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("reader", "password", RoleMember, true, "reader@example.com"); err != nil {
		t.Fatal(err)
	}
	u, err := s.UserByUsername("reader")
	if err != nil || u == nil {
		t.Fatalf("UserByUsername() = (%+v, %v)", u, err)
	}
	if err := s.SetDigestSubscribed("reader", true); err != nil {
		t.Fatal(err)
	}
	bookmark, err := s.GenerateBookmarkToken("reader")
	if err != nil {
		t.Fatal(err)
	}
	reset, _, found, err := s.RequestPasswordReset("reader@example.com")
	if err != nil || !found {
		t.Fatalf("RequestPasswordReset() = (%q, %t, %v)", reset, found, err)
	}

	if err := s.SetEnabled(u.ID, false); err != nil {
		t.Fatal(err)
	}
	if s.CheckPassword("reader", "password") {
		t.Error("disabled account accepted password login")
	}
	if _, ok := s.VerifyBookmarkToken(bookmark); ok {
		t.Error("disabled account accepted bookmark token")
	}
	if _, ok := s.VerifyResetToken(reset); ok {
		t.Error("disabled account accepted reset token")
	}
	if _, _, found, err := s.RequestPasswordReset("reader@example.com"); err != nil || found {
		t.Errorf("disabled RequestPasswordReset() found=%t, err=%v", found, err)
	}

	if err := s.SetEnabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	restored, err := s.UserByID(u.ID)
	if err != nil || restored == nil {
		t.Fatalf("UserByID() = (%+v, %v)", restored, err)
	}
	if !restored.Enabled || restored.Email != "reader@example.com" || !restored.CanBookmark || !restored.DigestSubscribed {
		t.Errorf("restored account = %+v", restored)
	}
	if !s.CheckPassword("reader", "password") {
		t.Error("re-enabled account did not restore password access")
	}
}

func TestSetEnabledRefusesLastEnabledAdmin(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("admin1", "password", RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("admin2", "password", RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}
	admin1, _ := s.UserByUsername("admin1")
	admin2, _ := s.UserByUsername("admin2")
	if err := s.SetEnabled(admin2.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(admin1.ID, false); err == nil {
		t.Error("disabled the final enabled administrator")
	}
}

func TestUserLookups(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("reader", "password", RoleUserManager, true, "reader@example.com"); err != nil {
		t.Fatal(err)
	}

	byName, err := s.UserByUsername("reader")
	if err != nil || byName == nil {
		t.Fatalf("UserByUsername() = (%+v, %v)", byName, err)
	}
	if !byName.CanManageUsers || byName.CanManageServer || byName.Email != "reader@example.com" {
		t.Errorf("UserByUsername() = %+v", byName)
	}
	byID, err := s.UserByID(byName.ID)
	if err != nil || byID == nil || byID.Username != "reader" {
		t.Errorf("UserByID() = (%+v, %v)", byID, err)
	}
	missing, err := s.UserByUsername("missing")
	if err != nil || missing != nil {
		t.Errorf("missing user = (%+v, %v), want (nil, nil)", missing, err)
	}
}

func TestDelete_RefusesLastAdmin(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("admin", "hunter2", RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}

	users, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}

	if err := s.Delete(users[0].ID); err == nil {
		t.Error("expected deleting the last admin to fail")
	}
}

func TestDelete_AllowsNonLastAdmin(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("admin1", "hunter2", RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("admin2", "hunter2", RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}

	users, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}

	if err := s.Delete(users[0].ID); err != nil {
		t.Errorf("expected deleting one of two admins to succeed, got %v", err)
	}
}

func TestDelete_AllowsNonAdmin(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("admin", "hunter2", RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("regular", "hunter2", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	users, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var regularID int64
	for _, u := range users {
		if u.Username == "regular" {
			regularID = u.ID
		}
	}
	if err := s.Delete(regularID); err != nil {
		t.Errorf("expected deleting a non-admin to succeed, got %v", err)
	}
	if s.CheckPassword("regular", "hunter2") {
		t.Error("expected deleted user to no longer be able to log in")
	}
}

func TestResetPassword(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("bob", "old-pass", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	users, _ := s.List()
	if err := s.ResetPassword(users[0].ID, "new-pass"); err != nil {
		t.Fatal(err)
	}

	if s.CheckPassword("bob", "old-pass") {
		t.Error("expected old password to no longer work")
	}
	if !s.CheckPassword("bob", "new-pass") {
		t.Error("expected new password to work")
	}
}

func TestBookmarkToken_GenerateVerifyRevoke(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("bob", "s3cret", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	if s.HasBookmarkToken("bob") {
		t.Error("expected no bookmark token before one is generated")
	}

	token, err := s.GenerateBookmarkToken("bob")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if !s.HasBookmarkToken("bob") {
		t.Error("expected HasBookmarkToken to be true after generating one")
	}

	username, ok := s.VerifyBookmarkToken(token)
	if !ok || username != "bob" {
		t.Errorf("VerifyBookmarkToken(token) = %q, %v; want bob, true", username, ok)
	}

	if _, ok := s.VerifyBookmarkToken("not-the-right-token"); ok {
		t.Error("expected a wrong token to fail verification")
	}
	if _, ok := s.VerifyBookmarkToken(""); ok {
		t.Error("expected an empty token to fail verification")
	}

	if err := s.RevokeBookmarkToken("bob"); err != nil {
		t.Fatal(err)
	}
	if s.HasBookmarkToken("bob") {
		t.Error("expected no bookmark token after revoking")
	}
	if _, ok := s.VerifyBookmarkToken(token); ok {
		t.Error("expected the old token to stop working after revoking")
	}
}

func TestBookmarkToken_RegenerateInvalidatesPrevious(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("bob", "s3cret", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	first, err := s.GenerateBookmarkToken("bob")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.GenerateBookmarkToken("bob")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("expected regenerating to produce a different token")
	}

	if _, ok := s.VerifyBookmarkToken(first); ok {
		t.Error("expected the first token to stop working once regenerated")
	}
	username, ok := s.VerifyBookmarkToken(second)
	if !ok || username != "bob" {
		t.Errorf("VerifyBookmarkToken(second) = %q, %v; want bob, true", username, ok)
	}
}

// TestMigration_BackfillsRoleAndBookmarkFromLegacySchema simulates opening a
// users.db created before role/can_bookmark existed: an admin row with only
// is_admin=1 set. The migration must promote it to role=admin and must not
// take away bookmark access from any existing user.
func TestMigration_BackfillsRoleAndBookmarkFromLegacySchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "users.db")

	legacy, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE users (
		    id            INTEGER PRIMARY KEY AUTOINCREMENT,
		    username      TEXT NOT NULL UNIQUE,
		    password_hash TEXT NOT NULL,
		    is_admin      INTEGER NOT NULL DEFAULT 0,
		    created_at    INTEGER NOT NULL
		)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO users (username, password_hash, is_admin, created_at) VALUES (?, ?, 1, ?), (?, ?, 0, ?)`,
		"legacyadmin", "hash", time.Now().Unix(), "legacyreader", "hash", time.Now().Unix(),
	); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if !s.IsAdmin("legacyadmin") {
		t.Error("expected legacy is_admin=1 user to become role=admin")
	}
	if !s.CanManageUsers("legacyadmin") || !s.CanManageServer("legacyadmin") {
		t.Error("expected migrated admin to have both capabilities")
	}
	if !s.CanUseBookmark("legacyadmin") || !s.CanUseBookmark("legacyreader") {
		t.Error("expected every pre-existing user to keep bookmark-link access after migration")
	}
	if secret, created, err := s.SessionSecret(); err != nil || !created || secret == "" {
		t.Errorf("SessionSecret() after legacy migration = %q, %v, %v", secret, created, err)
	}
	if s.CanManageUsers("legacyreader") || s.CanManageServer("legacyreader") {
		t.Error("expected legacy non-admin user to become a plain member")
	}
}

func TestCanManageUsersAndServer_ByRole(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("admin", "pw", RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("um", "pw", RoleUserManager, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("sm", "pw", RoleServerManager, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("member", "pw", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		username         string
		wantManageUsers  bool
		wantManageServer bool
	}{
		{"admin", true, true},
		{"um", true, false},
		{"sm", false, true},
		{"member", false, false},
	}
	for _, c := range cases {
		if got := s.CanManageUsers(c.username); got != c.wantManageUsers {
			t.Errorf("CanManageUsers(%q) = %v, want %v", c.username, got, c.wantManageUsers)
		}
		if got := s.CanManageServer(c.username); got != c.wantManageServer {
			t.Errorf("CanManageServer(%q) = %v, want %v", c.username, got, c.wantManageServer)
		}
	}
}

func TestPermissions_CombineRoleDefaultsAndUserOverrides(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("admin", "pw", RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("member", "pw", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	users, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var memberID int64
	for _, user := range users {
		if user.Username == "member" {
			memberID = user.ID
		}
	}
	if !s.Can("member", PermissionOwnShelves) {
		t.Fatal("members should receive the default own-shelf capability")
	}
	if s.Can("member", PermissionManageShelves) {
		t.Fatal("members must not administer other users' shelves")
	}
	if !s.Can("admin", PermissionManageShelves) {
		t.Fatal("administrators should administer all shelves")
	}
	if s.Can("member", PermissionCreatePublicShelves) {
		t.Fatal("members must not create public shelves by default")
	}
	if !s.Can("admin", PermissionCreatePublicShelves) {
		t.Fatal("administrators should create public shelves")
	}
	revoke := false
	if err := s.SetPermissionOverride(memberID, PermissionOwnShelves, &revoke); err != nil {
		t.Fatal(err)
	}
	if s.Can("member", PermissionOwnShelves) {
		t.Fatal("explicit revocation should override the role default")
	}
	grant := true
	if err := s.SetPermissionOverride(memberID, PermissionManageShelves, &grant); err != nil {
		t.Fatal(err)
	}
	if !s.Can("member", PermissionManageShelves) {
		t.Fatal("explicit grant should override the role default")
	}
	if err := s.SetPermissionOverride(memberID, PermissionOwnShelves, nil); err != nil {
		t.Fatal(err)
	}
	if !s.Can("member", PermissionOwnShelves) {
		t.Fatal("clearing an override should restore the role default")
	}
	if err := s.SetEnabled(memberID, false); err != nil {
		t.Fatal(err)
	}
	if s.Can("member", PermissionManageShelves) {
		t.Fatal("disabled users must not retain effective permissions")
	}
}

func TestSetRole_RefusesDemotingLastAdmin(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("admin", "pw", RoleAdmin, true, ""); err != nil {
		t.Fatal(err)
	}
	users, _ := s.List()

	if err := s.SetRole(users[0].ID, RoleMember, true); err == nil {
		t.Error("expected demoting the last admin to fail")
	}
}

func TestSetRole_CanBookmarkRevokesTokenUse(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("bob", "pw", RoleMember, true, ""); err != nil {
		t.Fatal(err)
	}
	users, _ := s.List()

	token, err := s.GenerateBookmarkToken("bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyBookmarkToken(token); !ok {
		t.Fatal("expected token to work before revoking")
	}

	if err := s.SetRole(users[0].ID, RoleMember, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyBookmarkToken(token); ok {
		t.Error("expected token to stop working once can_bookmark is revoked, even though the hash still matches")
	}
}

func TestPasswordReset_RequestVerifyComplete(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("bob", "old-pass", RoleMember, true, "bob@example.com"); err != nil {
		t.Fatal(err)
	}

	token, username, found, err := s.RequestPasswordReset("bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !found || username != "bob" {
		t.Fatalf("RequestPasswordReset = %q, %v; want bob, true", username, found)
	}

	if _, _, found, err := s.RequestPasswordReset("nobody@example.com"); err != nil || found {
		t.Errorf("RequestPasswordReset(unknown email) = found %v, err %v; want false, nil", found, err)
	}

	if got, ok := s.VerifyResetToken(token); !ok || got != "bob" {
		t.Errorf("VerifyResetToken = %q, %v; want bob, true", got, ok)
	}

	if err := s.CompletePasswordReset(token, "new-pass"); err != nil {
		t.Fatal(err)
	}
	if s.CheckPassword("bob", "old-pass") {
		t.Error("expected old password to no longer work")
	}
	if !s.CheckPassword("bob", "new-pass") {
		t.Error("expected new password to work")
	}
	if _, ok := s.VerifyResetToken(token); ok {
		t.Error("expected reset token to be single-use")
	}
}

func TestInvite_AcceptEnablesLogin(t *testing.T) {
	s := openTestStore(t)
	token, err := s.InviteUser("newbie", "newbie@example.com", RoleMember, true)
	if err != nil {
		t.Fatal(err)
	}

	if s.CheckPassword("newbie", "") || s.CheckPassword("newbie", "anything") {
		t.Error("expected an un-accepted invite to never allow login")
	}

	if err := s.AcceptInvite(token, "chosen-pass"); err != nil {
		t.Fatal(err)
	}
	if !s.CheckPassword("newbie", "chosen-pass") {
		t.Error("expected login to work with the password set during invite acceptance")
	}
	if _, ok := s.VerifyInviteToken(token); ok {
		t.Error("expected invite token to be single-use")
	}

	list, _ := s.List()
	for _, u := range list {
		if u.Username == "newbie" && u.InvitePending {
			t.Error("expected InvitePending to clear after accepting")
		}
	}
}

func TestInvite_ResendIssuesNewToken(t *testing.T) {
	s := openTestStore(t)
	first, err := s.InviteUser("newbie", "newbie@example.com", RoleMember, true)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()

	second, err := s.ResendInvite(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("expected resend to produce a different token")
	}
	if _, ok := s.VerifyInviteToken(first); ok {
		t.Error("expected the original invite token to stop working after resend")
	}
	if _, ok := s.VerifyInviteToken(second); !ok {
		t.Error("expected the new invite token to verify")
	}
}

func TestTimedTokens_Expire(t *testing.T) {
	cases := []struct {
		name      string
		issue     func(*Store) (string, error)
		verify    func(*Store, string) (string, bool)
		consume   func(*Store, string) error
		username  string
		createdAt string
		ttl       time.Duration
	}{
		{
			name: "reset",
			issue: func(s *Store) (string, error) {
				token, _, _, err := s.RequestPasswordReset("bob@example.com")
				return token, err
			},
			verify:    func(s *Store, token string) (string, bool) { return s.VerifyResetToken(token) },
			consume:   func(s *Store, token string) error { return s.CompletePasswordReset(token, "new-pass") },
			username:  "bob",
			createdAt: resetTokenSpec.createdAtColumn,
			ttl:       resetTokenSpec.ttl,
		},
		{
			name: "invite",
			issue: func(s *Store) (string, error) {
				return s.InviteUser("bob", "bob@example.com", RoleMember, true)
			},
			verify:    func(s *Store, token string) (string, bool) { return s.VerifyInviteToken(token) },
			consume:   func(s *Store, token string) error { return s.AcceptInvite(token, "new-pass") },
			username:  "bob",
			createdAt: inviteTokenSpec.createdAtColumn,
			ttl:       inviteTokenSpec.ttl,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			if tc.name == "reset" {
				if err := s.Create("bob", "old-pass", RoleMember, true, "bob@example.com"); err != nil {
					t.Fatal(err)
				}
			}
			token, err := tc.issue(s)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.sql.Exec(`UPDATE users SET `+tc.createdAt+` = ? WHERE username = ?`, time.Now().Add(-tc.ttl-time.Second).Unix(), tc.username); err != nil {
				t.Fatal(err)
			}
			if _, ok := tc.verify(s, token); ok {
				t.Error("expired token verified")
			}
			if err := tc.consume(s, token); err == nil {
				t.Error("expired token was consumed")
			}
		})
	}
}

func TestTimedTokens_ReissueConsumeAndUpdatePassword(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*Store) error
		issue   func(*Store) (string, error)
		reissue func(*Store) (string, error)
		verify  func(*Store, string) (string, bool)
		consume func(*Store, string) error
	}{
		{
			name:  "reset",
			setup: func(s *Store) error { return s.Create("bob", "old-pass", RoleMember, true, "bob@example.com") },
			issue: func(s *Store) (string, error) {
				token, _, _, err := s.RequestPasswordReset("bob@example.com")
				return token, err
			},
			reissue: func(s *Store) (string, error) {
				token, _, _, err := s.RequestPasswordReset("bob@example.com")
				return token, err
			},
			verify:  func(s *Store, token string) (string, bool) { return s.VerifyResetToken(token) },
			consume: func(s *Store, token string) error { return s.CompletePasswordReset(token, "new-pass") },
		},
		{
			name:  "invite",
			setup: func(*Store) error { return nil },
			issue: func(s *Store) (string, error) {
				return s.InviteUser("bob", "bob@example.com", RoleMember, true)
			},
			reissue: func(s *Store) (string, error) {
				u, err := s.UserByUsername("bob")
				if err != nil {
					return "", err
				}
				return s.ResendInvite(u.ID)
			},
			verify:  func(s *Store, token string) (string, bool) { return s.VerifyInviteToken(token) },
			consume: func(s *Store, token string) error { return s.AcceptInvite(token, "new-pass") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			if err := tc.setup(s); err != nil {
				t.Fatal(err)
			}
			first, err := tc.issue(s)
			if err != nil {
				t.Fatal(err)
			}
			second, err := tc.reissue(s)
			if err != nil {
				t.Fatal(err)
			}
			if first == second {
				t.Fatal("reissuing returned the old token")
			}
			if _, ok := tc.verify(s, first); ok {
				t.Error("reissuing did not invalidate the first token")
			}
			if err := tc.consume(s, second); err != nil {
				t.Fatal(err)
			}
			if !s.CheckPassword("bob", "new-pass") {
				t.Error("consuming token did not update the password")
			}
			if _, ok := tc.verify(s, second); ok {
				t.Error("consumed token remained valid")
			}
			if err := tc.consume(s, second); err == nil {
				t.Error("consumed token was accepted a second time")
			}
		})
	}
}

func TestCreate_RefusesDuplicateEmail(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("bob", "pw", RoleMember, true, "shared@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("alice", "pw", RoleMember, true, "shared@example.com"); err == nil {
		t.Error("expected creating a second user with the same email to fail")
	}
	if err := s.Create("carol", "pw", RoleMember, true, ""); err != nil {
		t.Error("expected multiple users with no email to be allowed")
	}
	if err := s.Create("dave", "pw", RoleMember, true, ""); err != nil {
		t.Error("expected a second user with no email to also be allowed")
	}
}

func TestDigestSubscribers(t *testing.T) {
	s := openTestStore(t)
	if err := s.Create("bob", "pw", RoleMember, true, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("carol", "pw", RoleMember, true, "carol@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("dave", "pw", RoleMember, true, ""); err != nil {
		t.Fatal(err) // no email on file
	}

	if err := s.SetDigestSubscribed("bob", true); err != nil {
		t.Fatal(err)
	}
	if !s.IsDigestSubscribed("bob") {
		t.Error("expected bob to be subscribed")
	}
	if s.IsDigestSubscribed("carol") {
		t.Error("expected carol to default to unsubscribed")
	}

	// dave has no email, so even if subscribed he shouldn't show up.
	if err := s.SetDigestSubscribed("dave", true); err != nil {
		t.Fatal(err)
	}

	subs, err := s.DigestSubscribers()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0] != "bob@example.com" {
		t.Errorf("DigestSubscribers() = %v, want [bob@example.com]", subs)
	}
}

func TestSMTPSettings_SaveAndGet(t *testing.T) {
	s := openTestStore(t)

	empty, err := s.GetSMTPSettings()
	if err != nil {
		t.Fatal(err)
	}
	if empty.Enabled() {
		t.Error("expected no saved settings to mean disabled")
	}

	m := mail.Settings{
		Host: "smtp.example.com", Port: 465, Encryption: "tls",
		Username: "user", Password: "pass", FromName: "No Reply", FromAddress: "noreply@example.com",
	}
	if err := s.SaveSMTPSettings(m); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetSMTPSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != m {
		t.Errorf("GetSMTPSettings() = %+v, want %+v", got, m)
	}

	// Saving again should upsert, not duplicate.
	m.Host = "smtp2.example.com"
	if err := s.SaveSMTPSettings(m); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSMTPSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "smtp2.example.com" {
		t.Errorf("GetSMTPSettings().Host = %q, want smtp2.example.com", got.Host)
	}
}

func TestIntegrationSettings_SaveAndGet(t *testing.T) {
	s := openTestStore(t)

	empty, err := s.GetIntegrationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if empty != (IntegrationSettings{}) {
		t.Errorf("GetIntegrationSettings() with nothing saved = %+v, want the zero value", empty)
	}

	m := IntegrationSettings{
		HardcoverEnabled:        true,
		HardcoverToken:          "hc-token",
		ChaptarrEnabled:         true,
		ChaptarrURL:             "http://chaptarr.local:8978",
		ChaptarrAPIKey:          "ch-key",
		HardcoverOverwriteCover: true,
	}
	if err := s.SaveIntegrationSettings(m); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetIntegrationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != m {
		t.Errorf("GetIntegrationSettings() = %+v, want %+v", got, m)
	}

	// Saving again should upsert, not duplicate, and disabling should
	// persist (not just adding fields).
	m.HardcoverEnabled = false
	if err := s.SaveIntegrationSettings(m); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetIntegrationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.HardcoverEnabled {
		t.Error("expected HardcoverEnabled to be persisted as false after re-saving")
	}
	if got.ChaptarrAPIKey != "ch-key" {
		t.Errorf("ChaptarrAPIKey = %q, want it left unchanged by the re-save", got.ChaptarrAPIKey)
	}
}

func TestGeneralSettings_SaveAndGet(t *testing.T) {
	s := openTestStore(t)

	empty, err := s.GetGeneralSettings()
	if err != nil {
		t.Fatal(err)
	}
	if empty != (GeneralSettings{}) {
		t.Errorf("GetGeneralSettings() with nothing saved = %+v, want the zero value", empty)
	}

	m := GeneralSettings{
		SiteName:             "My Library",
		PublicURL:            "https://library.example.com",
		CoverWidth:           400,
		PageSize:             24,
		SessionTTL:           48 * time.Hour,
		PasswordResetEnabled: true,
	}
	if err := s.SaveGeneralSettings(m); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetGeneralSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != m {
		t.Errorf("GetGeneralSettings() = %+v, want %+v", got, m)
	}

	// Saving again should upsert, not duplicate.
	m.SiteName = "Renamed Library"
	if err := s.SaveGeneralSettings(m); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetGeneralSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.SiteName != "Renamed Library" {
		t.Errorf("GetGeneralSettings().SiteName = %q, want Renamed Library", got.SiteName)
	}
}

func TestKepubSettings_SaveAndGet(t *testing.T) {
	s := openTestStore(t)

	defaults, err := s.GetKepubSettings()
	if err != nil {
		t.Fatal(err)
	}
	if defaults != (KepubSettings{Enabled: true}) {
		t.Errorf("GetKepubSettings() defaults = %+v, want enabled conversion", defaults)
	}

	want := KepubSettings{Enabled: false, WriteCalibreMetadata: true}
	if err := s.SaveKepubSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetKepubSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("GetKepubSettings() = %+v, want %+v", got, want)
	}
}

func TestSetEmail(t *testing.T) {
	s := openTestStore(t)

	if err := s.Create("alice", "hunter2", RoleMember, false, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("bob", "hunter2", RoleMember, false, "bob@example.com"); err != nil {
		t.Fatal(err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var aliceID int64
	for _, u := range list {
		if u.Username == "alice" {
			aliceID = u.ID
		}
	}
	if aliceID == 0 {
		t.Fatal("alice not found")
	}

	if err := s.SetEmail(aliceID, "alice2@example.com"); err != nil {
		t.Fatal(err)
	}
	list, err = s.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range list {
		if u.ID == aliceID && u.Email != "alice2@example.com" {
			t.Errorf("Email = %q, want alice2@example.com", u.Email)
		}
	}

	// Rejects duplicating another user's email.
	if err := s.SetEmail(aliceID, "bob@example.com"); err == nil {
		t.Error("expected SetEmail to reject an email already used by another user")
	}

	// Clearing to empty is allowed (multiple users can share an empty email).
	if err := s.SetEmail(aliceID, ""); err != nil {
		t.Fatal(err)
	}
}
