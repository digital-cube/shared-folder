package thumbs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"time"

	// Decoders of the image kinds (KindOf); all pure Go.
	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Default limits of a source (Limits).
const (
	DefaultMaxImageBytes = 64 << 20
	DefaultMaxPixels     = 64_000_000
	DefaultMaxPDFBytes   = 256 << 20
	DefaultPDFTimeout    = 30 * time.Second
	// JPEGQuality is the quality of every thumbnail.
	JPEGQuality = 82
)

// Limits bound what a source may cost. Over a limit the hash gets a .fail
// marker.
type Limits struct {
	MaxImageBytes int64         // size of an image file
	MaxPixels     int64         // width × height of an image, from its header
	MaxPDFBytes   int64         // size of a PDF file
	PDFTimeout    time.Duration // pdftoppm of one PDF, killed with its process group after it
}

// DefaultLimits are the limits shfd thumbs uses without flags.
func DefaultLimits() Limits {
	return Limits{MaxImageBytes: DefaultMaxImageBytes, MaxPixels: DefaultMaxPixels, MaxPDFBytes: DefaultMaxPDFBytes, PDFTimeout: DefaultPDFTimeout}
}

// Generator makes thumbnails.
type Generator struct {
	Limits Limits
	// PDFToPPM is the pdftoppm executable (poppler-utils); "" means PDFs
	// cannot be done.
	PDFToPPM string
}

// Outcome of one job.
type Outcome string

// Outcomes.
const (
	Ready   Outcome = "ready"   // every size written
	Failed  Outcome = "failed"  // a .fail marker written; not tried again
	Skipped Outcome = "skipped" // the source is not what the job said; nothing written
)

// Result is what Generate did.
type Result struct {
	Outcome Outcome
	// Reason says why it failed or was skipped. It holds no path or
	// content of the file, so it may be logged and stored.
	Reason string
}

// failure is an error whose text becomes the .fail marker.
type failure struct{ msg string }

func (f failure) Error() string { return f.msg }

func failf(format string, a ...any) error { return failure{fmt.Sprintf(format, a...)} }

// errSkip means the source changed under the job.
var errSkip = errors.New("skip")

// Generate makes every size of the thumbnail of src, whose content the job
// says hashes to hash, into st. While it reads src it computes the
// SHA-256; when that differs the file changed since the job was handed out
// and the job is skipped without a trace (a new job with the new hash
// follows). A source over the limits, one that does not decode (or panics
// the decoder) and a PDF that times out get a .fail marker. A .work marker
// stands while the thumbnail is made, so a source that kills the process
// is not tried again (Store.RecoverWork).
//
// The error is for the store itself failing (disk full, permissions);
// such a job is neither done nor failed.
func (g *Generator) Generate(ctx context.Context, st Store, src *os.File, hash string, kind Kind) (Result, error) {
	if !ValidHash(hash) {
		return Result{}, fmt.Errorf("invalid hash %q", hash)
	}
	if kind == KindPDF && g.PDFToPPM == "" {
		return Result{}, errors.New("no pdftoppm for a PDF job")
	}
	dir := st.sub(hash)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(st.workPath(hash), nil, 0o600); err != nil {
		return Result{}, err
	}
	defer os.Remove(st.workPath(hash))

	imgs, err := g.render(ctx, src, hash, kind)
	if errors.Is(err, errSkip) {
		return Result{Outcome: Skipped, Reason: "content does not match the job's hash"}, nil
	}
	var f failure
	if errors.As(err, &f) {
		return g.fail(st, hash, f.msg)
	}
	if err != nil {
		return Result{}, err
	}
	for i, s := range Sizes {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, imgs[i], &jpeg.Options{Quality: JPEGQuality}); err != nil {
			return g.fail(st, hash, "jpeg encoding: "+err.Error())
		}
		if err := writeAtomic(dir, hash+"."+s.Name+".jpg", buf.Bytes()); err != nil {
			return Result{}, err
		}
	}
	os.Remove(st.failPath(hash))
	return Result{Outcome: Ready}, nil
}

func (g *Generator) fail(st Store, hash, reason string) (Result, error) {
	for _, s := range Sizes {
		os.Remove(st.Path(hash, s.Name))
	}
	if err := writeAtomic(st.sub(hash), hash+failSuffix, []byte(reason+"\n")); err != nil {
		return Result{}, err
	}
	return Result{Outcome: Failed, Reason: reason}, nil
}

// render reads and checks the source and returns one image per Size.
func (g *Generator) render(ctx context.Context, src *os.File, hash string, kind Kind) (imgs []image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			imgs, err = nil, failf("decoder panic: %v", r)
		}
	}()
	switch kind {
	case KindImage:
		data, over, err := readHashed(src, hash, g.Limits.MaxImageBytes)
		if err != nil {
			return nil, err
		}
		if over {
			return nil, failf("image larger than %d bytes", g.Limits.MaxImageBytes)
		}
		return g.image(data)
	case KindPDF:
		_, over, err := readHashed(src, hash, 0)
		if err != nil {
			return nil, err
		}
		if fi, err := src.Stat(); err == nil && fi.Size() > g.Limits.MaxPDFBytes {
			over = true
		}
		if over {
			return nil, failf("PDF larger than %d bytes", g.Limits.MaxPDFBytes)
		}
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		img, err := g.pdfFirstPage(ctx, src)
		if err != nil {
			return nil, err
		}
		return scaleAll(img, 1), nil
	}
	return nil, fmt.Errorf("unknown kind %q", kind)
}

// readHashed reads src to the end, hashing it; it keeps up to max bytes
// in memory (none when max is 0) and says whether there were more. A
// hash that differs is errSkip.
func readHashed(src *os.File, hash string, max int64) (data []byte, over bool, err error) {
	h := sha256.New()
	var buf bytes.Buffer
	if max > 0 {
		n, err := io.Copy(io.MultiWriter(h, &buf), io.LimitReader(src, max+1))
		if err != nil {
			return nil, false, readErr(err)
		}
		over = n > max
	}
	if _, err := io.Copy(h, src); err != nil {
		return nil, false, readErr(err)
	}
	if hex.EncodeToString(h.Sum(nil)) != hash {
		return nil, false, errSkip
	}
	if over {
		return nil, true, nil
	}
	return buf.Bytes(), false, nil
}

// readErr keeps the path out of a read error, which may become a .fail.
func readErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return failf("read: %v", pe.Err)
	}
	return failf("read: %v", err)
}

func (g *Generator) image(data []byte) ([]image.Image, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, failf("not a readable image: %v", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, failf("image has no pixels (%dx%d)", cfg.Width, cfg.Height)
	}
	if int64(cfg.Width)*int64(cfg.Height) > g.Limits.MaxPixels {
		return nil, failf("image of %dx%d pixels is over the limit of %d", cfg.Width, cfg.Height, g.Limits.MaxPixels)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, failf("%s: %v", format, err)
	}
	orient := 1
	if format == "jpeg" {
		orient = exifOrientation(data)
	}
	return scaleAll(img, orient), nil
}
