package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/sethwv/my-sideload-library/internal/chaptarr"
	"github.com/sethwv/my-sideload-library/internal/hardcover"
	"github.com/sethwv/my-sideload-library/internal/index"
)

type matchCandidate struct {
	ID          string
	Title       string
	Authors     string
	Series      string
	SeriesIndex string
	ReleaseDate string
	Snippet     string
	Selected    bool
}

// BookEditMetadata renders the current values and provider search results as
// form defaults. Search results are not persisted until the form is saved.
func (s *Server) BookEditMetadata(w http.ResponseWriter, r *http.Request) {
	base, _, err := s.baseData(r)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	book, err := s.DB.Get(id)
	if err != nil {
		http.Error(w, "failed to load book", http.StatusInternalServerError)
		return
	}
	if book == nil {
		http.NotFound(w, r)
		return
	}
	source, err := s.DB.GetEnrichmentSource(id)
	if err != nil {
		s.log().Error("edit-metadata: load source for book")
	}

	mode := r.URL.Query().Get("mode")
	manualQuery := r.URL.Query().Get("q")
	selectedID := r.URL.Query().Get("selected")
	data := map[string]any{
		"Title":           "Edit Metadata",
		"BookID":          id,
		"CurrentTitle":    book.Title,
		"Source":          source,
		"Enabled":         s.Hardcover.Enabled(),
		"ChaptarrEnabled": s.Chaptarr.Enabled(),
		"Mode":            mode,
		"ManualQuery":     manualQuery,
		"FormTitle":       book.Title,
		"FormSeries":      book.Series,
		"FormPublished":   book.PublishedAt,
		"FormDesc":        book.Description,
		"FormGenres":      strings.Join(book.Genres, ", "),
		"FormPublisher":   book.Publisher,
		"FormPages":       formatIntOrBlank(book.Pages),
		"FormISBN":        book.ISBN,
		"FormRating":      formatFloatOrBlank(book.Rating),
		"CoverURL":        "/covers/" + strconv.FormatInt(id, 10),
	}
	if book.SeriesIndex != 0 {
		data["FormSeriesIndex"] = strconv.FormatFloat(book.SeriesIndex, 'f', -1, 64)
	}

	switch {
	case mode == "chaptarr" && s.Chaptarr.Enabled():
		s.runChaptarrSearch(r.Context(), data, id, book.FilePath, selectedID)
	case mode != "" && s.Hardcover.Enabled():
		s.runHardcoverSearch(r.Context(), data, id, book, mode, manualQuery, selectedID)
	}

	mergeInto(data, base)
	render(w, "book_edit_metadata.html", data)
}

func (s *Server) runHardcoverSearch(ctx context.Context, data map[string]any, id int64, book *index.Book, mode, manualQuery, selectedID string) {
	var matches []hardcover.Match
	var searchErr error
	if mode == "check" {
		matches, searchErr = s.Hardcover.Search(ctx, book.Title, book.Author, book.Identifier)
	} else {
		matches, searchErr = s.Hardcover.Search(ctx, manualQuery, "", "")
	}
	if searchErr != nil {
		s.log().Error("edit-metadata search failed for book")
		data["SearchError"] = "Hardcover search failed."
	}

	candidates := make([]matchCandidate, 0, len(matches))
	var selected *hardcover.Match
	for i := range matches {
		m := &matches[i]
		c := matchCandidate{
			ID:          m.ID,
			Title:       m.Title,
			Authors:     strings.Join(m.Authors, ", "),
			Series:      m.Series,
			ReleaseDate: m.ReleaseDate,
			Snippet:     snippet(m.Description, 160),
		}
		if m.SeriesIndex != 0 {
			c.SeriesIndex = strconv.FormatFloat(m.SeriesIndex, 'f', -1, 64)
		}
		if selectedID != "" && m.ID == selectedID {
			c.Selected = true
			selected = m
		}
		candidates = append(candidates, c)
	}
	data["Candidates"] = candidates

	if selected != nil {
		var detail hardcover.Detail
		if d, err := s.Hardcover.Detail(ctx, selected.ID); err != nil {
			s.log().Error("edit-metadata detail lookup failed for book")
		} else {
			detail = d
		}
		applySelectedDefaults(data, selected, detail)
	}
}

func (s *Server) runChaptarrSearch(ctx context.Context, data map[string]any, id int64, filePath, selectedID string) {
	books, _ := s.Chaptarr.CachedBooks()
	if len(books) == 0 {
		data["SearchError"] = "Chaptarr catalog is not available yet."
		return
	}
	match, ok := chaptarr.MatchByPath(books, filePath)
	if !ok {
		data["Candidates"] = []matchCandidate{}
		return
	}

	candidateID := strconv.Itoa(match.ID)
	c := matchCandidate{
		ID:      candidateID,
		Title:   match.Title,
		Authors: strings.Join(match.Authors, ", "),
		Series:  match.Series,
	}
	if match.SeriesIndex != 0 {
		c.SeriesIndex = strconv.FormatFloat(match.SeriesIndex, 'f', -1, 64)
	}
	if selectedID != "" && candidateID == selectedID {
		c.Selected = true
		var hc hardcover.Match
		var hcDetail hardcover.Detail
		if s.Hardcover.Enabled() && match.HardcoverID != "" {
			if m, d, err := s.Hardcover.GetByID(ctx, match.HardcoverID); err != nil {
				s.log().Error("edit-metadata chaptarr daisy-chain lookup failed for book")
			} else {
				hc, hcDetail = m, d
			}
		}
		applyMergedFieldsAsDefaults(data, mergeChaptarrFields(match, hc, hcDetail), hcDetail.Image)
	}
	data["Candidates"] = []matchCandidate{c}
}

