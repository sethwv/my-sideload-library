package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const hardcoverEndpoint = "https://api.hardcover.app/v1/graphql"

// HardcoverSource fetches a user's personal Hardcover library. It is separate
// from internal/hardcover, which owns the server-wide enrichment credential.
type HardcoverSource struct{ http *http.Client }

func NewHardcoverSource(transport http.RoundTripper) *HardcoverSource {
	return &HardcoverSource{http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}

func NormalizeHardcoverToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) >= 7 && strings.EqualFold(token[:7], "bearer ") {
		return token
	}
	return "Bearer " + token
}

const hardcoverLibraryQuery = `query PersonalLibrary {
  me {
    user_books(order_by: {date_added: desc}) {
      id
      date_added
      status_id
      book {
        id
        title
        contributions { author { name } }
      }
      edition { isbn_10 isbn_13 }
    }
  }
}`

type hardcoverLibraryResponse struct {
	Me []struct {
		UserBooks []struct {
			AddedAt  string `json:"date_added"`
			StatusID int    `json:"status_id"`
			Book     *struct {
				ID            hardcoverID `json:"id"`
				Title         string      `json:"title"`
				Contributions []struct {
					Author *struct {
						Name string `json:"name"`
					} `json:"author"`
				} `json:"contributions"`
			} `json:"book"`
			Edition *struct {
				ISBN10 string `json:"isbn_10"`
				ISBN13 string `json:"isbn_13"`
			} `json:"edition"`
		} `json:"user_books"`
	} `json:"me"`
}

func (s *HardcoverSource) Snapshot(ctx context.Context, token string) (Snapshot, error) {
	token = NormalizeHardcoverToken(token)
	if token == "" {
		return Snapshot{}, fmt.Errorf("a Hardcover API token is required")
	}
	body, err := json.Marshal(struct {
		Query string `json:"query"`
	}{Query: hardcoverLibraryQuery})
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode Hardcover request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hardcoverEndpoint, bytes.NewReader(body))
	if err != nil {
		return Snapshot{}, fmt.Errorf("build Hardcover request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	req.Header.Set("User-Agent", "my-sideload-library")
	resp, err := s.http.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("contact Hardcover: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read Hardcover response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return Snapshot{}, fmt.Errorf("Hardcover rejected that API token")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Description string `json:"error_description"`
		}
		if json.Unmarshal(data, &failure) == nil && strings.TrimSpace(failure.Description) != "" {
			return Snapshot{}, fmt.Errorf("Hardcover request failed: %s", boundedHardcoverMessage(failure.Description))
		}
		if resp.StatusCode == http.StatusForbidden {
			return Snapshot{}, fmt.Errorf("Hardcover rejected that API token")
		}
		return Snapshot{}, fmt.Errorf("Hardcover is unavailable (status %d)", resp.StatusCode)
	}
	var envelope struct {
		Data   hardcoverLibraryResponse `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Snapshot{}, fmt.Errorf("decode Hardcover response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return Snapshot{}, fmt.Errorf("Hardcover could not read this library: %s", boundedHardcoverMessage(envelope.Errors[0].Message))
	}
	if len(envelope.Data.Me) == 0 {
		return Snapshot{}, fmt.Errorf("Hardcover returned no account for that API token")
	}

	// Reading statuses are fixed by Hardcover. Include empty statuses in source
	// discovery so users can opt in before a status receives its first book.
	shelves := map[string]Shelf{}
	for _, statusID := range []int{1, 2, 3, 4, 5, 6} {
		key, name := hardcoverShelf(statusID)
		shelves[key] = Shelf{Key: key, Name: name}
	}
	items := make([]Item, 0, len(envelope.Data.Me[0].UserBooks))
	for position, record := range envelope.Data.Me[0].UserBooks {
		if record.Book == nil || strings.TrimSpace(record.Book.Title) == "" {
			continue
		}
		externalID := string(record.Book.ID)
		if externalID == "" {
			continue
		}
		key, name := hardcoverShelf(record.StatusID)
		if key == "" {
			continue
		}
		shelves[key] = Shelf{Key: key, Name: name}
		author := ""
		for _, contribution := range record.Book.Contributions {
			if contribution.Author != nil && strings.TrimSpace(contribution.Author.Name) != "" {
				author = contribution.Author.Name
				break
			}
		}
		isbn := ""
		if record.Edition != nil {
			isbn = record.Edition.ISBN13
			if isbn == "" {
				isbn = record.Edition.ISBN10
			}
		}
		items = append(items, Item{ShelfKey: key, ExternalID: externalID, Title: record.Book.Title, Author: author, ISBN: isbn, AddedAt: hardcoverTime(record.AddedAt), Position: position})
	}
	result := Snapshot{Items: items}
	for _, shelf := range shelves {
		result.Shelves = append(result.Shelves, shelf)
	}
	return result, nil
}

func boundedHardcoverMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 300 {
		return message[:300]
	}
	return message
}

type hardcoverID string

func (id *hardcoverID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*id = hardcoverID(strings.TrimSpace(value))
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*id = hardcoverID(number.String())
	return nil
}

func hardcoverShelf(statusID int) (string, string) {
	switch statusID {
	case 1:
		return "to-read", "Want to Read"
	case 2:
		return "currently-reading", "Currently Reading"
	case 3:
		return "read", "Read"
	case 4:
		return "paused", "Paused"
	case 5:
		return "did-not-finish", "Did Not Finish"
	case 6:
		return "ignored", "Ignored"
	default:
		return "", ""
	}
}

func hardcoverTime(value string) int64 {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.Unix()
	}
	return 0
}
