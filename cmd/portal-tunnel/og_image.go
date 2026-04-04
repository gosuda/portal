package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	ogBgColor    = color.RGBA{11, 17, 32, 255}
	ogWhite      = color.RGBA{248, 250, 252, 255}
	ogSlate      = color.RGBA{148, 163, 184, 255}
	ogSky        = color.RGBA{56, 189, 248, 255}
	ogPink       = color.RGBA{244, 114, 182, 255}
	ogDarkSlate  = color.RGBA{30, 41, 59, 255}
	ogCardBg     = color.RGBA{15, 23, 42, 255}
	ogGridColor  = color.RGBA{30, 41, 59, 255}
)

func loadFontFace(ttf []byte, size float64) font.Face {
	f, err := opentype.Parse(ttf)
	if err != nil {
		return nil
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil
	}
	return face
}

func drawTextCentered(img *image.RGBA, text string, cx, cy int, col color.Color, face font.Face) {
	if face == nil {
		return
	}
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: face,
	}
	bounds, _ := d.BoundString(text)
	w := (bounds.Max.X - bounds.Min.X).Ceil()
	d.Dot = fixed.P(cx-w/2, cy)
	d.DrawString(text)
}

func fillRect(img *image.RGBA, x, y, w, h int, col color.Color) {
	draw.Draw(img, image.Rect(x, y, x+w, y+h), image.NewUniform(col), image.Point{}, draw.Over)
}

func drawRoundedRectOutline(img *image.RGBA, x, y, w, h int, col color.Color) {
	for i := x; i < x+w; i++ {
		img.Set(i, y, col)
		img.Set(i, y+h-1, col)
	}
	for j := y; j < y+h; j++ {
		img.Set(x, j, col)
		img.Set(x+w-1, j, col)
	}
}

func drawGradientLine(img *image.RGBA, x1, x2, y, thickness int) {
	for x := x1; x < x2; x++ {
		t := float64(x-x1) / float64(x2-x1)
		var r, g, b uint8
		if t < 0.5 {
			tt := t * 2
			r = uint8(56 + tt*(129-56))
			g = uint8(189 + tt*(140-189))
			b = uint8(248 + tt*(248-248))
		} else {
			tt := (t - 0.5) * 2
			r = uint8(129 + tt*(244-129))
			g = uint8(140 + tt*(114-140))
			b = uint8(248 + tt*(182-248))
		}
		for dy := -thickness / 2; dy <= thickness/2; dy++ {
			alpha := uint8(255 - int(math.Abs(float64(dy))/float64(thickness/2+1)*100))
			img.Set(x, y+dy, color.RGBA{r, g, b, alpha})
		}
	}
}

func generateOGBannerPNG(w io.Writer, displayHost string) error {
	const width, height = 1200, 630

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(ogBgColor), image.Point{}, draw.Src)

	// Grid
	for x := 0; x < width; x += 40 {
		for y := 0; y < height; y++ {
			img.Set(x, y, ogGridColor)
		}
	}
	for y := 0; y < height; y += 40 {
		for x := 0; x < width; x++ {
			img.Set(x, y, ogGridColor)
		}
	}

	// Fonts
	titleFace := loadFontFace(gobold.TTF, 52)
	subtitleFace := loadFontFace(goregular.TTF, 22)
	urlFace := loadFontFace(gomono.TTF, 26)
	labelFace := loadFontFace(goregular.TTF, 18)
	monoSmall := loadFontFace(gomono.TTF, 18)

	// Left card - Local Environment
	fillRect(img, 100, 160, 300, 240, ogCardBg)
	drawRoundedRectOutline(img, 100, 160, 300, 240, ogDarkSlate)
	drawTextCentered(img, "Local Environment", 250, 210, ogWhite, labelFace)
	fillRect(img, 150, 240, 200, 100, ogDarkSlate)
	drawRoundedRectOutline(img, 150, 240, 200, 100, ogSky)
	drawTextCentered(img, "localhost:3000", 250, 295, ogSky, monoSmall)
	drawTextCentered(img, "Dev Server", 250, 320, ogSlate, labelFace)

	// Right card - Public Internet
	fillRect(img, 800, 160, 300, 240, ogCardBg)
	drawRoundedRectOutline(img, 800, 160, 300, 240, ogDarkSlate)
	drawTextCentered(img, "Public Internet", 950, 210, ogWhite, labelFace)
	fillRect(img, 850, 240, 200, 100, ogDarkSlate)
	drawRoundedRectOutline(img, 850, 240, 200, 100, ogPink)
	host := displayHost
	if len(host) > 28 {
		host = host[:28]
	}
	drawTextCentered(img, host, 950, 295, ogPink, monoSmall)
	drawTextCentered(img, "Public URL", 950, 320, ogSlate, labelFace)

	// Gradient connection line
	drawGradientLine(img, 400, 800, 290, 6)

	// Center circle
	cx, cy, cr := 600, 290, 40
	for x := cx - cr; x <= cx+cr; x++ {
		for y := cy - cr; y <= cy+cr; y++ {
			dx, dy := float64(x-cx), float64(y-cy)
			if dx*dx+dy*dy <= float64(cr*cr) {
				img.Set(x, y, ogCardBg)
			}
			dist := math.Abs(math.Sqrt(dx*dx+dy*dy) - float64(cr))
			if dist < 2 {
				img.Set(x, y, color.RGBA{129, 140, 248, 255})
			}
		}
	}

	// Title and subtitle
	drawTextCentered(img, "Portal by Gosuda", 600, 490, ogWhite, titleFace)
	drawTextCentered(img, "Secure tunneling from local development to the world", 600, 530, ogSlate, subtitleFace)

	// URL display
	drawTextCentered(img, "https://"+displayHost, 600, 580, ogPink, urlFace)

	return png.Encode(w, img)
}

func renderOGBannerPNG(displayHost string) ([]byte, error) {
	var buf bytes.Buffer
	if err := generateOGBannerPNG(&buf, displayHost); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