func applyMergedFieldsAsDefaults(data map[string]any, f index.MetadataPatch, coverURL string) {
	if f.Title != "" {
		data["FormTitle"] = f.Title
	}
	if f.Series != "" {
		data["FormSeries"] = f.Series
		if f.SeriesIndex != 0 {
			data["FormSeriesIndex"] = strconv.FormatFloat(f.SeriesIndex, 'f', -1, 64)
		}
	}
	if f.PublishedDate != "" {
		data["FormPublished"] = f.PublishedDate
	}
	if f.Description != "" {
		data["FormDesc"] = f.Description
	}
	if len(f.Genres) > 0 {
		data["FormGenres"] = strings.Join(f.Genres, ", ")
	}
	if f.Publisher != "" {
		data["FormPublisher"] = f.Publisher
	}
	if f.Pages != 0 {
		data["FormPages"] = strconv.Itoa(f.Pages)
	}
	if f.ISBN != "" {
		data["FormISBN"] = f.ISBN
	}
	if f.Rating != 0 {
		data["FormRating"] = strconv.FormatFloat(f.Rating, 'f', -1, 64)
	}
	if coverURL != "" {
		data["SelectedCoverURL"] = coverURL
	}
}

func applySelectedDefaults(data map[string]any, m *hardcover.Match, d hardcover.Detail) {
	data["FormTitle"] = m.Title
	if m.Series != "" {
		data["FormSeries"] = m.Series
		if m.SeriesIndex != 0 {
			data["FormSeriesIndex"] = strconv.FormatFloat(m.SeriesIndex, 'f', -1, 64)
		}
	}
	if m.ReleaseDate != "" {
		data["FormPublished"] = m.ReleaseDate
	}
	if m.Description != "" {
		data["FormDesc"] = m.Description
	}
	if len(m.Genres) > 0 {
		data["FormGenres"] = strings.Join(m.Genres, ", ")
	}
	if d.Publisher != "" {
		data["FormPublisher"] = d.Publisher
	}
	if m.Pages != 0 {
		data["FormPages"] = strconv.Itoa(m.Pages)
	}
	if isbn := firstISBN(m.ISBNs); isbn != "" {
		data["FormISBN"] = isbn
	}
	if m.Rating != 0 {
		data["FormRating"] = strconv.FormatFloat(m.Rating, 'f', -1, 64)
	}
	if d.Image != "" {
		data["SelectedCoverURL"] = d.Image
	}
}

func snippet(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func formatIntOrBlank(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func formatFloatOrBlank(f float64) string {
	if f == 0 {
		return ""
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// BookEditMetadataSave persists submitted non-empty fields and, when chosen,
// allows the manually selected provider cover to replace the current cover.
func (s *Server) BookEditMetadataSave(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	fields := index.MetadataPatch{
		Title:         r.FormValue("title"),
		Series:        r.FormValue("series"),
		PublishedDate: r.FormValue("published_date"),
		Description:   r.FormValue("description"),
		Publisher:     r.FormValue("publisher"),
		ISBN:          r.FormValue("isbn"),
	}
	if v := r.FormValue("series_index"); v != "" {
		fields.SeriesIndex, _ = strconv.ParseFloat(v, 64)
	}
	if v := r.FormValue("pages"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			fields.Pages = n
		}
	}
	if v := r.FormValue("rating"); v != "" {
		fields.Rating, _ = strconv.ParseFloat(v, 64)
	}
	if v := strings.TrimSpace(r.FormValue("genres")); v != "" {
		var genres []string
		for _, genre := range strings.Split(v, ",") {
			if genre = strings.TrimSpace(genre); genre != "" {
				genres = append(genres, genre)
			}
		}
		fields.Genres = genres
	}

	if err := s.DB.SaveMetadata(id, fields); err != nil {
		http.Error(w, "failed to save changes", http.StatusInternalServerError)
		return
	}
	if err := s.DB.SetEnrichmentStatus(id, "done"); err != nil {
		s.log().Error("edit-metadata save: mark done failed for book")
	}
	if r.FormValue("cover_action") == "use_hardcover_cover" {
		if coverURL := r.FormValue("hardcover_cover_url"); coverURL != "" {
			if err := s.applyCoverFromURL(r.Context(), id, coverURL); err != nil {
				s.log().Error("edit-metadata save: cover apply failed for book")
			}
		}
	}

	http.Redirect(w, r, "/books/"+strconv.FormatInt(id, 10)+"/edit-metadata", http.StatusSeeOther)
}
