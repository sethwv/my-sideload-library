package web

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sethwv/my-sideload-library/internal/chaptarr"
	"github.com/sethwv/my-sideload-library/internal/connections"
	"github.com/sethwv/my-sideload-library/internal/hardcover"
	"github.com/sethwv/my-sideload-library/internal/index"
	"github.com/sethwv/my-sideload-library/internal/users"
)

const idlePollInterval = 30 * time.Second

const enrichmentBatchSize = 200

const enrichmentWorkers = 4

// RunEnrichmentQueue gives fresh Chaptarr path matches and provider-connected
// items priority, then uses general Hardcover matching as fallback work.
func (s *Server) RunEnrichmentQueue(ctx context.Context) {
	lastState := ""
	logState := func(state string) {
		s.setEnrichmentQueueState(state)
		if state != lastState {
			log.Printf("enrichment queue: %s", state)
			lastState = state
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Stored provider metadata can resolve against the local library without
		// an active external integration, so always retry those matches first.
		if promoted, err := s.promoteStoredConnectionMatches(); err != nil {
			log.Printf("enrichment queue: promote stored connection matches: %v", err)
		} else if promoted > 0 {
			log.Printf("enrichment queue: promoted %d stored connection matches", promoted)
		}
		if !s.Hardcover.Enabled() && !s.Chaptarr.Enabled() {
			logState("idle, no integrations are enabled")
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
		coverCandidates, err := s.Users.ConnectionCoverCacheCandidates(enrichmentBatchSize)
		if err != nil {
			log.Printf("enrichment queue: list connection cover cache candidates: %v", err)
		}
		if len(candidates) == 0 && len(connectionCandidates) == 0 && len(coverCandidates) == 0 {
			logState("idle, no eligible candidates")
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
		var knownHardcoverMatches map[string]hardcover.Match
		var knownHardcoverDetails map[string]hardcover.Detail
		knownHardcoverLookedUp := false
		if s.Chaptarr.Enabled() {
			chBooks, chaptarrRefreshedAt = s.Chaptarr.CachedBooks()
			if len(chBooks) > 0 {
				s.backfillHardcoverIDsFromChaptarr(chBooks)
			}
			if chaptarrRefreshedAt.IsZero() || !chaptarrRefreshedAt.Add(chaptarr.DefaultCacheTTL).After(time.Now()) {
				// A stale catalog only disables path matching. It must not block
				// connected items or otherwise eligible Hardcover work.
				chBooks = nil
			}
		}
		if s.Hardcover.Enabled() && chBooks != nil {
			knownHardcoverMatches, knownHardcoverDetails, knownHardcoverLookedUp = s.batchChaptarrHardcoverLookups(ctx, candidates, chBooks)
		}
		logState(fmt.Sprintf("processing %d library, %d connection, and %d cover cache candidates (hardcover=%t chaptarr_cache=%t)", len(candidates), len(connectionCandidates), len(coverCandidates), s.Hardcover.Enabled(), chBooks != nil))

		processedAny := false
		if runEnrichmentWorkers(ctx, coverCandidates, func(ctx context.Context, c users.ConnectionEnrichmentCandidate) bool {
			if err := s.cacheConnectionCover(ctx, c, c.CoverURL); err != nil {
				log.Printf("enrichment queue: cache connection cover for %s/%s: %v", c.Provider, c.ExternalID, err)
				s.markConnectionCoverCacheFailure(c, err)
				return false
			}
			return true
		}) {
			processedAny = true
		}
		// Handle deterministic Chaptarr paths before generic matching.
		for _, c := range candidates {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if chBooks == nil || !c.AddedAt.Before(chaptarrRefreshedAt.Truncate(time.Second)) {
				// This file arrived after the snapshot and waits for its refresh.
				continue
			}
			if s.processChaptarrMatch(ctx, c, chBooks, overwriteCover, knownHardcoverMatches, knownHardcoverDetails, knownHardcoverLookedUp) {
				processedAny = true
			} else {
				if err := s.DB.SetChaptarrStatus(c.ID, "no_match"); err != nil {
					log.Printf("enrichment queue: mark chaptarr no_match failed for book %d: %v", c.ID, err)
				}
			}
		}
		// Finish local-library work before connected ghosts so enhancements for
		// EPUB-backed books are always prioritized.
		hardcoverCandidates := make([]index.EnrichmentCandidate, 0, len(candidates))
		for _, c := range candidates {
			if chBooks != nil && c.AddedAt.Before(chaptarrRefreshedAt.Truncate(time.Second)) {
				if _, matched := chaptarr.MatchByPath(chBooks, c.FilePath); matched {
					continue
				}
			}
			hardcoverCandidates = append(hardcoverCandidates, c)
		}
		if runEnrichmentWorkers(ctx, hardcoverCandidates, func(ctx context.Context, c index.EnrichmentCandidate) bool {
			return s.processHardcoverMatch(ctx, c, overwriteCover)
		}) {
			processedAny = true
		}
		if runEnrichmentWorkers(ctx, connectionCandidates, s.processConnectionHardcoverMatch) {
			processedAny = true
		}

		if !processedAny {
			sleepOrDone(ctx, idlePollInterval)
		}
	}
}

// runEnrichmentWorkers overlaps request and thumbnail latency while the
// Hardcover client remains the single authority for provider rate limiting.
func runEnrichmentWorkers[T any](ctx context.Context, candidates []T, process func(context.Context, T) bool) bool {
	if len(candidates) == 0 {
		return false
	}
	jobs := make(chan T)
	var processed atomic.Bool
	var workers sync.WaitGroup
	for range min(enrichmentWorkers, len(candidates)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				if process(ctx, candidate) {
					processed.Store(true)
				}
			}
		}()
	}
	for _, candidate := range candidates {
		select {
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return processed.Load()
		case jobs <- candidate:
		}
	}
	close(jobs)
	workers.Wait()
	return processed.Load()
}

func (s *Server) processConnectionHardcoverMatch(ctx context.Context, c users.ConnectionEnrichmentCandidate) bool {
	if !s.Hardcover.Enabled() {
		return false
	}
	title := connectionDisplayTitle(c.Title)
	matches, err := s.Hardcover.Search(ctx, title, c.Author, c.ISBN)
	if err != nil {
		log.Printf("enrichment queue: connection search failed for %s/%s: %v", c.Provider, c.ExternalID, err)
		if hardcover.Retryable(err) {
			return s.Users.SetConnectionItemEnrichment(c, "", "", "", "", "error") == nil
		}
		return s.Users.SetConnectionItemEnrichment(c, "", "", "", "", "error") == nil
	}
	best, ok := connectionHardcoverMatch(matches, c.ISBN, title, c.Author)
	if !ok {
		return s.Users.SetConnectionItemEnrichment(c, "", "", "", "", "no_match") == nil
	}
	detail, err := s.Hardcover.Detail(ctx, best.ID)
	if err != nil {
		log.Printf("enrichment queue: connection detail failed for %s/%s: %v", c.Provider, c.ExternalID, err)
		if hardcover.Retryable(err) {
			return s.Users.SetConnectionItemEnrichment(c, "", "", "", "", "error") == nil
		}
	}
	if err := s.Users.SetConnectionItemEnrichment(c, best.ID, best.Title, strings.Join(best.Authors, ", "), detail.Image, "done"); err != nil {
		log.Printf("enrichment queue: save connection enrichment: %v", err)
		return true
	}
	if detail.Image != "" {
		if err := s.cacheConnectionCover(ctx, c, detail.Image); err != nil {
			log.Printf("enrichment queue: cache connection cover for %s/%s: %v", c.Provider, c.ExternalID, err)
			s.markConnectionCoverCacheFailure(c, err)
		}
	}
	s.promoteEnrichedConnectionMatch(c, best)
	return true
}

func (s *Server) markConnectionCoverCacheFailure(c users.ConnectionEnrichmentCandidate, err error) {
	if !errors.Is(err, image.ErrFormat) {
		return
	}
	if saveErr := s.Users.SetConnectionItemCoverCacheFailed(c); saveErr != nil {
		log.Printf("enrichment queue: mark uncached connection cover for %s/%s: %v", c.Provider, c.ExternalID, saveErr)
	}
}

func (s *Server) cacheConnectionCover(ctx context.Context, c users.ConnectionEnrichmentCandidate, rawURL string) error {
	data, mediaType, err := fetchCover(ctx, rawURL)
	if err != nil {
		return err
	}
	path, err := s.Covers.SaveConnectionCover(c.Username+"\x00"+c.Provider+"\x00"+c.RemoteShelfKey+"\x00"+c.ExternalID, data, mediaType)
	if err != nil {
		return err
	}
	return s.Users.SetConnectionItemCoverPath(c, path)
}

// batchChaptarrHardcoverLookups resolves IDs supplied by Chaptarr together,
// avoiding one Hardcover request per candidate when the IDs are already known.
func (s *Server) batchChaptarrHardcoverLookups(ctx context.Context, candidates []index.EnrichmentCandidate, chBooks []chaptarr.Book) (map[string]hardcover.Match, map[string]hardcover.Detail, bool) {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		match, ok := chaptarr.MatchByPath(chBooks, candidate.FilePath)
		if ok && match.HardcoverID != "" {
			ids = append(ids, match.HardcoverID)
		}
	}
	if len(ids) == 0 {
		return nil, nil, true
	}
	matches, details, err := s.Hardcover.GetByIDs(ctx, ids)
	if err != nil {
		log.Printf("enrichment queue: batch hardcover lookup failed: %v", err)
		return nil, nil, false
	}
	return matches, details, true
}

