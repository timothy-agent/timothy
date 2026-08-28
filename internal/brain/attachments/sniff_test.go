package attachments

import (
	"bytes"
	"errors"
	"net/http"
	"testing"
)

// Fixtures use real magic bytes for each format so the assertions
// exercise the actual http.DetectContentType behavior, not a guess at
// it. Padded to a comfortable length past each signature's own
// look-ahead requirement (e.g. the mp4 ftyp box needs its full boxSize
// available in the sniffed buffer).

var sniffPNGBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
}

func mp4Fixture() []byte {
	// ftyp box: size(24) + "ftyp" + major brand(4, unchecked) +
	// minor version(4, skipped by the sniffer) + compatible brand
	// "mp42"(4, matched) + padding so len(data) >= boxSize.
	b := []byte{0, 0, 0, 24}
	b = append(b, []byte("ftyp")...)
	b = append(b, []byte("isom")...)
	b = append(b, 0, 0, 0, 0)
	b = append(b, []byte("mp42")...)
	return append(b, make([]byte, 32)...)
}

func webmFixture() []byte {
	return []byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81, 0x01, 0x42, 0xF7, 0x81}
}

func mp3Fixture() []byte {
	// ID3v2 header: Go's sniffer only recognizes the "ID3" tag, not a
	// raw MPEG frame sync — real-world mp3 uploads carry ID3 tags.
	return append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), make([]byte, 16)...)
}

func wavFixture() []byte {
	b := []byte("RIFF")
	b = append(b, 0, 0, 0, 0) // chunk size, masked out by the sniffer
	b = append(b, []byte("WAVEfmt ")...)
	return b
}

func oggFixture() []byte {
	return append([]byte("OggS\x00"), make([]byte, 8)...)
}

func TestSniffAndNormalize(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", sniffPNGBytes, "image/png"},
		{"mp4", mp4Fixture(), "video/mp4"},
		{"webm", webmFixture(), "video/webm"},
		{"mp3", mp3Fixture(), "audio/mpeg"},
		{"wav", wavFixture(), "audio/wav"},
		{"ogg", oggFixture(), "audio/ogg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sniffed := http.DetectContentType(tc.data)
			got := normalizeSniffedMime(sniffed)
			if got != tc.want {
				t.Fatalf("normalizeSniffedMime(DetectContentType(%s)) = %q (raw sniff %q), want %q", tc.name, got, sniffed, tc.want)
			}
			if _, ok := allowedExt[got]; !ok {
				t.Fatalf("%q missing from allowedExt", got)
			}
		})
	}
}

func TestMaxBytesFor(t *testing.T) {
	cases := []struct {
		mime string
		want int64
	}{
		{"image/png", maxDefaultBytes},
		{"application/pdf", maxDefaultBytes},
		{"text/plain", maxDefaultBytes},
		{"video/mp4", maxVideoBytes},
		{"video/webm", maxVideoBytes},
		{"audio/mpeg", maxAudioBytes},
		{"audio/wav", maxAudioBytes},
		{"audio/ogg", maxAudioBytes},
	}
	for _, tc := range cases {
		if got := maxBytesFor(tc.mime); got != tc.want {
			t.Fatalf("maxBytesFor(%q) = %d, want %d", tc.mime, got, tc.want)
		}
	}
}

func TestSaveRejectsOversizeForType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := &Store{dir: dir} // db unused: rejection happens before any query
	oversized := bytes.Repeat([]byte{0}, int(maxDefaultBytes)+1)
	oversized = append(sniffPNGBytes, oversized...)
	_, err := s.Save(t.Context(), bytes.NewReader(oversized))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Save err = %v, want ErrTooLarge", err)
	}
}
