package publisher

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log/slog"
	"math/rand"
	"testing"
)

// discardLog is a no-op logger for tests that don't care about log output.
var discardLog = slog.New(slog.NewTextHandler(noopWriter{}, nil))

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// makeNoisyJPEG returns a JPEG image filled with random pixel noise at the given
// quality. Noise prevents JPEG from compressing the image to a trivially small
// size, making size assertions meaningful.
func makeNoisyJPEG(t *testing.T, width, height, quality int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewSource(42))
	for y := range height {
		for x := range width {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("makeNoisyJPEG: encode failed: %v", err)
	}
	return buf.Bytes()
}

// makeNoisyPNG returns a PNG with random pixels and a semi-transparent alpha
// channel so the white-background compositing step is exercised.
func makeNoisyPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewSource(99))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: uint8(128 + rng.Intn(128)), // semi-transparent
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("makeNoisyPNG: encode failed: %v", err)
	}
	return buf.Bytes()
}

// makeTransparentPNG returns a PNG where the top half is fully transparent and
// the bottom half is solid red. Used to verify that transparency is composited
// onto white correctly.
func makeTransparentPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := range 50 {
		for x := range 100 {
			img.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 0}) // fully transparent
		}
	}
	for y := 50; y < 100; y++ {
		for x := range 100 {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255}) // solid red
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("makeTransparentPNG: encode failed: %v", err)
	}
	return buf.Bytes()
}

// ── maybeCompressImage ───────────────────────────────────────────────────────

