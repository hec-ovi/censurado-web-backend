// Package media is the platform's self-hosted image store and the shared helpers
// for safe media references.
//
// The store is content-addressed: an uploaded image is validated, hashed, and
// written as <sha256>.<ext>, so identical bytes always map to the same name (natural
// dedup) and the URL is immutable, which lets it be cached forever. Image bytes live
// on disk, never in the database; an article references one only by its URL
// (/media/<sha256>.<ext>) in the open metadata bag, so storing media needs no schema
// or contract change. The store has no garbage collection: an image whose article is
// never published, or is later replaced, leaves its bytes on disk. That is bounded
// (each upload is size-capped and deduplicated by content) and accepted as the cost of
// an immutable, dependency-free store.
//
// The reference helpers (SafeMediaURL, YouTubeEmbedURL) are the single source of
// truth for turning a stored reference or an operator-supplied URL into something
// safe to render. The static generator (internal/generate) and the admin console
// both use them, so what the admin accepts is exactly what the site will render.
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultMaxBytes bounds one stored image. It is a size guard on a binary upload,
// not a content limit on article text.
const DefaultMaxBytes = 10 << 20 // 10 MiB

// allowed maps a SNIFFED content type to its canonical file extension. Only these
// image types are accepted; the type is detected from the bytes, never trusted from
// the client, so a mislabeled or hostile upload cannot smuggle a non-image through.
var allowed = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// storedNameRe is the exact content-addressed shape a stored file must have. A serve
// request is matched against it BEFORE any filesystem access, so a name can never
// traverse out of the store dir or name an arbitrary file.
var storedNameRe = regexp.MustCompile(`^[a-f0-9]{64}\.(?:jpg|png|gif|webp)$`)

var (
	// ErrTooLarge is returned when an upload exceeds the store's size limit.
	ErrTooLarge = errors.New("media: image exceeds the size limit")
	// ErrUnsupportedType is returned when the sniffed type is not an allowed image.
	ErrUnsupportedType = errors.New("media: unsupported image type")
	// ErrEmpty is returned for a zero-byte upload.
	ErrEmpty = errors.New("media: empty image")
)

// Store is a content-addressed image store rooted at one directory.
type Store struct {
	dir      string
	maxBytes int64
}

// NewStore opens (creating if needed) a store at dir. A non-positive maxBytes
// defaults to DefaultMaxBytes.
func NewStore(dir string, maxBytes int64) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("media: empty store dir")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("media: create store dir: %w", err)
	}
	return &Store{dir: dir, maxBytes: maxBytes}, nil
}

// Stored describes one stored image.
type Stored struct {
	Name        string `json:"name"`         // <sha256>.<ext>
	URL         string `json:"url"`          // /media/<sha256>.<ext> (root-relative)
	ContentType string `json:"content_type"` // the sniffed image type
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

// Put validates, hashes, and stores an image read from r. It reads at most
// maxBytes+1 so an oversize upload is detected without buffering unbounded input,
// sniffs the real content type from the bytes (the client's declared type is
// ignored), rejects anything not in the allowlist, and writes <dir>/<sha>.<ext>
// atomically. The write is skipped when the content already exists, so re-uploading
// the same image is a cheap idempotent dedup that returns the same URL.
func (s *Store) Put(r io.Reader) (Stored, error) {
	data, err := io.ReadAll(io.LimitReader(r, s.maxBytes+1))
	if err != nil {
		return Stored{}, err
	}
	if int64(len(data)) > s.maxBytes {
		return Stored{}, ErrTooLarge
	}
	if len(data) == 0 {
		return Stored{}, ErrEmpty
	}

	// DetectContentType reads the leading bytes; for images it returns the bare type
	// (no charset), but split defensively in case that ever changes.
	ctype := strings.TrimSpace(strings.SplitN(http.DetectContentType(data), ";", 2)[0])
	ext, ok := allowed[ctype]
	if !ok {
		return Stored{}, fmt.Errorf("%w: %s", ErrUnsupportedType, ctype)
	}

	sum := sha256.Sum256(data)
	hashHex := hex.EncodeToString(sum[:])
	name := hashHex + "." + ext
	stored := Stored{
		Name:        name,
		URL:         "/media/" + name,
		ContentType: ctype,
		Size:        int64(len(data)),
		SHA256:      hashHex,
	}

	dst := filepath.Join(s.dir, name)
	if _, err := os.Stat(dst); err == nil {
		return stored, nil // already stored: content-addressed dedup
	}
	if err := writeFileAtomic(dst, data); err != nil {
		return Stored{}, err
	}
	return stored, nil
}

// Open returns the file for one stored name, plus its content type, for serving.
// The name must match the content-addressed shape, so the path is always inside the
// store dir; any other name is reported as not-existing.
func (s *Store) Open(name string) (*os.File, string, error) {
	if !storedNameRe.MatchString(name) {
		return nil, "", os.ErrNotExist
	}
	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return nil, "", err
	}
	return f, contentTypeForName(name), nil
}

func contentTypeForName(name string) string {
	switch {
	case strings.HasSuffix(name, ".jpg"):
		return "image/jpeg"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".gif"):
		return "image/gif"
	case strings.HasSuffix(name, ".webp"):
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// writeFileAtomic writes data to a temp file in the same directory, then renames it
// over dst, so a serve never sees a half-written file and a crash leaves only a
// stray temp file (never a corrupt stored image).
func writeFileAtomic(dst string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		cleanup()
		return err
	}
	return nil
}

// --- shared media-reference helpers (the single source of truth) -------------

// youtubeIDRe matches a bare YouTube video id.
var youtubeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,32}$`)

// SafeMediaURL normalizes a media reference for rendering. It accepts a root-relative
// path (e.g. /media/<hash>.jpg, the self-hosted store) or an absolute http(s) URL,
// and rejects everything else (protocol-relative //host, javascript:, data:, ...),
// returning empty strings so the caller renders no media rather than an unsafe one.
// base, when set, is prefixed to a root-relative path to build the absolute URL used
// in Open Graph tags.
func SafeMediaURL(base, raw string) (src, absoluteURL string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		if strings.TrimSpace(base) == "" {
			return raw, raw
		}
		return raw, strings.TrimRight(base, "/") + raw
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return "", ""
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", ""
	}
	return raw, raw
}

// YouTubeEmbedURL turns a YouTube reference into a privacy-friendly nocookie embed
// URL, or returns empty if it is not a recognizable video. It accepts a bare video
// id or a watch / youtu.be / shorts / embed URL on any youtube host. The result is
// always a youtube-nocookie.com/embed/<id> URL, so the embed never sets tracking
// cookies until the viewer plays.
func YouTubeEmbedURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if youtubeIDRe.MatchString(raw) {
		return "https://www.youtube-nocookie.com/embed/" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	var id string
	switch host {
	case "youtu.be":
		id = strings.Trim(strings.Split(strings.Trim(u.Path, "/"), "/")[0], " ")
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com":
		switch {
		case u.Query().Get("v") != "":
			id = u.Query().Get("v")
		case strings.HasPrefix(u.Path, "/embed/"):
			id = strings.TrimPrefix(u.Path, "/embed/")
		case strings.HasPrefix(u.Path, "/shorts/"):
			id = strings.TrimPrefix(u.Path, "/shorts/")
		}
	}
	id = strings.Split(strings.Trim(id, "/"), "/")[0]
	if !youtubeIDRe.MatchString(id) {
		return ""
	}
	return "https://www.youtube-nocookie.com/embed/" + id
}