func connectionHardcoverMatch(matches []hardcover.Match, isbn, title, author string) (hardcover.Match, bool) {
	isbn = normalizeConnectionISBN(isbn)
	if isbn != "" {
		for _, match := range matches {
			for _, candidateISBN := range match.ISBNs {
				if normalizeConnectionISBN(candidateISBN) == isbn {
					return match, true
				}
			}
		}
	}
	return hardcover.BestConfidentMatch(matches, title, author)
}

func normalizeConnectionISBN(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer("-", "", " ", "").Replace(value)
}

// promoteEnrichedConnectionMatch lets a metadata match become a library match
// without waiting for the user to re-import the provider snapshot.
func (s *Server) promoteEnrichedConnectionMatch(c users.ConnectionEnrichmentCandidate, match hardcover.Match) {
	s.promoteConnectionMatch(c, match.ID, match.Title, strings.Join(match.Authors, ", "))
}

func (s *Server) promoteStoredConnectionMatches() (int, error) {
	candidates, err := s.Users.ConnectionPromotionCandidates(enrichmentBatchSize)
	if err != nil {
		return 0, err
	}
	promoted := 0
	for _, c := range candidates {
		title, author := c.EnrichedTitle, c.EnrichedAuthor
		if title == "" {
			title = connectionDisplayTitle(c.Title)
		}
		if author == "" {
			author = c.Author
		}
		if s.promoteConnectionMatch(c, c.HardcoverID, title, author) {
			promoted++
		}
	}
	return promoted, nil
}

