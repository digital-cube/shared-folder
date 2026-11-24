package thumbs

import (
	"image"
	"image/color"

	"golang.org/x/image/draw"
)

// scaleAll returns one image per Size, largest first: the source flattened
// onto white, turned upright by the EXIF orientation and scaled so that
// its longer side fits the size, keeping proportions and never enlarging.
func scaleAll(src image.Image, orient int) []image.Image {
	l := Sizes[0].Px
	// A large source is first averaged down by a whole factor to about
	// twice the largest size: a resampling kernel over 64 megapixels
	// would take seconds, a box filter takes one pass.
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if orient >= 5 && orient <= 8 { // a quarter turn swaps the sides
		w, h = h, w
	}
	k := max(w, h) / (2 * l)
	base := flatten(src, max(k, 1))
	base = orientImage(base, orient)
	out := make([]image.Image, len(Sizes))
	prev := image.Image(base)
	for i, s := range Sizes {
		// The dimensions come from the source, not from the image before
		// (whose sides were rounded already).
		prev = resize(prev, fitDims(w, h, s.Px))
		out[i] = prev
	}
	return out
}

// fitDims are w×h scaled so that the longer side is at most px, rounded,
// at least 1×1, never enlarged.
func fitDims(w, h, px int) image.Point {
	if w <= px && h <= px {
		return image.Pt(w, h)
	}
	if w >= h {
		return image.Pt(px, max(1, int(float64(h)*float64(px)/float64(w)+0.5)))
	}
	return image.Pt(max(1, int(float64(w)*float64(px)/float64(h)+0.5)), px)
}

// resize scales img to exactly d unless it has that size already.
func resize(img image.Image, d image.Point) image.Image {
	b := img.Bounds()
	if b.Size() == d {
		return img
	}
	dst := image.NewRGBA(image.Rectangle{Max: d})
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// flatten averages k×k blocks of src (k = 1 copies) into an opaque RGBA
// image, compositing transparent pixels onto white.
func flatten(src image.Image, k int) *image.RGBA {
	b := src.Bounds()
	w, h := (b.Dx()+k-1)/k, (b.Dy()+k-1)/k
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// Sums of premultiplied 16-bit channels for one row of output.
	type acc struct{ r, g, b, a, n uint64 }
	row := make([]acc, w)
	at := pixelReader(src)
	for oy := 0; oy < h; oy++ {
		clear(row)
		y0 := b.Min.Y + oy*k
		y1 := min(y0+k, b.Max.Y)
		for y := y0; y < y1; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, a := at(x, y)
				c := &row[(x-b.Min.X)/k]
				c.r += uint64(r)
				c.g += uint64(g)
				c.b += uint64(bl)
				c.a += uint64(a)
				c.n++
			}
		}
		for ox := range row {
			c := row[ox]
			// Over white: premultiplied colour + (1 - alpha) × white.
			white := c.n*0xffff - c.a
			i := dst.PixOffset(ox, oy)
			dst.Pix[i+0] = uint8(((c.r + white) / c.n) >> 8)
			dst.Pix[i+1] = uint8(((c.g + white) / c.n) >> 8)
			dst.Pix[i+2] = uint8(((c.b + white) / c.n) >> 8)
			dst.Pix[i+3] = 0xff
		}
	}
	return dst
}

// pixelReader returns a function giving the premultiplied 16-bit RGBA of
// a pixel, without the allocation of At for the common decoded types.
func pixelReader(src image.Image) func(x, y int) (r, g, b, a uint32) {
	switch m := src.(type) {
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			yi, ci := m.YOffset(x, y), m.COffset(x, y)
			return color.YCbCr{Y: m.Y[yi], Cb: m.Cb[ci], Cr: m.Cr[ci]}.RGBA()
		}
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			i := m.PixOffset(x, y)
			p := m.Pix[i : i+4 : i+4]
			return color.NRGBA{R: p[0], G: p[1], B: p[2], A: p[3]}.RGBA()
		}
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			i := m.PixOffset(x, y)
			p := m.Pix[i : i+4 : i+4]
			return color.RGBA{R: p[0], G: p[1], B: p[2], A: p[3]}.RGBA()
		}
	case *image.Gray:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			return color.Gray{Y: m.Pix[m.PixOffset(x, y)]}.RGBA()
		}
	}
	return func(x, y int) (uint32, uint32, uint32, uint32) { return src.At(x, y).RGBA() }
}

// orientImage turns img upright according to an EXIF orientation (1–8);
// other values leave it as it is.
func orientImage(img *image.RGBA, orient int) *image.RGBA {
	if orient < 2 || orient > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := orient >= 5
	dw, dh := w, h
	if swap {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orient {
			case 2: // mirrored horizontally
				dx, dy = w-1-x, y
			case 3: // rotated 180°
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored vertically
				dx, dy = x, h-1-y
			case 5: // transposed
				dx, dy = y, x
			case 6: // rotate 90° clockwise to view
				dx, dy = h-1-y, x
			case 7: // transversed
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 90° counter-clockwise to view
				dx, dy = y, w-1-x
			}
			si, di := img.PixOffset(b.Min.X+x, b.Min.Y+y), dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], img.Pix[si:si+4])
		}
	}
	return dst
}
