package thumbs

import "encoding/binary"

// exifOrientation reads the Orientation tag (0x0112) of IFD0 from the
// APP1 "Exif" segment of a JPEG; 1 (as stored) when there is none or the
// data is not understood. Phones save portrait photos as landscape pixels
// with orientation 6 or 8.
func exifOrientation(jpg []byte) int {
	if len(jpg) < 4 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		return 1
	}
	p := 2
	for p+4 <= len(jpg) {
		if jpg[p] != 0xFF {
			return 1
		}
		marker := jpg[p+1]
		if marker == 0xFF { // fill byte
			p++
			continue
		}
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			p += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // image data: no more headers
			return 1
		}
		n := int(binary.BigEndian.Uint16(jpg[p+2:]))
		if n < 2 || p+2+n > len(jpg) {
			return 1
		}
		seg := jpg[p+4 : p+2+n]
		if marker == 0xE1 && len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
			if o := tiffOrientation(seg[6:]); o != 0 {
				return o
			}
		}
		p += 2 + n
	}
	return 1
}

// tiffOrientation reads the orientation from a TIFF header and its IFD0;
// 0 when it is not there.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(t[2:]) != 42 {
		return 0
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return 0
	}
	count := int(bo.Uint16(t[ifd:]))
	for i := 0; i < count; i++ {
		e := ifd + 2 + 12*i
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) != 0x0112 {
			continue
		}
		if bo.Uint16(t[e+2:]) != 3 { // SHORT
			return 0
		}
		v := int(bo.Uint16(t[e+8:]))
		if v < 1 || v > 8 {
			return 0
		}
		return v
	}
	return 0
}
