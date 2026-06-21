package publisher

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log/slog"
)

// maxPhotoBytes is 300 KB — images larger than this are compressed before upload.
const maxPhotoBytes = 300 * 1024

// maybeCompressImage compresses image data when its size exceeds maxBytes.
//
//   - JPEG: re-encoded at progressively lower quality (80 → 60 → 40) until the
//     result fits. If even quality 40 is still too large, that smallest result is
//     returned rather than giving up entirely.
//   - PNG: decoded, composited onto a white background (flattens alpha), then
//     re-encoded as JPEG at quality 80. contentType is updated to "image/jpeg".
//   - Everything else (GIF, WebP, …): returned unchanged with no error.
//
// Decode failures are non-fatal: the original data is returned so the upload can
// still proceed. When no compression is needed the inputs are returned as-is.
func maybeCompressImage(data []byte, contentType string, maxBytes int, log *slog.Logger) ([]byte, string, error) {
	if len(data) <= maxBytes {
		return data, contentType, nil
	}

	originalSize := len(data)

	switch contentType {
	case "image/jpeg":
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			// A JPEG that Go can't decode is still worth attempting to upload.
			log.Warn("image_compress: jpeg decode failed, uploading original",
				"original_bytes", originalSize, "err", err)
			return data, contentType, nil
		}
		out, quality, err := compressJPEG(img, maxBytes)
		if err != nil {
			return nil, "", err
		}
		log.Info("image_compress: compressed jpeg",
			"original_bytes", originalSize,
			"compressed_bytes", len(out),
			"quality", quality,
			"under_limit", len(out) <= maxBytes)
		return out, "image/jpeg", nil

	case "image/png":
		out, err := pngToJPEG(data)
		if err != nil {
			log.Warn("image_compress: png→jpeg conversion failed, uploading original",
				"original_bytes", originalSize, "err", err)
			return data, contentType, nil
		}
		log.Info("image_compress: converted png→jpeg",
			"original_bytes", originalSize,
			"compressed_bytes", len(out))
		return out, "image/jpeg", nil

	default:
		// No stdlib encoder for this type; pass through unchanged.
		return data, contentType, nil
	}
}

// compressJPEG re-encodes img at quality levels 80, 60, 40 in order, stopping
// as soon as the result fits within maxBytes. If none of the levels fit, the
// quality-40 result (smallest) is returned along with quality=40.
func compressJPEG(img image.Image, maxBytes int) ([]byte, int, error) {
	qualities := []int{80, 60, 40}
	var best []byte
	for _, q := range qualities {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, 0, fmt.Errorf("jpeg encode at quality %d: %w", q, err)
		}
		best = buf.Bytes()
		if len(best) <= maxBytes {
			return best, q, nil
		}
	}
	return best, 40, nil
}

// pngToJPEG decodes a PNG, composites it onto a solid white RGBA background to
// eliminate transparency, and encodes the result as JPEG at quality 80.
// White is the safest default for a white-background WordPress theme.
func pngToJPEG(data []byte) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("png decode: %w", err)
	}

	bounds := src.Bounds()
	canvas := image.NewRGBA(bounds)

	// Fill with white, then composite the PNG on top.
	draw.Draw(canvas, bounds, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(canvas, bounds, src, bounds.Min, draw.Over)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 80}); err != nil {
		return nil, fmt.Errorf("jpeg encode from png: %w", err)
	}
	return buf.Bytes(), nil
}
