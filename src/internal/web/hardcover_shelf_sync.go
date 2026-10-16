package web

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/sethwv/my-sideload-library/internal/connections"
)

const hardcoverSyncInterval = 24 * time.Hour
const hardcoverSyncRetryDelay = time.Hour

func (s *Server) hardcoverSource() connections.Source {
	if s.HardcoverSource != nil {
		return s.HardcoverSource
	}
	return connections.NewHardcoverSource(nil)
}

// RunHardcoverShelfSync synchronizes every currently due personal connection.
// Individual failures are persisted per account and do not stop the pass.
func (s *Server) RunHardcoverShelfSync(ctx context.Context) error {
	due, err := s.Users.DueHardcoverConnections(time.Now().Unix())
	if err != nil {
		return fmt.Errorf("load due Hardcover connections: %w", err)
	}
	failures := 0
	for _, connection := range due {
		if err := s.syncHardcoverConnection(ctx, connection.Username, connection.Secret); err != nil {
			failures++
			log.Printf("hardcover shelf sync for %s: %v", connection.Username, err)
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d Hardcover connection(s) failed to sync", failures)
	}
	return nil
}

func (s *Server) syncHardcoverConnection(ctx context.Context, username, secret string) error {
	snapshot, err := s.hardcoverSource().Snapshot(ctx, secret)
	if err != nil {
		message := safeHardcoverSyncError(err)
		_ = s.Users.SetConnectionSyncResult(username, "hardcover", false, time.Now().Add(hardcoverSyncRetryDelay).Unix(), message)
		return err
	}
	if err := connections.Reconcile(username, "hardcover", snapshot, s.Users, s.DB); err != nil {
		_ = s.Users.SetConnectionSyncResult(username, "hardcover", false, time.Now().Add(hardcoverSyncRetryDelay).Unix(), "Could not update your Hardcover shelves. Try again later.")
		return err
	}
	return s.Users.SetConnectionSyncResult(username, "hardcover", true, time.Now().Add(hardcoverSyncInterval).Unix(), "")
}

func safeHardcoverSyncError(err error) string {
	message := err.Error()
	if strings.Contains(strings.ToLower(message), "token") {
		return "Could not sync Hardcover. Check the API token and try again."
	}
	if len(message) > 500 {
		message = message[:500]
	}
	if message != "" {
		return message
	}
	return "Could not sync Hardcover. Try again later."
}

// RunHardcoverShelfSyncScheduler schedules the shared task from persisted
// connection state, so restarts cannot cause duplicate daily runs.
func (s *Server) RunHardcoverShelfSyncScheduler(ctx context.Context) {
	for {
		next, err := s.Users.NextHardcoverSyncAt()
		if err != nil {
			log.Printf("hardcover shelf sync scheduler: next due: %v", err)
			sleepOrDone(ctx, idlePollInterval)
			continue
		}
		now := time.Now()
		if next != 0 && next <= now.Unix() {
			if s.Tasks != nil {
				if _, _, err := s.Tasks.Enqueue("hardcover-shelf-sync"); err != nil {
					log.Printf("hardcover shelf sync scheduler: queue sync: %v", err)
				}
			}
			sleepOrDone(ctx, idlePollInterval)
			continue
		}
		if next == 0 {
			sleepOrDone(ctx, idlePollInterval)
			continue
		}
		sleepOrDone(ctx, time.Until(time.Unix(next, 0)))
	}
}
