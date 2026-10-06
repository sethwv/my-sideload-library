package web

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/sethwv/my-sideload-library/internal/chaptarr"
	"github.com/sethwv/my-sideload-library/internal/hardcover"
	"github.com/sethwv/my-sideload-library/internal/index"
	"github.com/sethwv/my-sideload-library/internal/users"
)

const idlePollInterval = 30 * time.Second

const enrichmentBatchSize = 200

// RunEnrichmentQueue processes candidates in a fixed Chaptarr-then-Hardcover
// order. The Chaptarr snapshot must be current before a candidate can fall
// through to Hardcover.
func (s *Server) RunEnrichmentQueue(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !s.Hardcover.Enabled() && !s.Chaptarr.Enabled() {
			sleepOrDone(ctx, idlePollInterval)
			continue
		}

		candidates, err := s.DB.BooksNeedingEnrichment(enrichmentBatchSize)
		if err != nil {
			log.Printf("enrichment queue: list candidates: %v", err)
			sleepOrDone(ctx, idlePollInterval)
			continue
		}
		connectionCandidates, err := s.Users.ConnectionEnrichmentCandidates(enrichmentBatchSize)
		if err != nil {
			log.Printf("enrichment queue: list connection candidates: %v", err)
		}
		if len(candidates) == 0 && len(connectionCandidates) == 0 {
			sleepOrDone(ctx, idlePollInterval)
			continue
		}

		overwriteCover := false
		if settings, err := s.Users.GetIntegrationSettings(); err != nil {
			log.Printf("enrichment queue: load integration settings: %v", err)
		} else {
			overwriteCover = settings.HardcoverOverwriteCover
		}

		var chBooks []chaptarr.Book
		var chaptarrRefreshedAt time.Time
		if s.Chaptarr.Enabled() {
			chBooks, chaptarrRefreshedAt = s.Chaptarr.CachedBooks()
			if len(chBooks) > 0 {
				s.backfillHardcoverIDsFromChaptarr(chBooks)
			}
			if chaptarrRefreshedAt.IsZero() || !chaptarrRefreshedAt.Add(chaptarr.DefaultCacheTTL).After(time.Now()) {
				// The scheduler owns catalog crawls. A stale snapshot cannot
				// concede candidates to Hardcover before Chaptarr checks them.
				sleepOrDone(ctx, idlePollInterval)
				continue
			}
		}

		processedAny := false
		for _, c := range candidates {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if s.Chaptarr.Enabled() && !c.AddedAt.Before(chaptarrRefreshedAt.Truncate(time.Second)) {
				// This file arrived after the snapshot and waits for its refresh.
				continue
			}
			if s.processChaptarrMatch(ctx, c, chBooks, overwriteCover) {
				processedAny = true
				continue
			}
			if chBooks != nil {
				if err := s.DB.SetChaptarrStatus(c.ID, "no_match"); err != nil {
					log.Printf("enrichment queue: mark chaptarr no_match failed for book %d: %v", c.ID, err)
				}
			}
			if s.processHardcoverMatch(ctx, c, overwriteCover) {
				processedAny = true
			}
		}
		for _, c := range connectionCandidates {
			if s.processConnectionHardcoverMatch(ctx, c) {
				processedAny = true
			}
		}

		if !processedAny {
			sleepOrDone(ctx, idlePollInterval)
		}
	}
}

func (s *Server) processConnectionHardcoverMatch(ctx context.Context, c users.ConnectionEnrichmentCandidate) bool {
	if !s.Hardcover.Enabled() {
		return false
	}
	matches, err := s.Hardcover.Search(ctx, c.Title, c.Author, c.ISBN)
	if err != nil {
		log.Printf("enrichment queue: connection search failed for %s/%s: %v", c.Provider, c.ExternalID, err)
		return s.Users.SetConnectionItemEnrichment(c, "", "", "", "", "error") == nil
	}
	best, ok := hardcover.BestConfidentMatch(matches, c.Title, c.Author)
	if !ok {
		return s.Users.SetConnectionItemEnrichment(c, "", "", "", "", "no_match") == nil
	}
	detail, err := s.Hardcover.Detail(ctx, best.ID)
	if err != nil {
		log.Printf("enrichment queue: connection detail failed for %s/%s: %v", c.Provider, c.ExternalID, err)
	}
	if err := s.Users.SetConnectionItemEnrichment(c, best.ID, best.Title, strings.Join(best.Authors, ", "), detail.Image, "done"); err != nil {
		log.Printf("enrichment queue: save connection enrichment: %v", err)
		return true
	}
	s.promoteEnrichedConnectionMatch(c, best)
	return true
}

