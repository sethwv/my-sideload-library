package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sethwv/my-sideload-library/internal/auth"
	"github.com/sethwv/my-sideload-library/internal/index"
	"github.com/sethwv/my-sideload-library/internal/kepub"
	"github.com/sethwv/my-sideload-library/internal/users"
)

func (s *Server) LibraryGrid(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.renderBookList(w, r, bookListParams{action: "/", filter: index.Filter{Search: strings.TrimSpace(q.Get("q"))}, heading: "Library", defaultSort: index.SortAdded})
}

var validSortParams = map[string]bool{"title": true, "author": true, "series": true, "added": true, "released": true}

func defaultDirFor(sort index.SortKey) string {
	if sort == index.SortAdded || sort == index.SortReleased {
		return "desc"
	}
	return "asc"
}

type bookListParams struct {
	action         string
	name           string
	filter         index.Filter
	heading        string
	defaultSort    index.SortKey
	viewingShelfID int64
	manageShelf    bool
}

func (s *Server) hideMatchFilter() index.Filter {
	settings, err := s.Users.GetIntegrationSettings()
	if err != nil {
		return index.Filter{}
	}
	return index.Filter{HideNoChaptarrMatch: settings.HideNoChaptarrMatch, HideNoHardcoverMatch: settings.HideNoHardcoverMatch}
}

func (s *Server) renderBookList(w http.ResponseWriter, r *http.Request, p bookListParams) {
	q := r.URL.Query()
	sortParam := q.Get("sort")
	if !validSortParams[sortParam] {
		sortParam = string(p.defaultSort)
	}
	sort := index.SortKey(sortParam)
	dir := q.Get("dir")
	if dir != "asc" && dir != "desc" {
		dir = defaultDirFor(sort)
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize := s.PageSize
	if pageSize < 1 {
		pageSize = 48
	}
	search := strings.TrimSpace(q.Get("q"))
	filter := p.filter
	filter.Search = search
	hide := s.hideMatchFilter()
	filter.HideNoChaptarrMatch = hide.HideNoChaptarrMatch
	filter.HideNoHardcoverMatch = hide.HideNoHardcoverMatch
	total, err := s.DB.Count(filter)
	if err != nil {
		http.Error(w, "failed to load library", http.StatusInternalServerError)
		return
	}
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	pages := make([]int, totalPages)
	for i := range pages {
		pages[i] = i + 1
	}
	descending := dir == "desc"
	books, err := s.DB.List(sort, descending, page, pageSize, filter)
	if err != nil {
		http.Error(w, "failed to load library", http.StatusInternalServerError)
		return
	}
	base, shelves, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load shelves", http.StatusInternalServerError)
		return
	}
	downloadFormat := ""
	if p.viewingShelfID != 0 {
		downloadFormat = "epub"
		if base["KepubEnabled"].(bool) {
			downloadFormat = "kepub"
		}
		switch q.Get("download") {
		case "epub":
			downloadFormat = "epub"
		case "kepub":
			// Keep the enabled KEPUB default when it is available.
		}
	}
	var shelfDownloadBooks []index.Book
	if downloadFormat != "" {
		shelfDownloadBooks = books
	}
	bookIDs := make([]int64, len(books))
	for i, book := range books {
		bookIDs[i] = book.ID
	}
	username, _ := auth.UsernameFromContext(r.Context())
	memberships, err := s.DB.ShelfMemberships(username, bookIDs)
	if err != nil {
		http.Error(w, "failed to load shelves", http.StatusInternalServerError)
		return
	}
	var favoritesShelfID int64
	for _, shelf := range shelves {
		if shelf.IsSystem {
			favoritesShelfID = shelf.ID
		}
	}
	recentShelves := make(map[int64]*index.Shelf)
	if len(shelves) > 1 {
		for _, book := range books {
			shelf, err := s.DB.RecentShelf(username, book.ID)
			if err != nil {
				http.Error(w, "failed to load shelves", http.StatusInternalServerError)
				return
			}
			recentShelves[book.ID] = shelf
		}
	}
	locations, err := s.DB.LocationsForBooks(bookIDs)
	if err != nil {
		http.Error(w, "failed to load library", http.StatusInternalServerError)
		return
	}
	for _, book := range books {
		locations[book.ID] = append([]index.Location{{LibraryRoot: book.LibraryRoot, FilePath: book.FilePath}}, locations[book.ID]...)
	}
	toggleDir := "desc"
	if descending {
		toggleDir = "asc"
	}
	data := map[string]any{"Title": p.heading, "Heading": p.heading, "Books": books, "Sort": sortParam, "Dir": dir, "ToggleDir": toggleDir, "Page": page, "PrevPage": page - 1, "NextPage": page + 1, "HasNext": page < totalPages, "TotalPages": totalPages, "Pages": pages, "Query": search, "Action": p.action, "Name": p.name, "ShelfMemberships": memberships, "ViewingShelfID": p.viewingShelfID, "ManageViewingShelf": p.manageShelf, "Locations": locations, "FavoritesShelfID": favoritesShelfID, "EditableShelfCount": len(shelves), "RecentShelves": recentShelves, "ShelfDownloadFormat": downloadFormat, "ShelfDownloadBooks": shelfDownloadBooks}
	mergeInto(data, base)
	render(w, "library.html", data)
}

