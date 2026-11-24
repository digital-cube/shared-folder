package thumbs

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
)

// A profile picture of an account (docs/admin.md, "Profilna slika"): the
// same decoders, EXIF orientation and scaling as a thumbnail, cut to a
// square from the middle.
const (
	// AvatarPx is the side of a profile picture.
	AvatarPx = 256
	// AvatarQuality is the JPEG quality of a profile picture.
	AvatarQuality = 85
	// AvatarMaxPixels bounds width × height of an uploaded picture, read
	// from its header before anything is decoded: a small file that
	// claims a huge canvas (a decompression bomb) is refused at once.
	AvatarMaxPixels = 40_000_000
)

// AvatarFormats are the formats a profile picture may come in, by the name
// image.DecodeConfig gives them. GIF gives its first frame.
var AvatarFormats = map[string]bool{"png": true, "jpeg": true, "webp": true, "gif": true}

// Reasons a picture is refused (errors.Is).
var (
	ErrAvatarFormat = errors.New("not a PNG, JPEG, WebP or GIF image")
	ErrAvatarPixels = errors.New("image has too many pixels")
)

// Avatar turns an uploaded picture into a profile picture: the format is
// read from the content (never from a name or a Content-Type), a picture
// over maxPixels is refused before it is decoded, the EXIF orientation of
// a JPEG is applied, the largest centred square is cut out and scaled to
// AvatarPx × AvatarPx, and the result is a JPEG of AvatarQuality without
// any metadata (image/jpeg writes none). Transparency becomes white.
func Avatar(data []byte, maxPixels int64) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("%w: decoder panic: %v", ErrAvatarFormat, r)
		}
	}()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !AvatarFormats[format] {
		return nil, ErrAvatarFormat
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("%w: it has no pixels", ErrAvatarFormat)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, fmt.Errorf("%w: %dx%d is over the limit of %d", ErrAvatarPixels, cfg.Width, cfg.Height, maxPixels)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrAvatarFormat, format, err)
	}
	orient := 1
	if format == "jpeg" {
		orient = exifOrientation(data)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, square(img, orient, AvatarPx), &jpeg.Options{Quality: AvatarQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// square cuts the largest centred square out of src, turns it upright by
// the EXIF orientation and scales it to px × px (a smaller one is
// enlarged). The middle of the picture stays the middle whichever way it
// is turned, so the cut is made on the stored pixels.
func square(src image.Image, orient, px int) image.Image {
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	sq := subImage(src, image.Rect(x0, y0, x0+side, y0+side))
	// As for thumbnails: a large source is first averaged down by a whole
	// factor to about twice the target, then resampled.
	base := flatten(sq, max(side/(2*px), 1))
	return resize(orientImage(base, orient), image.Pt(px, px))
}

// subImage is r of img, sharing its pixels when the type allows it.
func subImage(img image.Image, r image.Rectangle) image.Image {
	if s, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return s.SubImage(r)
	}
	return cropped{img, r}
}

type cropped struct {
	image.Image
	r image.Rectangle
}

func (c cropped) Bounds() image.Rectangle { return c.r }