// promoteEnrichedConnectionMatch lets a metadata match become a library match
// without waiting for the user to re-import the provider snapshot.
func (s *Server) promoteEnrichedConnectionMatch(c users.ConnectionEnrichmentCandidate, match hardcover.Match) {
	bookID, err := s.DB.FindConnectionBook("hardcover", match.ID, c.ISBN, match.Title, strings.Join(match.Authors, ", "))
	if err != nil {
		log.Printf("enrichment queue: resolve enriched connection match: %v", err)
		return
	}
	if bookID == 0 {
		return
	}
	if err := s.Users.SetConnectionItemMatch(c.Username, c.Provider, c.RemoteShelfKey, c.ExternalID, bookID); err != nil {
		log.Printf("enrichment queue: save enriched connection match: %v", err)
		return
	}
	shelves, err := s.Users.ConnectionShelves(c.Username, c.Provider)
	if err != nil {
		log.Printf("enrichment queue: load connection shelves: %v", err)
		return
	}
	for _, shelf := range shelves {
		if shelf.RemoteKey != c.RemoteShelfKey || !shelf.Selected {
			continue
		}
		items, err := s.Users.ConnectionItems(c.Username, c.Provider, c.RemoteShelfKey)
		if err != nil {
			log.Printf("enrichment queue: load connection items: %v", err)
			return
		}
		bookIDs := make([]int64, 0, len(items))
		for _, item := range items {
			if item.LocalBookID != 0 {
				bookIDs = append(bookIDs, item.LocalBookID)
			}
		}
		if _, err := s.DB.ReplaceIntegrationShelfBooks(c.Username, c.Provider, c.RemoteShelfKey, shelf.Name, bookIDs); err != nil {
			log.Printf("enrichment queue: refresh integration shelf: %v", err)
		}
		return
	}
}

// backfillHardcoverIDsFromChaptarr captures IDs already present in the local
// Chaptarr snapshot. It deliberately performs no Hardcover request.
func (s *Server) backfillHardcoverIDsFromChaptarr(chBooks []chaptarr.Book) {
	books, err := s.DB.BooksMissingHardcoverID(500)
	if err != nil {
		log.Printf("enrichment queue: list missing hardcover ids: %v", err)
		return
	}
	for _, book := range books {
		match, ok := chaptarr.MatchByPath(chBooks, book.FilePath)
		if !ok || match.HardcoverID == "" {
			continue
		}
		if err := s.DB.SetHardcoverID(book.ID, match.HardcoverID); err != nil {
			log.Printf("enrichment queue: backfill hardcover id for book %d: %v", book.ID, err)
		}
	}
}

