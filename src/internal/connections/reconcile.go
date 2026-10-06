package connections

import (
	"github.com/sethwv/my-sideload-library/internal/index"
	"github.com/sethwv/my-sideload-library/internal/users"
)

// Reconcile persists a successful source snapshot, retains unmatched items as
// ghosts, and replaces memberships only for shelves the user selected.
func Reconcile(username, provider string, snapshot Snapshot, store *users.Store, db *index.DB) error {
	previous, err := store.ConnectionShelves(username, provider)
	if err != nil {
		return err
	}
	shelves := make([]users.ConnectionShelf, len(snapshot.Shelves))
	present := make(map[string]bool, len(snapshot.Shelves))
	for i, shelf := range snapshot.Shelves {
		shelves[i] = users.ConnectionShelf{RemoteKey: shelf.Key, Name: shelf.Name}
		present[shelf.Key] = true
	}
	items := make([]users.ConnectionItem, len(snapshot.Items))
	for i, item := range snapshot.Items {
		items[i] = users.ConnectionItem{RemoteShelfKey: item.ShelfKey, ExternalID: item.ExternalID, Title: item.Title, Author: item.Author, ISBN: item.ISBN, AddedAt: item.AddedAt, SourcePosition: item.Position}
	}
	if err := store.ReplaceConnectionSnapshot(username, provider, shelves, items); err != nil {
		return err
	}
	for _, shelf := range previous {
		if shelf.Selected && !present[shelf.RemoteKey] {
			if err := db.DeleteIntegrationShelf(username, provider, shelf.RemoteKey); err != nil {
				return err
			}
		}
	}
	selected, err := store.ConnectionShelves(username, provider)
	if err != nil {
		return err
	}
	for _, shelf := range selected {
		if !shelf.Selected {
			continue
		}
		shelfItems, err := store.ConnectionItems(username, provider, shelf.RemoteKey)
		if err != nil {
			return err
		}
		bookIDs := make([]int64, 0, len(shelfItems))
		for _, item := range shelfItems {
			bookID, err := db.FindConnectionBook(provider, item.ExternalID, item.ISBN, item.Title, item.Author)
			if err != nil {
				return err
			}
			if err := store.SetConnectionItemMatch(username, provider, shelf.RemoteKey, item.ExternalID, bookID); err != nil {
				return err
			}
			if bookID != 0 {
				bookIDs = append(bookIDs, bookID)
			}
		}
		if _, err := db.ReplaceIntegrationShelfBooks(username, provider, shelf.RemoteKey, shelf.Name, bookIDs); err != nil {
			return err
		}
	}
	return nil
}