func TestMaybeCompress_SmallJPEGPassthrough(t *testing.T) {
	// A small JPEG well under the 300 KB limit should be returned byte-for-byte.
	data := makeNoisyJPEG(t, 50, 50, 90)
	if len(data) >= maxPhotoBytes {
		t.Fatalf("precondition: test image (%d B) must be < %d B", len(data), maxPhotoBytes)
	}

	out, ct, err := maybeCompressImage(data, "image/jpeg", maxPhotoBytes, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "image/jpeg" {
		t.Errorf("content type changed to %q", ct)
	}
	if !bytes.Equal(out, data) {
		t.Error("bytes changed for an image that was already under the limit")
	}
}

func TestMaybeCompress_LargeJPEGCompressed(t *testing.T) {
	// A large noisy JPEG over the limit must come back smaller and still decodable.
	data := makeNoisyJPEG(t, 1000, 1000, 99)
	if len(data) <= maxPhotoBytes {
		t.Fatalf("precondition: test image (%d B) must be > %d B", len(data), maxPhotoBytes)
	}

	out, ct, err := maybeCompressImage(data, "image/jpeg", maxPhotoBytes, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "image/jpeg" {
		t.Errorf("content type should remain image/jpeg, got %q", ct)
	}
	if len(out) >= len(data) {
		t.Errorf("expected output (%d B) to be smaller than input (%d B)", len(out), len(data))
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("output is not a valid JPEG: %v", err)
	}
}

func TestMaybeCompress_LargeJPEGUnderThreshold(t *testing.T) {
	// With a generous enough maxBytes, quality-80 should be sufficient and the
	// result must fit within the specified limit.
	data := makeNoisyJPEG(t, 800, 800, 99)
	if len(data) <= maxPhotoBytes {
		t.Fatalf("precondition: test image (%d B) must be > %d B", len(data), maxPhotoBytes)
	}

	// Use a threshold large enough that quality-80 satisfies it.
	generousMax := len(data) - 1 // anything smaller than original is fine
	out, _, err := maybeCompressImage(data, "image/jpeg", generousMax, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) > generousMax {
		t.Errorf("output (%d B) exceeds maxBytes (%d B)", len(out), generousMax)
	}
}

func TestMaybeCompress_LargePNGConverted(t *testing.T) {
	// A large PNG over the limit must be converted to JPEG.
	data := makeNoisyPNG(t, 800, 800)

	// Force the threshold low so even a normal-sized PNG triggers conversion.
	out, ct, err := maybeCompressImage(data, "image/png", 1, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "image/jpeg" {
		t.Errorf("expected content type image/jpeg after PNG conversion, got %q", ct)
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("output is not a valid JPEG: %v", err)
	}
}

func TestMaybeCompress_SmallPNGPassthrough(t *testing.T) {
	// A PNG under the limit must pass through unchanged — no conversion to JPEG.
	data := makeNoisyPNG(t, 10, 10)
	if len(data) >= maxPhotoBytes {
		t.Fatalf("precondition: test image (%d B) must be < %d B", len(data), maxPhotoBytes)
	}

	out, ct, err := maybeCompressImage(data, "image/png", maxPhotoBytes, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "image/png" {
		t.Errorf("content type should stay image/png for small images, got %q", ct)
	}
	if !bytes.Equal(out, data) {
		t.Error("bytes changed for an image already under the limit")
	}
}

func TestMaybeCompress_GIFPassthrough(t *testing.T) {
	// GIFs have no stdlib encoder; they must pass through unchanged regardless of size.
	data := bytes.Repeat([]byte("GIF89a fake gif data"), 20000)

	out, ct, err := maybeCompressImage(data, "image/gif", 1, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "image/gif" {
		t.Errorf("content type changed to %q", ct)
	}
	if !bytes.Equal(out, data) {
		t.Error("bytes changed for unsupported image type")
	}
}

func TestMaybeCompress_WebPPassthrough(t *testing.T) {
	// WebP is also unsupported; same passthrough expectation as GIF.
	data := bytes.Repeat([]byte("RIFF fake webp data"), 20000)

	out, ct, err := maybeCompressImage(data, "image/webp", 1, discardLog)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "image/webp" {
		t.Errorf("content type changed to %q", ct)
	}
	if !bytes.Equal(out, data) {
		t.Error("bytes changed for unsupported image type")
	}
}

func TestMaybeCompress_CorruptJPEGPassthrough(t *testing.T) {
	// Garbage bytes declared as JPEG: decode will fail. The function must return
	// the original bytes rather than an error so the upload can still proceed.
	data := bytes.Repeat([]byte{0xFF, 0xD8, 0x00, 0xAB, 0xCD}, 100000) // fake JPEG header, corrupt body

	out, ct, err := maybeCompressImage(data, "image/jpeg", 1, discardLog)
	if err != nil {
		t.Fatalf("expected no error on corrupt JPEG, got: %v", err)
	}
	if ct != "image/jpeg" {
		t.Errorf("content type should not change on corrupt input, got %q", ct)
	}
	if !bytes.Equal(out, data) {
		t.Error("original bytes should be returned when decode fails")
	}
}

func TestMaybeCompress_CorruptPNGPassthrough(t *testing.T) {
	// Same as above but for PNG.
	data := bytes.Repeat([]byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0xAB}, 100000) // fake PNG header

	out, ct, err := maybeCompressImage(data, "image/png", 1, discardLog)
	if err != nil {
		t.Fatalf("expected no error on corrupt PNG, got: %v", err)
	}
	if ct != "image/png" {
		t.Errorf("content type should not change on corrupt input, got %q", ct)
	}
	if !bytes.Equal(out, data) {
		t.Error("original bytes should be returned when decode fails")
	}
}

// ── compressJPEG ────────────────────────────────────────────────────────────

func TestCompressJPEG_StopsAtFirstFit(t *testing.T) {
	// With a generous limit, the loop should exit on the first quality (80)
	// and not waste time encoding at lower qualities.
	img := makeDecodedNoisyImage(t, 200, 200)

	// Encode at quality 80 to find out how large that is, then use it as maxBytes.
	var q80buf bytes.Buffer
	if err := jpeg.Encode(&q80buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	generousMax := q80buf.Len() + 1

	_, quality, err := compressJPEG(img, generousMax)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quality != 80 {
		t.Errorf("expected quality 80 to be sufficient, but loop used quality %d", quality)
	}
}

func TestCompressJPEG_FallsBackToLowest(t *testing.T) {
	// With an impossibly tight limit, the function should fall back to quality 40
	// and return it without error rather than panicking or returning an empty result.
	img := makeDecodedNoisyImage(t, 200, 200)

	out, quality, err := compressJPEG(img, 1) // 1 byte — no JPEG can fit
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quality != 40 {
		t.Errorf("expected fallback quality 40, got %d", quality)
	}
	if len(out) == 0 {
		t.Error("output must not be empty even at the minimum quality")
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("fallback output is not a valid JPEG: %v", err)
	}
}

// makeDecodedNoisyImage is a helper that produces an image.Image (already decoded)
// for direct use in compressJPEG tests.
func makeDecodedNoisyImage(t *testing.T, width, height int) image.Image {
	t.Helper()
	data := makeNoisyJPEG(t, width, height, 99)
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("makeDecodedNoisyImage: decode failed: %v", err)
	}
	return img
}

// ── pngToJPEG ───────────────────────────────────────────────────────────────

func TestPNGToJPEG_TransparencyToWhite(t *testing.T) {
	// The top half of the PNG is fully transparent; after conversion to JPEG on a
	// white background, those pixels must be close to white (JPEG is lossy, so we
	// allow ±15 per channel rather than requiring exact 255).
	data := makeTransparentPNG(t)

	out, err := pngToJPEG(data)
	if err != nil {
		t.Fatalf("pngToJPEG failed: %v", err)
	}

	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("output is not a valid JPEG: %v", err)
	}

	// Sample the centre of the transparent region (top half → row 25, col 50).
	r, g, b, _ := img.At(50, 25).RGBA()
	// RGBA() returns 16-bit values; shift to 8-bit.
	r8, g8, b8 := uint8(r>>8), uint8(g>>8), uint8(b>>8)
	const minWhite = uint8(240)
	if r8 < minWhite || g8 < minWhite || b8 < minWhite {
		t.Errorf("transparent region should map to near-white; got R=%d G=%d B=%d", r8, g8, b8)
	}
}
