package index

import (
	"path/filepath"
	"strconv"
	"testing"
)

func TestEnsureSystemShelf_CreatesAndReuses(t *testing.T) {
	db := openTestDB(t)

	id1, err := db.EnsureSystemShelf("alice", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == 0 {
		t.Fatal("expected non-zero shelf id")
	}

	id2, err := db.EnsureSystemShelf("alice", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("EnsureSystemShelf not idempotent: got %d then %d", id1, id2)
	}

	// Different user gets their own shelf, same slug.
	id3, err := db.EnsureSystemShelf("bob", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}
	if id3 == id1 {
		t.Error("expected different users to get different shelf ids")
	}
}

func TestListShelves_SystemFirstAndPerUserIsolation(t *testing.T) {
	db := openTestDB(t)

	favID, err := db.EnsureSystemShelf("alice", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureSystemShelf("bob", "favourites", "Favourites"); err != nil {
		t.Fatal(err)
	}

	shelves, err := db.ListShelves("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(shelves) != 1 {
		t.Fatalf("expected 1 shelf for alice, got %d", len(shelves))
	}
	if shelves[0].ID != favID || shelves[0].Username != "alice" || !shelves[0].IsSystem {
		t.Errorf("ListShelves(alice) = %+v, want favourites shelf owned by alice", shelves[0])
	}
}

func TestGetShelf_FoundAndNotFound(t *testing.T) {
	db := openTestDB(t)

	id, err := db.EnsureSystemShelf("alice", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}

	sh, err := db.GetShelf(id)
	if err != nil {
		t.Fatal(err)
	}
	if sh == nil || sh.Username != "alice" || sh.Slug != "favourites" {
		t.Errorf("GetShelf(%d) = %+v, want alice's favourites shelf", id, sh)
	}

	missing, err := db.GetShelf(999999)
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Errorf("GetShelf(missing) = %+v, want nil", missing)
	}
}

func TestUserShelfLifecycleIsPrivateAndOwnerScoped(t *testing.T) {
	db := openTestDB(t)
	shelf, err := db.CreateShelf("alice", "To Read", 25)
	if err != nil {
		t.Fatal(err)
	}
	if shelf.IsSystem || shelf.Visibility != "private" || shelf.Slug != "to-read" {
		t.Errorf("created shelf = %+v, want a private non-system To Read shelf", shelf)
	}
	if owned, err := db.GetOwnedShelf("alice", shelf.ID); err != nil || owned == nil {
		t.Fatalf("GetOwnedShelf(alice) = %+v, %v", owned, err)
	}
	if owned, err := db.GetOwnedShelf("bob", shelf.ID); err != nil || owned != nil {
		t.Errorf("GetOwnedShelf(bob) = %+v, %v, want nil", owned, err)
	}
	if err := db.RenameShelf("alice", shelf.ID, "Reading Soon"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateShelf("alice", "reading soon", 25); err == nil {
		t.Error("expected case-insensitive duplicate shelf name to fail")
	}
	if err := db.DeleteShelf("bob", shelf.ID); err == nil {
		t.Error("expected another user to be unable to delete the shelf")
	}
	if err := db.DeleteShelf("alice", shelf.ID); err != nil {
		t.Fatal(err)
	}
	if shelf, err := db.GetShelf(shelf.ID); err != nil || shelf != nil {
		t.Errorf("GetShelf after delete = %+v, %v, want nil", shelf, err)
	}
}

func TestUserShelfLimitAndSystemShelfProtection(t *testing.T) {
	db := openTestDB(t)
	favorites, err := db.EnsureSystemShelf("alice", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RenameShelf("alice", favorites, "Renamed"); err == nil {
		t.Error("expected system shelf rename to fail")
	}
	if err := db.DeleteShelf("alice", favorites); err == nil {
		t.Error("expected system shelf delete to fail")
	}
	const limit = 5
	for i := 0; i < limit; i++ {
		if _, err := db.CreateShelf("alice", "Shelf "+strconv.Itoa(i), limit); err != nil {
			t.Fatalf("CreateShelf %d: %v", i, err)
		}
	}
	if _, err := db.CreateShelf("alice", "One too many", limit); err == nil {
		t.Errorf("expected more than %d user shelves to fail", limit)
	}
	for i := 0; i < limit+1; i++ {
		if _, err := db.CreateShelf("admin", "Shelf "+strconv.Itoa(i), 0); err != nil {
			t.Fatalf("unlimited CreateShelf %d: %v", i, err)
		}
	}
}

func TestIntegrationShelfIsPrivateAndProviderManaged(t *testing.T) {
	db := openTestDB(t)

	id, err := db.EnsureIntegrationShelf("alice", "goodreads", "to-read", "To Read")
	if err != nil {
		t.Fatal(err)
	}
	again, err := db.EnsureIntegrationShelf("alice", "goodreads", "to-read", "Want to Read")
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Fatalf("EnsureIntegrationShelf ids = %d, %d; want idempotent result", id, again)
	}
	shelf, err := db.GetShelf(id)
	if err != nil {
		t.Fatal(err)
	}
	if shelf == nil || !shelf.IsIntegration() || shelf.IsSystem || shelf.Visibility != ShelfVisibilityPrivate || shelf.Provider != "goodreads" || shelf.RemoteKey != "to-read" || shelf.Name != "Want to Read" {
		t.Fatalf("integration shelf = %+v", shelf)
	}

	if _, err := db.CreateShelf("alice", "Manual", 1); err != nil {
		t.Fatalf("integration shelf should not consume manual quota: %v", err)
	}
	if _, err := db.CreateShelf("alice", "One too many", 1); err == nil {
		t.Fatal("expected manual shelf limit after one manual shelf")
	}
	if editable, err := db.ListEditableShelves("alice"); err != nil || len(editable) != 1 || editable[0].IsIntegration() {
		t.Fatalf("ListEditableShelves = %+v, %v", editable, err)
	}
	if editable, err := db.GetEditableShelf("alice", id); err != nil || editable != nil {
		t.Fatalf("GetEditableShelf integration = %+v, %v; want nil", editable, err)
	}
	if visible, err := db.GetVisibleShelf("bob", id); err != nil || visible != nil {
		t.Fatalf("other-user visibility = %+v, %v; want nil", visible, err)
	}
	if _, err := db.sql.Exec(`INSERT INTO shelf_members (shelf_id, username, created_at) VALUES (?, ?, 0)`, id, "bob"); err != nil {
		t.Fatal(err)
	}
	if visible, err := db.GetVisibleShelf("bob", id); err != nil || visible != nil {
		t.Fatalf("member visibility = %+v, %v; want nil", visible, err)
	}

	for _, mutate := range []func() error{
		func() error { return db.RenameShelf("alice", id, "Renamed") },
		func() error { return db.DeleteShelf("alice", id) },
		func() error { return db.SetShelfVisibility("alice", id, ShelfVisibilityShared) },
		func() error { return db.AddShelfMember("alice", id, "bob") },
		func() error { return db.AddBookToShelf(id, 42) },
		func() error { return db.RemoveBookFromShelf(id, 42) },
	} {
		if err := mutate(); err == nil {
			t.Fatal("expected manual integration shelf mutation to fail")
		}
	}

	if _, err := db.ReplaceIntegrationShelfBooks("alice", "goodreads", "to-read", "Want to Read", []int64{42, 42, 99}); err != nil {
		t.Fatal(err)
	}
	for _, bookID := range []int64{42, 99} {
		on, err := db.IsBookOnShelf(id, bookID)
		if err != nil || !on {
			t.Fatalf("book %d after trusted sync = %t, %v", bookID, on, err)
		}
	}
	if _, err := db.ReplaceIntegrationShelfBooks("alice", "goodreads", "to-read", "Want to Read", []int64{99}); err != nil {
		t.Fatal(err)
	}
	if on, err := db.IsBookOnShelf(id, 42); err != nil || on {
		t.Fatalf("removed provider book after replacement = %t, %v", on, err)
	}
	if recent, err := db.RecentShelf("alice", 99); err != nil || recent != nil {
		t.Fatalf("RecentShelf integration = %+v, %v; want nil", recent, err)
	}
	if err := db.DeleteIntegrationShelf("alice", "goodreads", "to-read"); err != nil {
		t.Fatal(err)
	}
	if shelf, err := db.GetShelf(id); err != nil || shelf != nil {
		t.Fatalf("GetShelf after integration removal = %+v, %v", shelf, err)
	}
}

func TestShelfSharingAccessAndMembership(t *testing.T) {
	db := openTestDB(t)
	shared, err := db.CreateShelf("alice", "Club Picks", 25)
	if err != nil {
		t.Fatal(err)
	}
	public, err := db.CreateShelf("alice", "Public Picks", 25)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetShelfVisibility("alice", shared.ID, ShelfVisibilityShared); err != nil {
		t.Fatal(err)
	}
	if err := db.SetShelfVisibility("alice", public.ID, ShelfVisibilityPublic); err != nil {
		t.Fatal(err)
	}
	if err := db.AddShelfMember("alice", shared.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if shelves, err := db.ListEditableShelves("bob"); err != nil || len(shelves) != 1 || shelves[0].ID != shared.ID {
		t.Errorf("editable shelves for member = %+v, %v", shelves, err)
	}
	if shelf, err := db.GetVisibleShelf("carol", shared.ID); err != nil || shelf != nil {
		t.Errorf("uninvited shared access = %+v, %v; want nil", shelf, err)
	}
	if shelf, err := db.GetVisibleShelf("bob", shared.ID); err != nil || shelf == nil || shelf.Role != "member" {
		t.Errorf("member shared access = %+v, %v", shelf, err)
	}
	if shelf, err := db.GetEditableShelf("bob", shared.ID); err != nil || shelf == nil {
		t.Errorf("member editable access = %+v, %v", shelf, err)
	}
	if shelf, err := db.GetEditableShelf("carol", public.ID); err != nil || shelf != nil {
		t.Errorf("public reader editable access = %+v, %v; want nil", shelf, err)
	}
	if err := db.AddShelfMember("alice", public.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if shelf, err := db.GetEditableShelf("bob", public.ID); err != nil || shelf == nil {
		t.Errorf("public member editable access = %+v, %v", shelf, err)
	}
	if err := db.RemoveShelfMember("alice", shared.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if shelf, err := db.GetVisibleShelf("bob", shared.ID); err != nil || shelf != nil {
		t.Errorf("revoked shared access = %+v, %v; want nil", shelf, err)
	}
}

func TestDeleteShelfForManagerAndUserCleanup(t *testing.T) {
	db := openTestDB(t)
	shelf, err := db.CreateShelf("alice", "Reading", 25)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetShelfVisibility("alice", shelf.ID, ShelfVisibilityShared); err != nil {
		t.Fatal(err)
	}
	if err := db.AddShelfMember("alice", shelf.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddBookToShelf(shelf.ID, 42); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteShelfForManager(shelf.ID); err != nil {
		t.Fatal(err)
	}
	if shelf, err := db.GetShelf(shelf.ID); err != nil || shelf != nil {
		t.Errorf("deleted shelf = %+v, %v; want nil", shelf, err)
	}
	shelf, err = db.CreateShelf("alice", "Again", 25)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetShelfVisibility("alice", shelf.ID, ShelfVisibilityShared); err != nil {
		t.Fatal(err)
	}
	if err := db.AddShelfMember("alice", shelf.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUserShelves("bob"); err != nil {
		t.Fatal(err)
	}
	if member, err := db.GetVisibleShelf("bob", shelf.ID); err != nil || member != nil {
		t.Errorf("deleted user membership = %+v, %v; want nil", member, err)
	}
	if err := db.DeleteUserShelves("alice"); err != nil {
		t.Fatal(err)
	}
	if owner, err := db.GetShelf(shelf.ID); err != nil || owner != nil {
		t.Errorf("deleted owner shelf = %+v, %v; want nil", owner, err)
	}
}

func TestRecentShelfPrefersBookMembershipThenLastUsed(t *testing.T) {
	db := openTestDB(t)
	reading, err := db.CreateShelf("alice", "Reading", 25)
	if err != nil {
		t.Fatal(err)
	}
	later, err := db.CreateShelf("alice", "Later", 25)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddBookToShelf(reading.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE shelves SET last_used_at = CASE id WHEN ? THEN 10 WHEN ? THEN 20 END WHERE id IN (?, ?)`, reading.ID, later.ID, reading.ID, later.ID); err != nil {
		t.Fatal(err)
	}
	recent, err := db.RecentShelf("alice", 10)
	if err != nil || recent == nil || recent.ID != reading.ID {
		t.Fatalf("RecentShelf for member book = %+v, %v; want Reading", recent, err)
	}
	recent, err = db.RecentShelf("alice", 99)
	if err != nil || recent == nil || recent.ID != later.ID {
		t.Fatalf("RecentShelf fallback = %+v, %v; want Later", recent, err)
	}
	if err := db.RemoveBookFromShelf(later.ID, 99); err != nil {
		t.Fatal(err)
	}
	recent, err = db.RecentShelf("alice", 99)
	if err != nil || recent == nil || recent.ID != later.ID {
		t.Fatalf("RecentShelf after removal = %+v, %v; want Later", recent, err)
	}
}

func TestRecentShelfIncludesWritableSharedShelves(t *testing.T) {
	db := openTestDB(t)
	shared, err := db.CreateShelf("alice", "Club Picks", 25)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetShelfVisibility("alice", shared.ID, ShelfVisibilityShared); err != nil {
		t.Fatal(err)
	}
	if err := db.AddShelfMember("alice", shared.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddBookToShelf(shared.ID, 10); err != nil {
		t.Fatal(err)
	}

	recent, err := db.RecentShelf("bob", 10)
	if err != nil || recent == nil || recent.ID != shared.ID {
		t.Fatalf("RecentShelf for shared member book = %+v, %v; want Club Picks", recent, err)
	}
	if err := db.RemoveBookFromShelf(shared.ID, 10); err != nil {
		t.Fatal(err)
	}
	recent, err = db.RecentShelf("bob", 99)
	if err != nil || recent == nil || recent.ID != shared.ID {
		t.Fatalf("RecentShelf fallback for shared member = %+v, %v; want Club Picks", recent, err)
	}
}

func TestShelfManagersCanManageAnotherUsersMembers(t *testing.T) {
	db := openTestDB(t)
	shelf, err := db.CreateShelf("alice", "Club Picks", 25)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetShelfVisibility("alice", shelf.ID, ShelfVisibilityShared); err != nil {
		t.Fatal(err)
	}
	if err := db.AddShelfMemberForManager(shelf.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	members, err := db.ListShelfMembersForManager(shelf.ID)
	if err != nil || len(members) != 1 || members[0].Username != "bob" {
		t.Fatalf("members after manager add = %+v, %v; want bob", members, err)
	}
	if err := db.RemoveShelfMemberForManager(shelf.ID, "bob"); err != nil {
		t.Fatal(err)
	}
}

func TestShelfBooks_AddRemoveIsOn(t *testing.T) {
	db := openTestDB(t)
	shelfID, err := db.EnsureSystemShelf("alice", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}

	on, err := db.IsBookOnShelf(shelfID, 42)
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Error("expected book not on shelf initially")
	}

	if err := db.AddBookToShelf(shelfID, 42); err != nil {
		t.Fatal(err)
	}
	on, err = db.IsBookOnShelf(shelfID, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Error("expected book on shelf after AddBookToShelf")
	}

	// Adding again is a no-op, not an error.
	if err := db.AddBookToShelf(shelfID, 42); err != nil {
		t.Fatal(err)
	}

	if err := db.RemoveBookFromShelf(shelfID, 42); err != nil {
		t.Fatal(err)
	}
	on, err = db.IsBookOnShelf(shelfID, 42)
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Error("expected book not on shelf after RemoveBookFromShelf")
	}
}

func TestShelfBookIDs(t *testing.T) {
	db := openTestDB(t)
	shelfID, err := db.EnsureSystemShelf("alice", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{1, 2, 3} {
		if err := db.AddBookToShelf(shelfID, id); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := db.ShelfBookIDs(shelfID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || !ids[1] || !ids[2] || !ids[3] {
		t.Errorf("ShelfBookIDs = %v, want {1,2,3}", ids)
	}
}

func TestShelfMembershipsScopesBooksAndOwner(t *testing.T) {
	db := openTestDB(t)
	alice, err := db.EnsureSystemShelf("alice", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureSystemShelf("bob", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddBookToShelf(alice, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.AddBookToShelf(alice, 3); err != nil {
		t.Fatal(err)
	}
	if err := db.AddBookToShelf(bob, 2); err != nil {
		t.Fatal(err)
	}

	memberships, err := db.ShelfMemberships("alice", []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 1 || !memberships[alice][1] || memberships[alice][2] || memberships[alice][3] || memberships[bob] != nil {
		t.Errorf("ShelfMemberships = %#v, want only shelf %d book 1", memberships, alice)
	}
	if memberships, err = db.ShelfMemberships("alice", nil); err != nil || len(memberships) != 0 {
		t.Errorf("ShelfMemberships with no books = %#v, %v; want an empty map", memberships, err)
	}
}

func TestFilter_ShelfID(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "b1.epub"), "Zebra Tales", "Amy Zed")
	writeTestEpub(t, filepath.Join(libDir, "b2.epub"), "Banana Republic", "Bob Young")

	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}

	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 2 {
		t.Fatalf("setup: expected 2 books, got %d", len(books))
	}

	shelfID, err := db.EnsureSystemShelf("alice", "favourites", "Favourites")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddBookToShelf(shelfID, books[0].ID); err != nil {
		t.Fatal(err)
	}

	favorited, err := db.List(SortTitle, false, 1, 10, Filter{ShelfID: shelfID})
	if err != nil {
		t.Fatal(err)
	}
	if len(favorited) != 1 || favorited[0].ID != books[0].ID {
		t.Errorf("Filter{ShelfID} = %+v, want just book %d", favorited, books[0].ID)
	}

	count, err := db.Count(Filter{ShelfID: shelfID})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("Count(Filter{ShelfID}) = %d, want 1", count)
	}
}

func TestFindConnectionBookPrefersPersistedHardcoverID(t *testing.T) {
	libDir := t.TempDir()
	writeTestEpub(t, filepath.Join(libDir, "book.epub"), "Local Title", "Local Author")
	db := openTestDB(t)
	if err := db.Scan([]string{libDir}, nil); err != nil {
		t.Fatal(err)
	}
	books, err := db.List(SortTitle, false, 1, 10, Filter{})
	if err != nil || len(books) != 1 {
		t.Fatalf("setup books = %+v, %v", books, err)
	}
	if err := db.SetHardcoverID(books[0].ID, "12345"); err != nil {
		t.Fatal(err)
	}
	matchedID, err := db.FindConnectionBook("hardcover", "12345", "", "Different Title", "Different Author")
	if err != nil || matchedID != books[0].ID {
		t.Fatalf("hardcover ID match = %d, %v; want %d", matchedID, err, books[0].ID)
	}
	matchedID, err = db.FindConnectionBook("goodreads", "12345", "", "Different Title", "Different Author")
	if err != nil || matchedID != 0 {
		t.Fatalf("non-hardcover ID match = %d, %v; want no match", matchedID, err)
	}
}
