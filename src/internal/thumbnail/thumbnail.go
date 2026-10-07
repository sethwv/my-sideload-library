// Package thumbnail resizes and caches book cover images to disk.
package thumbnail

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/image/draw"
)

// Store caches resized JPEG cover thumbnails on disk, keyed by book ID.
// width is mutable (see SetWidth) so the admin Settings page can change it
// without a restart; new covers pick it up immediately, but already-cached
// files on disk keep their original size until next re-saved (e.g. a
// rescan/reimport).
type Store struct {
	dir string

	mu    sync.RWMutex
	width int
}

// NewStore creates (if needed) coversDir and returns a Store that resizes
// covers to the given width, preserving aspect ratio.
func NewStore(coversDir string, width int) (*Store, error) {
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		return nil, fmt.Errorf("create covers dir: %w", err)
	}
	if width < 1 {
		width = 300
	}
	return &Store{dir: coversDir, width: width}, nil
}

// SetWidth updates the resize width used for covers saved from now on.
func (s *Store) SetWidth(width int) {
	if width < 1 {
		width = 300
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.width = width
}

// SaveCover decodes, resizes, and writes the cover for bookID, returning the
// filename (relative to the covers dir) to store as cover_path.
func (s *Store) SaveCover(bookID int64, data []byte, mediaType string) (string, error) {
	return s.saveCover(fmt.Sprintf("%d.jpg", bookID), data)
}

// SaveConnectionCover stores a provider cover under a stable opaque name.
func (s *Store) SaveConnectionCover(key string, data []byte, mediaType string) (string, error) {
	name := fmt.Sprintf("connection-%x.jpg", sha256.Sum256([]byte(key)))
	return s.saveCover(name, data)
}

func (s *Store) saveCover(name string, data []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("decode cover: %w", err)
	}

	s.mu.RLock()
	width := s.width
	s.mu.RUnlock()
	bounds := img.Bounds()
	height := max(1, width*bounds.Dy()/bounds.Dx())
	resized := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(resized, resized.Bounds(), img, bounds, draw.Over, nil)

	fullPath := filepath.Join(s.dir, name)

	f, err := os.Create(fullPath)
	if err != nil {
		return "", fmt.Errorf("create thumbnail file: %w", err)
	}
	defer f.Close()

	if err := jpeg.Encode(f, resized, &jpeg.Options{Quality: 82}); err != nil {
		return "", fmt.Errorf("encode thumbnail: %w", err)
	}

	return name, nil
}

// Path returns the full filesystem path for a cover_path value.
func (s *Store) Path(relPath string) string {
	return filepath.Join(s.dir, relPath)
}
