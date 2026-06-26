package media_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hec-ovi/censurado-web-backend/media"
)

// Minimal byte prefixes that net/http's DetectContentType recognizes as each image
// type. The store sniffs the type from the bytes (never the client's claim), so
// these headers are enough to exercise the allowlist without real image payloads.
var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegBytes = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	gifBytes  = []byte("GIF89a\x01\x00\x01\x00")
	webpBytes = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
)

func newStore(t *testing.T, maxBytes int64) (*media.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := media.NewStore(dir, maxBytes)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s, dir
}

func TestStore_PutRoundTripsEachType(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		ext  string
		ct   string
	}{
		{"png", pngBytes, ".png", "image/png"},
		{"jpeg", jpegBytes, ".jpg", "image/jpeg"},
		{"gif", gifBytes, ".gif", "image/gif"},
		{"webp", webpBytes, ".webp", "image/webp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t, 0)
			got, err := s.Put(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("put: %v", err)
			}
			if !strings.HasSuffix(got.Name, tc.ext) || got.ContentType != tc.ct {
				t.Fatalf("stored = %+v, want ext %s type %s", got, tc.ext, tc.ct)
			}
			if got.URL != "/media/"+got.Name {
				t.Errorf("url = %q, want /media/%s", got.URL, got.Name)
			}
			// Open returns the same bytes and content type for serving.
			f, ct, err := s.Open(got.Name)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer f.Close()
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(f); err != nil {
				t.Fatalf("read stored: %v", err)
			}
			if !bytes.Equal(buf.Bytes(), tc.data) {
				t.Errorf("served bytes differ from stored")
			}
			if ct != tc.ct {
				t.Errorf("served content type = %q, want %q", ct, tc.ct)
			}
		})
	}
}

func TestStore_PutIsContentAddressedDedup(t *testing.T) {
	s, dir := newStore(t, 0)
	a, err := s.Put(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("put a: %v", err)
	}
	b, err := s.Put(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("put b: %v", err)
	}
	if a.Name != b.Name || a.SHA256 != b.SHA256 {
		t.Fatalf("same bytes mapped to different names: %q vs %q", a.Name, b.Name)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dedup: %d files on disk, want 1", len(entries))
	}
}

func TestStore_PutRejectsOversizeBeforeSniffing(t *testing.T) {
	s, _ := newStore(t, 16) // tiny cap
	big := append(append([]byte{}, pngBytes...), make([]byte, 32)...)
	if _, err := s.Put(bytes.NewReader(big)); !errors.Is(err, media.ErrTooLarge) {
		t.Fatalf("oversize err = %v, want ErrTooLarge", err)
	}
}

func TestStore_PutRejectsNonImageAndEmpty(t *testing.T) {
	s, _ := newStore(t, 0)
	if _, err := s.Put(strings.NewReader("just some plain text, definitely not an image")); !errors.Is(err, media.ErrUnsupportedType) {
		t.Fatalf("text err = %v, want ErrUnsupportedType", err)
	}
	if _, err := s.Put(bytes.NewReader(nil)); !errors.Is(err, media.ErrEmpty) {
		t.Fatalf("empty err = %v, want ErrEmpty", err)
	}
}

func TestStore_OpenRejectsNonContentAddressedNames(t *testing.T) {
	s, _ := newStore(t, 0)
	for _, name := range []string{"../etc/passwd", "foo.png", "deadbeef.png", "", "a/b.png"} {
		if _, _, err := s.Open(name); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Open(%q) err = %v, want ErrNotExist", name, err)
		}
	}
	// A well-formed but absent name is also not-exist (never a path leak).
	missing := strings.Repeat("a", 64) + ".png"
	if _, _, err := s.Open(missing); err == nil {
		t.Errorf("Open(%q) succeeded, want an error for a missing file", missing)
	}
	// Sanity: a real stored file does open.
	got, _ := s.Put(bytes.NewReader(pngBytes))
	if _, _, err := s.Open(got.Name); err != nil {
		t.Errorf("Open(%q) of a stored file failed: %v", got.Name, err)
	}
}

func TestYouTubeEmbedURL(t *testing.T) {
	const want = "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"
	for _, in := range []string{
		"dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ",
	} {
		if got := media.YouTubeEmbedURL(in); got != want {
			t.Errorf("YouTubeEmbedURL(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "not a url", "https://vimeo.com/123", "https://example.com/watch?v=x"} {
		if got := media.YouTubeEmbedURL(in); got != "" {
			t.Errorf("YouTubeEmbedURL(%q) = %q, want empty", in, got)
		}
	}
}

func TestSafeMediaURL(t *testing.T) {
	if src, abs := media.SafeMediaURL("https://site.test", "/media/x.jpg"); src != "/media/x.jpg" || abs != "https://site.test/media/x.jpg" {
		t.Errorf("root-relative: got %q / %q", src, abs)
	}
	if src, _ := media.SafeMediaURL("", "https://cdn.test/a.png"); src != "https://cdn.test/a.png" {
		t.Errorf("absolute https dropped: %q", src)
	}
	for _, bad := range []string{"javascript:alert(1)", "//evil.test/x.jpg", "data:image/png;base64,AAAA", "ftp://x/y"} {
		if src, _ := media.SafeMediaURL("", bad); src != "" {
			t.Errorf("SafeMediaURL(%q) = %q, want empty (unsafe)", bad, src)
		}
	}
}
