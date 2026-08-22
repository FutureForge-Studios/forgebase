package main

// Image transformations: ?width=&height= on object reads resizes JPEG/PNG on
// the fly with a bilinear scaler (standard library only) and caches each
// rendition on disk, so repeat views cost a file read. Aspect ratio is
// preserved by fitting inside the requested box.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const renditionRoot = "/opt/pgforge-storage/.renditions"

// bilinearResize scales src to w x h with bilinear sampling.
func bilinearResize(src image.Image, w, h int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		fy := (float64(y) + 0.5) * float64(sh) / float64(h)
		y0 := int(fy - 0.5)
		if y0 < 0 {
			y0 = 0
		}
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		wy := fy - 0.5 - float64(y0)
		for x := 0; x < w; x++ {
			fx := (float64(x) + 0.5) * float64(sw) / float64(w)
			x0 := int(fx - 0.5)
			if x0 < 0 {
				x0 = 0
			}
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			wx := fx - 0.5 - float64(x0)
			r00, g00, b00, a00 := src.At(b.Min.X+x0, b.Min.Y+y0).RGBA()
			r10, g10, b10, a10 := src.At(b.Min.X+x1, b.Min.Y+y0).RGBA()
			r01, g01, b01, a01 := src.At(b.Min.X+x0, b.Min.Y+y1).RGBA()
			r11, g11, b11, a11 := src.At(b.Min.X+x1, b.Min.Y+y1).RGBA()
			lerp2 := func(a, b, c, d uint32) uint8 {
				top := float64(a)*(1-wx) + float64(b)*wx
				bot := float64(c)*(1-wx) + float64(d)*wx
				return uint8(uint32(top*(1-wy)+bot*wy) >> 8)
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = lerp2(r00, r10, r01, r11)
			dst.Pix[i+1] = lerp2(g00, g10, g01, g11)
			dst.Pix[i+2] = lerp2(b00, b10, b01, b11)
			dst.Pix[i+3] = lerp2(a00, a10, a01, a11)
		}
	}
	return dst
}

// fitBox returns the target size fitting (sw,sh) inside (mw,mh), preserving
// aspect; zero means unconstrained on that axis.
func fitBox(sw, sh, mw, mh int) (int, int) {
	if mw <= 0 && mh <= 0 {
		return sw, sh
	}
	rw, rh := 1.0, 1.0
	if mw > 0 && sw > mw {
		rw = float64(mw) / float64(sw)
	}
	if mh > 0 && sh > mh {
		rh = float64(mh) / float64(sh)
	}
	r := rw
	if rh < r {
		r = rh
	}
	w, h := int(float64(sw)*r), int(float64(sh)*r)
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// serveTransformed serves a resized rendition of an image object, or reports
// false so the caller falls through to the original file.
func (a *app) serveTransformed(w http.ResponseWriter, r *http.Request, slug, bucket, path, full, mime string) bool {
	wq, _ := strconv.Atoi(r.URL.Query().Get("width"))
	hq, _ := strconv.Atoi(r.URL.Query().Get("height"))
	if wq <= 0 && hq <= 0 {
		return false
	}
	if wq > 4096 || hq > 4096 || (mime != "image/jpeg" && mime != "image/png") {
		return false
	}
	key := fmt.Sprintf("%s_%s_%s_%dx%d", slug, bucket, strings.ReplaceAll(path, "/", "_"), wq, hq)
	if len(key) > 200 {
		sum := sha256.Sum256([]byte(key))
		key = key[:120] + hex.EncodeToString(sum[:])[:24]
	}
	ext := ".jpg"
	if mime == "image/png" {
		ext = ".png"
	}
	cached := filepath.Join(renditionRoot, key+ext)
	if _, err := os.Stat(cached); err != nil {
		f, err := os.Open(full)
		if err != nil {
			return false
		}
		src, _, derr := image.Decode(f)
		f.Close()
		if derr != nil {
			return false
		}
		b := src.Bounds()
		tw, th := fitBox(b.Dx(), b.Dy(), wq, hq)
		if tw == b.Dx() && th == b.Dy() {
			return false // no downscale needed; serve the original
		}
		out := bilinearResize(src, tw, th)
		os.MkdirAll(renditionRoot, 0o755)
		tmp := cached + ".part"
		g, err := os.Create(tmp)
		if err != nil {
			return false
		}
		var eerr error
		if mime == "image/png" {
			eerr = png.Encode(g, out)
		} else {
			eerr = jpeg.Encode(g, out, &jpeg.Options{Quality: 82})
		}
		g.Close()
		if eerr != nil {
			os.Remove(tmp)
			return false
		}
		os.Rename(tmp, cached)
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, cached)
	return true
}
