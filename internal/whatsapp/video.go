package whatsapp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
)

// Video messages. WhatsApp plays a video message only when it is an MP4 with
// H.264 video (and AAC audio, when it has sound), and it shows the clip's
// length, shape and a small JPEG preview from fields in the message itself, so
// wasync reads those from the file before sending. It parses the MP4 container
// in Go rather than requiring ffmpeg; only the preview frame needs a video
// decoder, and the caller that has the file (the wasync CLI or wasync-axi)
// extracts it with ffmpeg when that is installed and passes it along.

// MaxVideoBytes is the largest video wasync sends as a playable video. It is
// the limit WhatsApp's linked-device clients apply before they fall back to
// sending a video as a document; larger clips must be shortened or compressed.
const MaxVideoBytes = 64 << 20

// MaxThumbnailBytes bounds the preview frame carried with a video: a JPEG of
// at most 320 pixels on each side, as ffmpeg is asked for, is far smaller.
const MaxThumbnailBytes = 64 << 10

const (
	videoMIME       = "video/mp4"
	thumbnailMaxDim = 320  // the preview's longest side, in pixels
	maxMP4Boxes     = 4096 // boxes examined in one file, against crafted input
)

// VideoInfo is what a video message carries about its clip.
type VideoInfo struct {
	Seconds       uint32
	Width, Height uint32 // as displayed, after any rotation
	Audio         bool
}

var errNotMP4 = errors.New("video sending supports MP4 files with H.264 video and AAC audio; convert other formats first, for example with ffmpeg -i IN -c:v libx264 -c:a aac -movflags +faststart OUT.mp4")

// ProbeMP4 checks that r holds an MP4 that WhatsApp plays as a video message
// and reads its length and dimensions. It reads only box headers and the few
// small boxes it needs, never the media data.
func ProbeMP4(r io.ReaderAt, size int64) (VideoInfo, error) {
	var info VideoInfo
	if size > MaxVideoBytes {
		return info, fmt.Errorf("video is %.1f MiB; WhatsApp sends videos up to %d MiB as playable video, so shorten or compress it", float64(size)/(1<<20), MaxVideoBytes>>20)
	}
	p := &mp4Parser{r: r}
	top, err := p.children(0, size)
	if err != nil {
		return info, err
	}
	if len(top) == 0 || top[0].kind != "ftyp" {
		return info, errNotMP4
	}
	if ok, err := p.mp4Brand(top[0]); err != nil || !ok {
		return info, errNotMP4
	}
	var moov *mp4Box
	for i := range top {
		if top[i].kind == "moov" {
			moov = &top[i]
			break
		}
	}
	if moov == nil {
		return info, fmt.Errorf("video has no MP4 index (moov box); it may be incomplete or fragmented, so re-encode it with -movflags +faststart")
	}
	boxes, err := p.children(moov.body, moov.end)
	if err != nil {
		return info, err
	}
	var timescale, duration uint64
	videoTracks := 0
	for _, b := range boxes {
		switch b.kind {
		case "mvhd":
			if timescale, duration, err = p.movieHeader(b); err != nil {
				return info, err
			}
		case "trak":
			t, err := p.track(b)
			if err != nil {
				return info, err
			}
			switch t.handler {
			case "vide":
				if t.codec != "avc1" && t.codec != "avc3" {
					return info, fmt.Errorf("video uses the %q codec; WhatsApp plays H.264 (avc1) video, so re-encode it with -c:v libx264", t.codec)
				}
				videoTracks++
				if info.Width == 0 {
					info.Width, info.Height = t.width, t.height
				}
			case "soun":
				if !t.aac {
					return info, fmt.Errorf("video's sound uses the %q format; WhatsApp plays AAC audio, so re-encode it with -c:a aac", t.codec)
				}
				info.Audio = true
			}
		}
	}
	if videoTracks == 0 {
		return info, fmt.Errorf("file has no video track; send it as a document instead")
	}
	if timescale == 0 || duration == 0 {
		return info, fmt.Errorf("could not read the video's length; it may be fragmented, so re-encode it with -movflags +faststart")
	}
	seconds := math.Round(float64(duration) / float64(timescale))
	info.Seconds = uint32(max(1, min(seconds, math.MaxUint32)))
	return info, nil
}

type mp4Box struct {
	kind      string
	body, end int64 // the payload's offsets in the file
}

type mp4Parser struct {
	r     io.ReaderAt
	boxes int
}