func (s *Server) AuthorsHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		s.renderNameIndex(w, r, "Authors", "/authors", func() ([]index.NameCount, error) { return s.DB.ListAuthors(s.hideMatchFilter()) })
		return
	}
	s.renderBookList(w, r, bookListParams{action: "/authors", name: name, filter: index.Filter{Author: name}, heading: "Books by " + name, defaultSort: index.SortTitle})
}

func (s *Server) SeriesHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		s.renderNameIndex(w, r, "Series", "/series", func() ([]index.NameCount, error) { return s.DB.ListSeries(s.hideMatchFilter()) })
		return
	}
	s.renderBookList(w, r, bookListParams{action: "/series", name: name, filter: index.Filter{Series: name}, heading: "Series: " + name, defaultSort: index.SortSeries})
}

func (s *Server) FavoritesHandler(w http.ResponseWriter, r *http.Request) {
	username, _ := auth.UsernameFromContext(r.Context())
	shelfID, err := s.DB.EnsureSystemShelf(username, favoritesSlug, favoritesName)
	if err != nil {
		http.Error(w, "failed to load favourites", http.StatusInternalServerError)
		return
	}
	s.renderBookList(w, r, bookListParams{action: "/favorites", filter: index.Filter{ShelfID: shelfID}, heading: favoritesName, defaultSort: index.SortTitle, viewingShelfID: shelfID})
}

func (s *Server) ShelfHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	shelf, err := s.DB.GetVisibleShelf(username, id)
	if err != nil {
		http.Error(w, "failed to load shelf", http.StatusInternalServerError)
		return
	}
	if shelf == nil && s.Users.Can(username, users.PermissionManageShelves) && !auth.IsRestricted(r.Context()) {
		managed, err := s.DB.GetShelf(id)
		if err != nil {
			http.Error(w, "failed to load shelf", http.StatusInternalServerError)
			return
		}
		if managed != nil && !managed.IsIntegration() {
			shelf = &index.ShelfAccess{Shelf: *managed, Role: "manager"}
		}
	}
	if shelf == nil {
		http.NotFound(w, r)
		return
	}
	if shelf.IsIntegration() {
		s.renderConnectedShelf(w, r, shelf)
		return
	}
	manageShelf := shelf.Role == "owner" || (s.Users.Can(username, users.PermissionManageShelves) && !auth.IsRestricted(r.Context()))
	s.renderBookList(w, r, bookListParams{action: "/shelves/" + strconv.FormatInt(id, 10), filter: index.Filter{ShelfID: id}, heading: shelf.Name, defaultSort: index.SortTitle, viewingShelfID: id, manageShelf: manageShelf})
}

type connectedShelfItem struct {
	Title, Author, CoverURL string
	Book                    *index.Book
}

func (s *Server) renderConnectedShelf(w http.ResponseWriter, r *http.Request, shelf *index.ShelfAccess) {
	username, _ := auth.UsernameFromContext(r.Context())
	items, err := s.Users.ConnectionItems(username, shelf.Provider, shelf.RemoteKey)
	if err != nil {
		http.Error(w, "failed to load connected shelf", http.StatusInternalServerError)
		return
	}
	view := make([]connectedShelfItem, 0, len(items))
	for _, item := range items {
		entry := connectedShelfItem{Title: item.Title, Author: item.Author}
		if item.LocalBookID != 0 {
			entry.Book, _ = s.DB.Get(item.LocalBookID)
		} else {
			if item.EnrichedTitle != "" {
				entry.Title = item.EnrichedTitle
			}
			if item.EnrichedAuthor != "" {
				entry.Author = item.EnrichedAuthor
			}
			entry.CoverURL = item.CoverURL
		}
		view = append(view, entry)
	}
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	hasDownloads := false
	for _, item := range view {
		if item.Book != nil {
			hasDownloads = true
			break
		}
	}
	data := map[string]any{"Title": shelf.Name, "Shelf": shelf, "Items": view, "HasDownloads": hasDownloads}
	mergeInto(data, base)
	render(w, "connected_shelf.html", data)
}