func (s *Server) promoteConnectionMatch(c users.ConnectionEnrichmentCandidate, hardcoverID, title, author string) bool {
	bookID, err := s.DB.FindConnectionBook("hardcover", hardcoverID, c.ISBN, title, author)
	if err != nil {
		log.Printf("enrichment queue: resolve enriched connection match: %v", err)
		return false
	}
	if bookID == 0 {
		return false
	}
	if err := s.Users.SetConnectionItemMatch(c.Username, c.Provider, c.RemoteShelfKey, c.ExternalID, bookID); err != nil {
		log.Printf("enrichment queue: save enriched connection match: %v", err)
		return false
	}
	shelves, err := s.Users.ConnectionShelves(c.Username, c.Provider)
	if err != nil {
		log.Printf("enrichment queue: load connection shelves: %v", err)
		return false
	}
	for _, shelf := range shelves {
		if shelf.RemoteKey != c.RemoteShelfKey || !shelf.Selected {
			continue
		}
		items, err := s.Users.ConnectionItems(c.Username, c.Provider, c.RemoteShelfKey)
		if err != nil {
			log.Printf("enrichment queue: load connection items: %v", err)
			return false
		}
		bookIDs := make([]int64, 0, len(items))
		for _, item := range items {
			if item.LocalBookID != 0 {
				bookIDs = append(bookIDs, item.LocalBookID)
			}
		}
		if _, err := s.DB.ReplaceIntegrationShelfBooks(c.Username, c.Provider, c.RemoteShelfKey, connections.IntegrationShelfName(c.Provider, shelf.Name), bookIDs); err != nil {
			log.Printf("enrichment queue: refresh integration shelf: %v", err)
		}
		return true
	}
	return true
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

func (s *Server) processChaptarrMatch(ctx context.Context, c index.EnrichmentCandidate, chBooks []chaptarr.Book, overwriteCover bool, knownHardcoverMatches map[string]hardcover.Match, knownHardcoverDetails map[string]hardcover.Detail, knownHardcoverLookedUp bool) bool {
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
		if knownHardcoverLookedUp {
			hc = knownHardcoverMatches[match.HardcoverID]
			hcDetail = knownHardcoverDetails[match.HardcoverID]
		} else if m, d, err := s.Hardcover.GetByID(ctx, match.HardcoverID); err != nil {
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
		if hardcover.Retryable(err) {
			if err := s.DB.SetEnrichmentRetry(c.ID); err != nil {
				log.Printf("enrichment queue: defer retry failed for book %d: %v", c.ID, err)
			}
			return true
		}
		if err := s.DB.SetEnrichmentRetry(c.ID); err != nil {
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
		if hardcover.Retryable(err) {
			if err := s.DB.SetEnrichmentRetry(c.ID); err != nil {
				log.Printf("enrichment queue: defer retry failed for book %d: %v", c.ID, err)
			}
			return true
		}
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
