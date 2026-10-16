package web

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/sethwv/my-sideload-library/internal/index"
	"github.com/sethwv/my-sideload-library/internal/mail"
)

// digestInterval is how often the weekly new-book digest goes out.
const (
	digestInterval    = 7 * 24 * time.Hour
	lastDigestMetaKey = "last_digest_sent_at"
)

// RunDigestScheduler periodically emails every digest-subscribed user a
// list of books added to the library since the last digest, once SMTP is
// configured. Mirrors RunEnrichmentQueue's shape: a coarse poll loop with
// progress tracked in the DB (the meta table), not in memory, so a restart
// doesn't cause a duplicate or missed send.
func (s *Server) RunDigestScheduler(ctx context.Context) {
	if v, ok, _ := s.DB.GetMeta(lastDigestMetaKey); !ok || v == "" {
		// First run ever: start the clock now rather than digesting the
		// entire existing library on the very first send.
		if err := s.DB.SetMeta(lastDigestMetaKey, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
			log.Printf("digest scheduler: init last-sent marker: %v", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if s.smtpAvailable() && s.digestDue() {
			if s.Tasks == nil {
				s.maybeSendDigest()
			} else if _, _, err := s.Tasks.Enqueue("email-digest"); err != nil {
				log.Printf("digest scheduler: queue digest: %v", err)
			}
		}
		sleepOrDone(ctx, s.nextDigestDelay())
	}
}

func (s *Server) digestDue() bool {
	v, ok, err := s.DB.GetMeta(lastDigestMetaKey)
	if err != nil || !ok {
		return false
	}
	lastSent, err := strconv.ParseInt(v, 10, 64)
	return err == nil && time.Until(time.Unix(lastSent, 0).Add(digestInterval)) <= 0
}

func (s *Server) nextDigestDelay() time.Duration {
	v, ok, err := s.DB.GetMeta(lastDigestMetaKey)
	if err != nil || !ok {
		return digestInterval
	}
	lastSent, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return digestInterval
	}
	delay := time.Until(time.Unix(lastSent, 0).Add(digestInterval))
	if delay < 0 {
		return time.Minute
	}
	return delay
}

// SendDigestNow runs the scheduled digest immediately, for the admin task.
func (s *Server) SendDigestNow() error {
	if !s.smtpAvailable() {
		return nil
	}
	s.maybeSendDigest(true)
	return nil
}

func (s *Server) maybeSendDigest(force ...bool) {
	v, ok, err := s.DB.GetMeta(lastDigestMetaKey)
	if err != nil {
		log.Printf("digest scheduler: read last-sent marker: %v", err)
		return
	}
	var lastSent int64
	if ok {
		lastSent, _ = strconv.ParseInt(v, 10, 64)
	}
	if len(force) == 0 && time.Since(time.Unix(lastSent, 0)) < digestInterval {
		return
	}

	settings, err := s.Users.GetSMTPSettings()
	if err != nil {
		log.Printf("digest scheduler: load smtp settings: %v", err)
		return
	}
	if !settings.Enabled() {
		return
	}

	books, err := s.DB.List(index.SortAdded, true, 1, 500, index.Filter{AddedAfter: lastSent})
	if err != nil {
		log.Printf("digest scheduler: list new books: %v", err)
		return
	}

	now := time.Now().Unix()
	if len(books) == 0 {
		if err := s.DB.SetMeta(lastDigestMetaKey, strconv.FormatInt(now, 10)); err != nil {
			log.Printf("digest scheduler: update last-sent marker: %v", err)
		}
		return
	}

	subscribers, err := s.Users.DigestSubscribers()
	if err != nil {
		log.Printf("digest scheduler: list subscribers: %v", err)
		return
	}

	body := digestBody(s.SiteName, books)
	for _, email := range subscribers {
		if err := mail.Send(settings, email, fmt.Sprintf("%d new books in %s", len(books), s.SiteName), body); err != nil {
			log.Printf("digest scheduler: send to %s: %v", email, err)
		}
	}

	if err := s.DB.SetMeta(lastDigestMetaKey, strconv.FormatInt(now, 10)); err != nil {
		log.Printf("digest scheduler: update last-sent marker: %v", err)
	}
}

func digestBody(siteName string, books []index.Book) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d new book(s) were added to %s this week:\n\n", len(books), siteName)
	for _, book := range books {
		if book.Author != "" {
			fmt.Fprintf(&b, "- %s by %s\n", book.Title, book.Author)
		} else {
			fmt.Fprintf(&b, "- %s\n", book.Title)
		}
	}
	return b.String()
}
