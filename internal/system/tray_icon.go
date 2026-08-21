//go:build windows

// Decode the application icon (appicon.png) in pure Go and build an
// in-memory HICON via CreateIconIndirect. We deliberately avoid
// LoadImageW + MAKEINTRESOURCE here: that path crashed with an access
// violation when the module handle / embedded resource didn't line up
// (Go 1.25 cgocall on a baked .ico), which made enabling the tray kill
// the whole process. Building the icon from decoded RGBA bytes is fully
// self-contained and safe on every build (plain `go build` or `wails
// build`).

package system

import (
	"bytes"
	"image"
	"image/draw"
	_ "image/png"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	gdi32             = windows.NewLazySystemDLL("gdi32.dll")
	procCreateDIBsect  = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap   = gdi32.NewProc("CreateBitmap")
	procDeleteObject   = gdi32.NewProc("DeleteObject")
	procCreateIconIndir = user32.NewProc("CreateIconIndirect")
)

const (
	dibRGBColors = 0 // uUsage for CreateDIBSection
	iconDim      = 32
)

// bitmapInfoHEADER mirrors BITMAPINFOHEADER (subset used by
// CreateDIBSection).
type bitmapInfoHEADER struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

// iconInfo mirrors ICONINFO for CreateIconIndirect.
type iconInfo struct {
	FIcon    uint32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

// trayIcon returns an HICON for the tray. It builds it from the PNG
// bytes if present, otherwise falls back to the stock application
// icon. Returns 0 only as a last resort (caller treats it as "no
// icon").
func trayIcon(png []byte) uintptr {
	if h := createIconFromPNG(png); h != 0 {
		return h
	}
	h, _, _ := procLoadIconW.Call(0, uintptr(idiApp))
	return h
}

// createIconFromPNG decodes a PNG into a 32×32 32-bpp DIB plus a 1-bpp
// AND mask, then builds an HICON. Returns 0 on any failure so the
// caller can fall back.
func createIconFromPNG(png []byte) uintptr {
	if len(png) == 0 {
		return 0
	}
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return 0
	}
	src := imageToRGBA(img)
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	if sw == 0 || sh == 0 {
		return 0
	}

	// Box-sample the source into a 32×32 BGRA buffer (straight alpha).
	px := make([]byte, iconDim*iconDim*4)
	for y := 0; y < iconDim; y++ {
		for x := 0; x < iconDim; x++ {
			sx := int((float64(x) + 0.5) / float64(iconDim) * float64(sw))
			sy := int((float64(y) + 0.5) / float64(iconDim) * float64(sh))
			if sx >= sw {
				sx = sw - 1
			}
			if sy >= sh {
				sy = sh - 1
			}
			i := sy*src.Stride + sx*4
			o := (y*iconDim + x) * 4
			px[o], px[o+1], px[o+2], px[o+3] = src.Pix[i+2], src.Pix[i+1], src.Pix[i], src.Pix[i+3]
		}
	}

	// 32-bpp top-down DIB for the colour bitmap.
	var bi bitmapInfoHEADER
	bi.BiSize = uint32(unsafe.Sizeof(bi))
	bi.BiWidth = iconDim
	bi.BiHeight = -int32(iconDim) // negative → top-down
	bi.BiPlanes = 1
	bi.BiBitCount = 32
	var bits unsafe.Pointer
	hbmColor, _, _ := procCreateDIBsect.Call(
		0,
		uintptr(unsafe.Pointer(&bi)),
		uintptr(dibRGBColors),
		uintptr(unsafe.Pointer(&bits)),
		0, 0,
	)
	if hbmColor == 0 || bits == nil {
		return 0
	}
	dst := unsafe.Slice((*byte)(bits), iconDim*iconDim*4)
	copy(dst, px)

	// 1-bpp AND mask: bit=1 where the pixel is transparent (alpha <
	// 128). 32 px/row = 4 bytes, no scanline padding needed.
	mask := make([]byte, iconDim*iconDim/8)
	for p := 0; p < iconDim*iconDim; p++ {
		if px[p*4+3] < 128 {
			mask[p/8] |= 1 << (7 - uint(p%8))
		}
	}
	hbmMask, _, _ := procCreateBitmap.Call(iconDim, iconDim, 1, 1, uintptr(unsafe.Pointer(&mask[0])))
	if hbmMask == 0 {
		procDeleteObject.Call(hbmColor)
		return 0
	}

	var ii iconInfo
	ii.FIcon = 1
	ii.HbmMask = hbmMask
	ii.HbmColor = hbmColor
	hic, _, _ := procCreateIconIndir.Call(uintptr(unsafe.Pointer(&ii)))

	// The created icon holds its own copy; release the source bitmaps.
	procDeleteObject.Call(hbmColor)
	procDeleteObject.Call(hbmMask)
	return hic
}

// imageToRGBA normalises an arbitrary image.Image to an *image.RGBA.
func imageToRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	return rgba
}