func (s *Server) processChaptarrMatch(ctx context.Context, c index.EnrichmentCandidate, chBooks []chaptarr.Book, overwriteCover bool) bool {
	if !s.Chaptarr.Enabled() || chBooks == nil {
		return false
	}
	match, ok := chaptarr.MatchByPath(chBooks, c.FilePath)
	if !ok {
		return false
	}

	var hc hardcover.Match
	var hcDetail hardcover.Detail
	if s.Hardcover.Enabled() && match.HardcoverID != "" {
		if m, d, err := s.Hardcover.GetByID(ctx, match.HardcoverID); err != nil {
			log.Printf("enrichment queue: hardcover daisy-chain lookup failed for book %d: %v", c.ID, err)
		} else {
			hc, hcDetail = m, d
		}
	}

	fields := mergeChaptarrFields(match, hc, hcDetail)
	if err := s.DB.ApplyEnrichment(c.ID, fields, index.SourceChaptarr); err != nil {
		log.Printf("enrichment queue: chaptarr apply enrichment failed for book %d: %v", c.ID, err)
		return true
	}
	if match.HardcoverID != "" {
		if err := s.DB.SetHardcoverID(c.ID, match.HardcoverID); err != nil {
			log.Printf("enrichment queue: save hardcover id for book %d: %v", c.ID, err)
		}
	}

	if hcDetail.Image != "" {
		if book, err := s.DB.Get(c.ID); err == nil && book != nil && (overwriteCover || !book.HasCover) {
			if err := s.applyCoverFromURL(ctx, c.ID, hcDetail.Image); err != nil {
				log.Printf("enrichment queue: chaptarr cover fetch failed for book %d: %v", c.ID, err)
			}
		}
	}

	// A duplicate merge can delete c.ID, so it must run after its cover work.
	if fields.ISBN != "" {
		if err := s.DB.MergeDuplicateISBN(c.ID, s.LibraryPaths); err != nil {
			log.Printf("enrichment queue: isbn merge check failed for book %d: %v", c.ID, err)
		}
	}
	return true
}

func (s *Server) processHardcoverMatch(ctx context.Context, c index.EnrichmentCandidate, overwriteCover bool) bool {
	if !s.Hardcover.Enabled() {
		return false
	}

	matches, err := s.Hardcover.Search(ctx, c.Title, c.Author, c.Identifier)
	if err != nil {
		log.Printf("enrichment queue: search failed for book %d: %v", c.ID, err)
		if err := s.DB.SetEnrichmentStatus(c.ID, "error"); err != nil {
			log.Printf("enrichment queue: mark error failed for book %d: %v", c.ID, err)
		}
		if err := s.DB.SetHardcoverStatus(c.ID, "error"); err != nil {
			log.Printf("enrichment queue: mark hardcover error failed for book %d: %v", c.ID, err)
		}
		return true
	}
	best, ok := hardcover.BestConfidentMatch(matches, c.Title, c.Author)
	if !ok {
		if err := s.DB.SetEnrichmentStatus(c.ID, "no_match"); err != nil {
			log.Printf("enrichment queue: mark no_match failed for book %d: %v", c.ID, err)
		}
		if err := s.DB.SetHardcoverStatus(c.ID, "no_match"); err != nil {
			log.Printf("enrichment queue: mark hardcover no_match failed for book %d: %v", c.ID, err)
		}
		return true
	}

	var detail hardcover.Detail
	if d, err := s.Hardcover.Detail(ctx, best.ID); err != nil {
		log.Printf("enrichment queue: detail lookup failed for book %d: %v", c.ID, err)
	} else {
		detail = d
	}

	fields := hardcoverMetadataPatch(best, detail)
	if err := s.DB.ApplyEnrichment(c.ID, fields, index.SourceHardcover); err != nil {
		log.Printf("enrichment queue: apply enrichment failed for book %d: %v", c.ID, err)
		s.DB.SetEnrichmentStatus(c.ID, "error")
		return true
	}
	if err := s.DB.SetHardcoverID(c.ID, best.ID); err != nil {
		log.Printf("enrichment queue: save hardcover id for book %d: %v", c.ID, err)
	}

	if detail.Image != "" {
		if book, err := s.DB.Get(c.ID); err == nil && book != nil && (overwriteCover || !book.HasCover) {
			if err := s.applyCoverFromURL(ctx, c.ID, detail.Image); err != nil {
				log.Printf("enrichment queue: cover fetch failed for book %d: %v", c.ID, err)
			}
		}
	}

	if err := s.DB.SetEnrichmentStatus(c.ID, "done"); err != nil {
		log.Printf("enrichment queue: mark done failed for book %d: %v", c.ID, err)
	}
	if fields.ISBN != "" {
		if err := s.DB.MergeDuplicateISBN(c.ID, s.LibraryPaths); err != nil {
			log.Printf("enrichment queue: isbn merge check failed for book %d: %v", c.ID, err)
		}
	}
	return true
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
