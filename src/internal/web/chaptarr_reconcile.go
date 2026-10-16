package web

import (
	"context"
	"fmt"
	"time"

	"github.com/sethwv/my-sideload-library/internal/chaptarr"
	"github.com/sethwv/my-sideload-library/internal/hardcover"
)

// ReconcileChaptarrMatches revisits prior Chaptarr misses after a successful
// catalog refresh. This lets newly visible provider files replace stale
// no-match outcomes without resetting unrelated Hardcover results.
func (s *Server) ReconcileChaptarrMatches(ctx context.Context) error {
	if !s.Chaptarr.Enabled() {
		return nil
	}
	chBooks, refreshedAt := s.Chaptarr.CachedBooks()
	if len(chBooks) == 0 || refreshedAt.IsZero() || !refreshedAt.Add(chaptarr.DefaultCacheTTL).After(time.Now()) {
		return fmt.Errorf("chaptarr catalog is not current")
	}

	overwriteCover := false
	if settings, err := s.Users.GetIntegrationSettings(); err != nil {
		s.log().Error("chaptarr reconciliation: load integration settings")
	} else {
		overwriteCover = settings.HardcoverOverwriteCover
	}

	for afterID := int64(0); ; {
		candidates, err := s.DB.BooksWithChaptarrNoMatch(afterID, enrichmentBatchSize)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		afterID = candidates[len(candidates)-1].ID

		var knownMatches map[string]hardcover.Match
		var knownDetails map[string]hardcover.Detail
		knownLookedUp := false
		if s.Hardcover.Enabled() {
			knownMatches, knownDetails, knownLookedUp = s.batchChaptarrHardcoverLookups(ctx, candidates, chBooks)
		}
		for _, candidate := range candidates {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			s.processChaptarrMatch(ctx, candidate, chBooks, overwriteCover, knownMatches, knownDetails, knownLookedUp)
		}
	}
}

// ChaptarrReconciliationPending counts prior Chaptarr misses whose paths now
// exist in the current catalog. It performs no network I/O.
func (s *Server) ChaptarrReconciliationPending() (int, error) {
	if !s.Chaptarr.Enabled() {
		return 0, nil
	}
	chBooks, refreshedAt := s.Chaptarr.CachedBooks()
	if len(chBooks) == 0 || refreshedAt.IsZero() || !refreshedAt.Add(chaptarr.DefaultCacheTTL).After(time.Now()) {
		return 0, nil
	}

	pending := 0
	for afterID := int64(0); ; {
		candidates, err := s.DB.BooksWithChaptarrNoMatch(afterID, enrichmentBatchSize)
		if err != nil {
			return 0, err
		}
		if len(candidates) == 0 {
			return pending, nil
		}
		afterID = candidates[len(candidates)-1].ID
		for _, candidate := range candidates {
			if _, ok := chaptarr.MatchByPath(chBooks, candidate.FilePath); ok {
				pending++
			}
		}
	}
}