// children lists the boxes laid end to end in [from, to).
func (p *mp4Parser) children(from, to int64) ([]mp4Box, error) {
	var out []mp4Box
	for off := from; off < to; {
		p.boxes++
		if p.boxes > maxMP4Boxes {
			return nil, errNotMP4
		}
		if to-off < 8 {
			return nil, errNotMP4
		}
		var h [16]byte
		if _, err := p.r.ReadAt(h[:8], off); err != nil {
			return nil, errNotMP4
		}
		size, header := int64(binary.BigEndian.Uint32(h[:4])), int64(8)
		switch size {
		case 0: // extends to the end of its parent
			size = to - off
		case 1: // a 64-bit size follows the type
			if to-off < 16 {
				return nil, errNotMP4
			}
			if _, err := p.r.ReadAt(h[8:16], off+8); err != nil {
				return nil, errNotMP4
			}
			large := binary.BigEndian.Uint64(h[8:16])
			if large > uint64(math.MaxInt64) {
				return nil, errNotMP4
			}
			size, header = int64(large), 16
		}
		if size < header || size > to-off {
			return nil, errNotMP4
		}
		out = append(out, mp4Box{kind: string(h[4:8]), body: off + header, end: off + size})
		off += size
	}
	return out, nil
}

// read returns up to n bytes of a box's payload, starting skip bytes in.
func (p *mp4Parser) read(b mp4Box, skip, n int64) ([]byte, error) {
	if skip < 0 || n < 0 || b.body+skip+n > b.end {
		return nil, errNotMP4
	}
	buf := make([]byte, n)
	if _, err := p.r.ReadAt(buf, b.body+skip); err != nil {
		return nil, errNotMP4
	}
	return buf, nil
}

// mp4Brand accepts the ISO base media and MP4 brands, not QuickTime or 3GP.
func (p *mp4Parser) mp4Brand(ftyp mp4Box) (bool, error) {
	n := min(ftyp.end-ftyp.body, 4+4+4*64)
	if n < 8 {
		return false, errNotMP4
	}
	data, err := p.read(ftyp, 0, n)
	if err != nil {
		return false, err
	}
	brands := [][]byte{data[:4]}
	for i := 8; i+4 <= len(data); i += 4 {
		brands = append(brands, data[i:i+4])
	}
	for _, b := range brands {
		switch string(b) {
		case "isom", "iso2", "iso3", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "M4V ", "MSNV", "dash":
			return true, nil
		}
	}
	return false, nil
}

// movieHeader reads the movie's timescale and duration from mvhd.
func (p *mp4Parser) movieHeader(b mp4Box) (timescale, duration uint64, err error) {
	v, err := p.read(b, 0, 1)
	if err != nil {
		return 0, 0, err
	}
	if v[0] == 1 {
		d, err := p.read(b, 4+8+8, 4+8)
		if err != nil {
			return 0, 0, err
		}
		return uint64(binary.BigEndian.Uint32(d[:4])), binary.BigEndian.Uint64(d[4:]), nil
	}
	d, err := p.read(b, 4+4+4, 4+4)
	if err != nil {
		return 0, 0, err
	}
	duration = uint64(binary.BigEndian.Uint32(d[4:]))
	if duration == math.MaxUint32 { // "unknown" in a version 0 header
		duration = 0
	}
	return uint64(binary.BigEndian.Uint32(d[:4])), duration, nil
}

type mp4Track struct {
	handler, codec string
	width, height  uint32
	aac            bool
}

// track reads a trak's kind, first sample format and display size.
func (p *mp4Parser) track(trak mp4Box) (mp4Track, error) {
	var t mp4Track
	boxes, err := p.children(trak.body, trak.end)
	if err != nil {
		return t, err
	}
	for _, b := range boxes {
		switch b.kind {
		case "tkhd":
			if t.width, t.height, err = p.trackSize(b); err != nil {
				return t, err
			}
		case "mdia":
			if err = p.media(b, &t); err != nil {
				return t, err
			}
		}
	}
	return t, nil
}

// trackSize reads tkhd's 16.16 width and height, swapped when the matrix
// turns the picture a quarter turn, as phone recordings often do.
func (p *mp4Parser) trackSize(b mp4Box) (uint32, uint32, error) {
	v, err := p.read(b, 0, 1)
	if err != nil {
		return 0, 0, err
	}
	matrix := int64(4 + 4 + 4 + 4 + 4 + 4 + 8 + 2 + 2 + 2 + 2)
	if v[0] == 1 {
		matrix = 4 + 8 + 8 + 4 + 4 + 8 + 8 + 2 + 2 + 2 + 2
	}
	d, err := p.read(b, matrix, 36+8)
	if err != nil {
		return 0, 0, err
	}
	a, dd := int32(binary.BigEndian.Uint32(d[0:4])), int32(binary.BigEndian.Uint32(d[16:20]))
	w, h := binary.BigEndian.Uint32(d[36:40])>>16, binary.BigEndian.Uint32(d[40:44])>>16
	if a == 0 && dd == 0 {
		w, h = h, w
	}
	return w, h, nil
}

// media reads the handler type and the first sample description.
func (p *mp4Parser) media(mdia mp4Box, t *mp4Track) error {
	boxes, err := p.children(mdia.body, mdia.end)
	if err != nil {
		return err
	}
	for _, b := range boxes {
		switch b.kind {
		case "hdlr":
			d, err := p.read(b, 8, 4)
			if err != nil {
				return err
			}
			t.handler = string(d)
		case "minf":
			stbl, err := p.child(b, "stbl")
			if err != nil || stbl == nil {
				return err
			}
			stsd, err := p.child(*stbl, "stsd")
			if err != nil || stsd == nil {
				return err
			}
			entries, err := p.children(stsd.body+8, stsd.end)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				return errNotMP4
			}
			t.codec = entries[0].kind
			if t.codec == "mp4a" {
				t.aac = p.isAAC(entries[0])
			}
		}
	}
	return nil
}

