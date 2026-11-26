package screen

import (
	"image"
	"image/color"
	"testing"
)

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func TestOnlyChangedTilesAreSent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	fill(img, img.Bounds(), color.RGBA{20, 40, 60, 255})
	var e Encoder
	h, p, ok, err := e.Encode(img, 0, false)
	if err != nil || !ok || !h.Full {
		t.Fatal("first frame must be full")
	}
	want := 5 * 4 // ceil(300/64) * ceil(200/64)
	if len(h.Tiles) != want {
		t.Fatalf("tiles %d want %d", len(h.Tiles), want)
	}
	var c Canvas
	if _, err := c.Apply(h, p); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := e.Encode(img, 0, false); ok {
		t.Fatal("unchanged frame should send nothing")
	}
	fill(img, image.Rect(70, 70, 80, 80), color.RGBA{250, 250, 250, 255})
	h, p, ok, _ = e.Encode(img, 0, false)
	if !ok || len(h.Tiles) != 1 || h.Tiles[0].X != 64 || h.Tiles[0].Y != 64 {
		t.Fatalf("expected one changed tile, got %+v", h.Tiles)
	}
	dirty, err := c.Apply(h, p)
	if err != nil || dirty != image.Rect(64, 64, 128, 128) {
		t.Fatalf("dirty %v err %v", dirty, err)
	}
	got := c.Img.RGBAAt(75, 75)
	if got.R < 230 {
		t.Fatalf("pixel not updated: %v", got)
	}
}

func TestCanvasRejectsBadFrames(t *testing.T) {
	var c Canvas
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var e Encoder
	h, p, _, _ := e.Encode(img, 0, true)
	h.Tiles[0].X = 1000
	if _, err := c.Apply(h, p); err == nil {
		t.Fatal("tile outside frame accepted")
	}
	h.Tiles[0].X = 0
	h.Tiles[0].Len = len(p) + 1
	if _, err := c.Apply(h, p); err == nil {
		t.Fatal("bad length accepted")
	}
	h.W = 0
	if _, err := c.Apply(h, p); err == nil {
		t.Fatal("bad size accepted")
	}
}

func TestFitAndMapping(t *testing.T) {
	r := Fit(1920, 1080, 960, 1000)
	if r.Dx() != 960 || r.Dy() != 540 || r.Min.Y != 230 {
		t.Fatalf("fit %v", r)
	}
	x, y, ok := ToRemote(r.Min.X+480, r.Min.Y+270, r, 1920, 1080)
	if !ok || x != 960 || y != 540 {
		t.Fatalf("map %d,%d", x, y)
	}
	if _, _, ok := ToRemote(5, 5, r, 1920, 1080); ok {
		t.Fatal("point outside picture mapped")
	}
}
