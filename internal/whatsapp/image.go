package whatsapp

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
)

// Image messages. WhatsApp displays an image message inline when it is a JPEG
// or PNG up to 16 MiB and carries its dimensions. wasync checks the format and
// reads the header dimensions without decoding pixel data, so an oversized or
// malformed image is refused before uploading or counting against pacing.

// MaxImageBytes is the largest image wasync sends as a photo message. Larger
// images must be resized, compressed or sent as a document.
const MaxImageBytes = 16 << 20

// ImageInfo is what an image message carries about its picture.
type ImageInfo struct {
	MIME          string
	Width, Height uint32
}

// ProbeImage checks that r holds a JPEG or PNG within MaxImageBytes and reads
// its dimensions from the image header without decoding pixel data.
func ProbeImage(r io.ReaderAt, size int64) (ImageInfo, error) {
	var info ImageInfo
	if size <= 0 {
		return info, fmt.Errorf("image sending supports JPEG and PNG; use document for other files")
	}
	if size > MaxImageBytes {
		return info, fmt.Errorf("image is %.1f MiB; WhatsApp sends images up to %d MiB, so resize or compress it", float64(size)/(1<<20), MaxImageBytes>>20)
	}
	prefixLen := min(size, int64(512))
	prefix := make([]byte, prefixLen)
	n, err := io.ReadFull(io.NewSectionReader(r, 0, prefixLen), prefix)
	if err != nil {
		return info, fmt.Errorf("read image: %w", err)
	}
	mime := http.DetectContentType(prefix[:n])
	var cfg image.Config
	switch mime {
	case "image/jpeg":
		cfg, err = jpeg.DecodeConfig(io.NewSectionReader(r, 0, size))
	case "image/png":
		cfg, err = png.DecodeConfig(io.NewSectionReader(r, 0, size))
	default:
		return info, fmt.Errorf("image sending supports JPEG and PNG; use document for other files")
	}
	if err != nil {
		return info, fmt.Errorf("image is not a valid JPEG or PNG: %w", err)
	}
	if cfg.Width < 1 || cfg.Height < 1 || uint64(cfg.Width) > math.MaxUint32 || uint64(cfg.Height) > math.MaxUint32 {
		return info, fmt.Errorf("image has invalid dimensions")
	}
	return ImageInfo{MIME: mime, Width: uint32(cfg.Width), Height: uint32(cfg.Height)}, nil
}