func (p *mp4Parser) child(parent mp4Box, kind string) (*mp4Box, error) {
	boxes, err := p.children(parent.body, parent.end)
	if err != nil {
		return nil, err
	}
	for i := range boxes {
		if boxes[i].kind == kind {
			return &boxes[i], nil
		}
	}
	return nil, nil
}

// isAAC reports whether an mp4a sample entry declares AAC (MPEG-4 audio or
// MPEG-2 AAC) in its decoder configuration rather than MP3 or another format.
func (p *mp4Parser) isAAC(entry mp4Box) bool {
	// An audio sample entry: 8 reserved and reference bytes, then a version
	// that sets how many more fields precede its child boxes.
	v, err := p.read(entry, 8, 2)
	if err != nil {
		return false
	}
	skip := int64(28)
	switch binary.BigEndian.Uint16(v) {
	case 1:
		skip += 16
	case 2:
		skip += 36
	}
	if entry.end-entry.body < skip {
		return false
	}
	boxes, err := p.children(entry.body+skip, entry.end)
	if err != nil {
		return false
	}
	for _, b := range boxes {
		if b.kind != "esds" || b.end-b.body > 1<<10 {
			continue
		}
		d, err := p.read(b, 0, b.end-b.body)
		if err != nil {
			return false
		}
		oti, ok := esdsObjectType(d[min(4, len(d)):])
		return ok && (oti == 0x40 || oti == 0x66 || oti == 0x67 || oti == 0x68)
	}
	return false
}

// esdsObjectType walks an ES descriptor to its decoder configuration and
// returns the object type indication.
func esdsObjectType(d []byte) (byte, bool) {
	tag, body, ok := descriptor(d)
	if !ok || tag != 0x03 || len(body) < 3 {
		return 0, false
	}
	flags, rest := body[2], body[3:]
	if flags&0x80 != 0 {
		if len(rest) < 2 {
			return 0, false
		}
		rest = rest[2:]
	}
	if flags&0x40 != 0 {
		if len(rest) < 1 || len(rest) < 1+int(rest[0]) {
			return 0, false
		}
		rest = rest[1+int(rest[0]):]
	}
	if flags&0x20 != 0 {
		if len(rest) < 2 {
			return 0, false
		}
		rest = rest[2:]
	}
	tag, body, ok = descriptor(rest)
	if !ok || tag != 0x04 || len(body) < 1 {
		return 0, false
	}
	return body[0], true
}

// descriptor splits one MPEG-4 descriptor: a tag, then a length in up to four
// 7-bit groups.
func descriptor(d []byte) (byte, []byte, bool) {
	if len(d) < 2 {
		return 0, nil, false
	}
	tag, n, i := d[0], 0, 1
	for ; i < len(d) && i <= 4; i++ {
		n = n<<7 | int(d[i]&0x7f)
		if d[i]&0x80 == 0 {
			i++
			break
		}
	}
	if n > len(d)-i {
		return 0, nil, false
	}
	return tag, d[i : i+n], true
}

// VideoThumbnail returns the JPEG preview a video message carries: the frame
// the sending command took with ffmpeg, checked to be a small JPEG and passed
// on unchanged, never decoded or re-encoded. With no frame it draws a plain
// one of the video's shape, so the message always has a preview.
func VideoThumbnail(frame []byte, info VideoInfo) ([]byte, error) {
	if len(frame) == 0 {
		return placeholderThumbnail(info)
	}
	if len(frame) > MaxThumbnailBytes {
		return nil, fmt.Errorf("preview frame exceeds %d KiB", MaxThumbnailBytes>>10)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(frame))
	if err != nil {
		return nil, fmt.Errorf("preview frame must be a JPEG")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > thumbnailMaxDim || cfg.Height > thumbnailMaxDim {
		return nil, fmt.Errorf("preview frame must be at most %d pixels on each side", thumbnailMaxDim)
	}
	return frame, nil
}

func placeholderThumbnail(info VideoInfo) ([]byte, error) {
	w, h := fitBox(int(info.Width), int(info.Height), thumbnailMaxDim)
	if w == 0 {
		w, h = thumbnailMaxDim, thumbnailMaxDim*9/16
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0x22
	}
	return encodeThumbnail(img)
}

func encodeThumbnail(img image.Image) ([]byte, error) {
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 70}); err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}
	return out.Bytes(), nil
}

// fitBox scales w×h to fit inside limit×limit, never enlarging it.
func fitBox(w, h, limit int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if w <= limit && h <= limit {
		return w, h
	}
	if w >= h {
		return limit, max(1, h*limit/w)
	}
	return max(1, w*limit/h), limit
}