func (s *Server) ShelfToggle(w http.ResponseWriter, r *http.Request) {
	bookID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	shelfID, err := strconv.ParseInt(r.PathValue("shelfID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username, _ := auth.UsernameFromContext(r.Context())
	shelf, err := s.DB.GetEditableShelf(username, shelfID)
	if err != nil {
		http.Error(w, "failed to update shelf", http.StatusInternalServerError)
		return
	}
	if shelf == nil {
		http.NotFound(w, r)
		return
	}
	onShelf, err := s.DB.IsBookOnShelf(shelfID, bookID)
	if err == nil && onShelf {
		err = s.DB.RemoveBookFromShelf(shelfID, bookID)
	} else if err == nil {
		err = s.DB.AddBookToShelf(shelfID, bookID)
	}
	if err != nil {
		http.Error(w, "failed to update shelf", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("Accept") == "application/json" {
		recent, err := s.DB.RecentShelf(username, bookID)
		if err != nil {
			http.Error(w, "failed to load shelves", http.StatusInternalServerError)
			return
		}
		var recentState *struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			OnShelf bool   `json:"onShelf"`
		}
		if recent != nil {
			recentOnShelf, err := s.DB.IsBookOnShelf(recent.ID, bookID)
			if err != nil {
				http.Error(w, "failed to load shelves", http.StatusInternalServerError)
				return
			}
			recentState = &struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				OnShelf bool   `json:"onShelf"`
			}{ID: recent.ID, Name: recent.Name, OnShelf: recentOnShelf}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			OnShelf bool `json:"onShelf"`
			Recent  any  `json:"recent"`
		}{OnShelf: !onShelf, Recent: recentState})
		return
	}
	next := safeNext(r.FormValue("next"))
	target, err := url.Parse(next)
	if err != nil || target.Scheme != "" || target.Hostname() != "" || target.User != nil || !strings.HasPrefix(target.Path, "/") || strings.HasPrefix(target.Path, "//") {
		target = &url.URL{Path: "/"}
	}
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (s *Server) renderNameIndex(w http.ResponseWriter, r *http.Request, heading, linkBase string, list func() ([]index.NameCount, error)) {
	items, err := list()
	if err != nil {
		http.Error(w, "failed to load list", http.StatusInternalServerError)
		return
	}
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Title": heading, "Heading": heading, "Items": items, "LinkBase": linkBase}
	mergeInto(data, base)
	render(w, "name_index.html", data)
}

func (s *Server) Cover(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	book, err := s.DB.Get(id)
	if err != nil || book == nil || !book.HasCover || book.CoverPath == "" {
		http.Redirect(w, r, "/static/placeholder-cover.svg", http.StatusFound)
		return
	}
	http.ServeFile(w, r, s.Covers.Path(book.CoverPath))
}

func (s *Server) DownloadEPUB(w http.ResponseWriter, r *http.Request) {
	book, ok := s.lookupBook(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filenameFor(book.Title, book.Author, "epub")))
	http.ServeFile(w, r, filepath.Join(book.LibraryRoot, book.FilePath))
}

func (s *Server) DownloadKepub(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Users.GetKepubSettings()
	if err != nil {
		http.Error(w, "failed to load KEPUB settings", http.StatusInternalServerError)
		return
	}
	if !settings.Enabled {
		http.NotFound(w, r)
		return
	}
	book, ok := s.lookupBook(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/epub+zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filenameFor(book.Title, book.Author, "kepub.epub")))
	var metadata *kepub.Metadata
	if settings.WriteCalibreMetadata {
		metadata = &kepub.Metadata{Series: book.Series, SeriesIndex: book.SeriesIndex}
	}
	if err := kepub.ConvertFile(r.Context(), w, filepath.Join(book.LibraryRoot, book.FilePath), metadata); err != nil {
		log.Printf("kepub conversion failed for %s: %v", book.FilePath, err)
	}
}

func (s *Server) lookupBook(w http.ResponseWriter, r *http.Request) (*index.Book, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	book, err := s.DB.Get(id)
	if err != nil || book == nil {
		http.NotFound(w, r)
		return nil, false
	}
	return book, true
}

func filenameFor(title, author, ext string) string {
	name := title
	if author != "" {
		name = author + " - " + title
	}
	return sanitizeFilename(name) + "." + ext
}

var filenameReplacer = strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\"", "'", "<", "(", ">", ")", "|", "-", "?", "", "*", "")

func sanitizeFilename(name string) string { return filenameReplacer.Replace(name) }
