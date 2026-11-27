// Command assetgen draws the wasync app's pictures from logo D
// (packaging/desktop/logo.svg) and writes them where the build expects them.
// The results are committed, so building wasync never needs this tool; run it
// again on a Mac after changing the logo:
//
//	cd packaging/desktop/assetgen
//	GOWORK=off go run . -font-regular Inter-Regular.ttf -font-semibold Inter-SemiBold.ttf
//
// The Inter font files come from https://rsms.me/inter (Open Font License).
// They are only drawn into the disk-image background, not shipped.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/leaanthony/dmg/icns"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"github.com/tc-hib/winres"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	green     = color.RGBA{0x12, 0x82, 0x54, 0xff}
	ink       = color.RGBA{0x1f, 0x2a, 0x26, 0xff}
	muted     = color.RGBA{0x5f, 0x6b, 0x66, 0xff}
	paper     = color.RGBA{0xf6, 0xf8, 0xf7, 0xff}
	repoRoot  = filepath.Join("..", "..", "..")
	desktop   = filepath.Join(repoRoot, "packaging", "desktop")
	appIcons  = filepath.Join(repoRoot, "cmd", "wasync-desktop", "icons")
	macApp    = filepath.Join(repoRoot, "packaging", "macos", "app")
	macDMG    = filepath.Join(repoRoot, "packaging", "macos", "dmg")
	windowsPk = filepath.Join(repoRoot, "packaging", "windows")
)

func main() {
	regular := flag.String("font-regular", "", "Inter Regular .ttf")
	semibold := flag.String("font-semibold", "", "Inter SemiBold .ttf")
	flag.Parse()
	if *regular == "" || *semibold == "" {
		log.Fatal("pass -font-regular and -font-semibold (Inter, from https://rsms.me/inter)")
	}
	logo := read(filepath.Join(desktop, "logo.svg"))
	glyph := read(filepath.Join(desktop, "tray-template.svg"))

	// The app's own icon, and the icons its window and tray use.
	writePNG(filepath.Join(appIcons, "app.png"), logoAt(logo, 512))
	writePNG(filepath.Join(appIcons, "tray.png"), logoAt(logo, 64))
	writePNG(filepath.Join(appIcons, "tray-template.png"), template(render(glyph, 44, 44)))
	data, err := icns.IcnsFromImage(macIcon(logo))
	check(err)
	write(filepath.Join(macApp, "wasync.icns"), data)

	// Windows: the program icon, and the pictures Setup shows.
	ico := icoFile(logo, 16, 24, 32, 48, 64, 128, 256)
	write(filepath.Join(windowsPk, "wasync.ico"), ico)
	syso(logo)
	// Inno Setup's sizes for 100% to 250% display scaling: logo D centred
	// across and down on the green side picture, and the small corner picture.
	for i, size := range [][2]int{{164, 314}, {192, 386}, {246, 459}, {273, 556}, {328, 604}, {355, 700}, {410, 797}} {
		writeBMP(filepath.Join(windowsPk, fmt.Sprintf("wizard-%d.bmp", i)), sidePicture(logo, size[0], size[1]))
	}
	for i, size := range []int{55, 64, 83, 92, 110, 119, 138} {
		writeBMP(filepath.Join(windowsPk, fmt.Sprintf("wizard-small-%d.bmp", i)), cornerPicture(logo, size))
	}

	// The disk image window: drag wasync to Applications, and the byline.
	faces := loadFaces(*regular, *semibold)
	one := filepath.Join(os.TempDir(), "wasync-dmg-background.png")
	two := filepath.Join(os.TempDir(), "wasync-dmg-background@2x.png")
	writePNG(one, dmgBackground(faces, 1))
	writePNG(two, dmgBackground(faces, 2))
	check(os.MkdirAll(macDMG, 0o755))
	out, err := exec.Command("tiffutil", "-cathidpicheck", one, two, "-out", filepath.Join(macDMG, "background.tiff")).CombinedOutput()
	if err != nil {
		log.Fatalf("tiffutil: %v: %s", err, out)
	}
	fmt.Println("assets written")
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func read(path string) []byte {
	b, err := os.ReadFile(path)
	check(err)
	return b
}

func write(path string, data []byte) {
	check(os.MkdirAll(filepath.Dir(path), 0o755))
	check(os.WriteFile(path, data, 0o644))
}

// render draws an SVG at w x h on a transparent background. It draws at
// 1024 pixels and scales down, which keeps strokes in proportion at small sizes.
func render(svg []byte, w, h int) *image.RGBA {
	const big = 1024
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svg))
	check(err)
	icon.SetTarget(0, 0, big, big)
	full := image.NewRGBA(image.Rect(0, 0, big, big))
	icon.Draw(rasterx.NewDasher(big, big, rasterx.NewScannerGV(big, big, full, full.Bounds())), 1)
	if w == big && h == big {
		return full
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(img, img.Bounds(), full, full.Bounds(), xdraw.Src, nil)
	return img
}

// rounded cuts logo D's square to its rounded corners (radius 116 of 512),
// which the SVG library does not draw.
func rounded(img *image.RGBA) *image.RGBA {
	w := float64(img.Bounds().Dx())
	r := w * 116 / 512
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			cx, cy := math.Max(r-float64(x)-0.5, float64(x)+0.5-(w-r)), math.Max(r-float64(y)-0.5, float64(y)+0.5-(w-r))
			if cx <= 0 || cy <= 0 {
				continue
			}
			cover := math.Min(1, math.Max(0, r-math.Hypot(cx, cy)+0.5))
			i := img.PixOffset(x, y)
			for k := 0; k < 4; k++ {
				img.Pix[i+k] = uint8(float64(img.Pix[i+k]) * cover)
			}
		}
	}
	return img
}

// logoAt is logo D, rounded, at size x size.
func logoAt(svg []byte, size int) *image.RGBA { return rounded(render(svg, size, size)) }

// macIcon puts logo D on Apple's icon grid: an 824-pixel body centred in 1024.
func macIcon(svg []byte) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	body := logoAt(svg, 824)
	draw.Draw(img, body.Bounds().Add(image.Pt(100, 100)), body, image.Point{}, draw.Over)
	return img
}

// template turns a black-on-white drawing into a menu-bar template image:
// black with the drawing's darkness as its opacity.
func template(img *image.RGBA) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	for i := 0; i < len(img.Pix); i += 4 {
		// Premultiplied: opaque black gives 255, white or nothing gives 0.
		lum := (299*int(img.Pix[i]) + 587*int(img.Pix[i+1]) + 114*int(img.Pix[i+2])) / 1000
		out.Pix[i+3] = uint8(max(0, int(img.Pix[i+3])-lum))
	}
	return out
}

func encodePNG(img image.Image) []byte {
	var b bytes.Buffer
	check(png.Encode(&b, img))
	return b.Bytes()
}

func writePNG(path string, img image.Image) { write(path, encodePNG(img)) }

// icoFile is a Windows icon holding PNG pictures at each size.
func icoFile(svg []byte, sizes ...int) []byte {
	var head, body bytes.Buffer
	binary.Write(&head, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for _, s := range sizes {
		data := encodePNG(logoAt(svg, s))
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		head.Write([]byte{dim, dim, 0, 0})
		binary.Write(&head, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&head, binary.LittleEndian, [2]uint32{uint32(len(data)), uint32(offset)})
		offset += len(data)
		body.Write(data)
	}
	return append(head.Bytes(), body.Bytes()...)
}

// syso writes the Windows resources linked into wasync-desktop.exe: its icon,
// and a manifest that asks for sharp text at every display scale and the
// current look of Windows controls.
func syso(svg []byte) {
	rs := &winres.ResourceSet{}
	var images []image.Image
	for _, s := range []int{16, 24, 32, 48, 64, 128, 256} {
		images = append(images, logoAt(svg, s))
	}
	icon, err := winres.NewIconFromImages(images)
	check(err)
	check(rs.SetIcon(winres.Name("APPICON"), icon))
	rs.SetManifest(winres.AppManifest{
		Identity:            winres.AssemblyIdentity{Name: "wasync"},
		Description:         "wasync",
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
		ExecutionLevel:      winres.AsInvoker,
	})
	var b bytes.Buffer
	check(rs.WriteObject(&b, winres.ArchAMD64))
	write(filepath.Join(repoRoot, "cmd", "wasync-desktop", "rsrc_windows_amd64.syso"), b.Bytes())
}

func fill(img draw.Image, c color.Color) {
	draw.Draw(img, img.Bounds(), &image.Uniform{c}, image.Point{}, draw.Src)
}

// sidePicture is Setup's tall welcome and finish picture.
func sidePicture(svg []byte, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, green)
	s := int(math.Round(float64(w) * 0.62))
	logo := logoAt(svg, s)
	at := image.Pt((w-s)/2, (h-s)/2)
	draw.Draw(img, logo.Bounds().Add(at), logo, image.Point{}, draw.Over)
	return img
}

// cornerPicture is Setup's small picture at the top of the inner pages.
func cornerPicture(svg []byte, size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	fill(img, color.White)
	s := size * 9 / 10
	logo := logoAt(svg, s)
	at := image.Pt((size-s)/2, (size-s)/2)
	draw.Draw(img, logo.Bounds().Add(at), logo, image.Point{}, draw.Over)
	return img
}

// writeBMP writes a 24-bit Windows bitmap, the format Inno Setup reads everywhere.
func writeBMP(path string, img *image.RGBA) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	stride := (w*3 + 3) &^ 3
	var b bytes.Buffer
	b.WriteString("BM")
	binary.Write(&b, binary.LittleEndian, []uint32{uint32(54 + stride*h), 0, 54, 40, uint32(w), uint32(h)})
	binary.Write(&b, binary.LittleEndian, []uint16{1, 24})
	binary.Write(&b, binary.LittleEndian, []uint32{0, uint32(stride * h), 2835, 2835, 0, 0})
	row := make([]byte, stride)
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, y)
			row[x*3], row[x*3+1], row[x*3+2] = c.B, c.G, c.R
		}
		b.Write(row)
	}
	write(path, b.Bytes())
}

type faces struct{ regular, semibold *opentype.Font }

func loadFaces(regular, semibold string) faces {
	parse := func(path string) *opentype.Font {
		f, err := opentype.Parse(read(path))
		check(err)
		return f
	}
	return faces{parse(regular), parse(semibold)}
}

func face(f *opentype.Font, size float64, scale int) font.Face {
	ff, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size * float64(scale), DPI: 72, Hinting: font.HintingNone})
	check(err)
	return ff
}

// centred draws text centred across the picture with its baseline at y.
func centred(img draw.Image, f font.Face, c color.Color, text string, y int) {
	d := &font.Drawer{Dst: img, Src: &image.Uniform{c}, Face: f}
	width := d.MeasureString(text)
	d.Dot = fixed.Point26_6{X: (fixed.I(img.Bounds().Dx()) - width) / 2, Y: fixed.I(y)}
	d.DrawString(text)
}

// DMGWidth and DMGHeight are the disk image window's size in points, and
// DMGAppX, DMGLinkX and DMGIconY where its two icons sit; scripts/make-dmg
// uses the same numbers.
const (
	DMGWidth, DMGHeight = 660, 400
	DMGAppX, DMGLinkX   = 180, 480
	DMGIconY            = 205
)

// dmgBackground is the disk image window's picture at scale 1 or 2.
func dmgBackground(fs faces, scale int) *image.RGBA {
	w, h := DMGWidth*scale, DMGHeight*scale
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, paper)
	centred(img, face(fs.semibold, 20, scale), ink, "Drag wasync to Applications", 64*scale)
	centred(img, face(fs.regular, 13, scale), muted, "Then open wasync from your Applications folder.", 90*scale)
	arrow(img, scale)
	centred(img, face(fs.regular, 13, scale), muted, "by @gehariharan · gehariharan.com", (DMGHeight-34)*scale)
	return img
}

// arrow points from the app's icon to the Applications folder.
func arrow(img *image.RGBA, scale int) {
	y := float64(DMGIconY * scale)
	x0, x1 := float64((DMGAppX+78)*scale), float64((DMGLinkX-78)*scale)
	sc := rasterx.NewScannerGV(img.Bounds().Dx(), img.Bounds().Dy(), img, img.Bounds())
	stroker := rasterx.NewStroker(img.Bounds().Dx(), img.Bounds().Dy(), sc)
	stroker.SetStroke(fixed.I(3*scale), fixed.I(4*scale), rasterx.RoundCap, rasterx.RoundCap, rasterx.RoundGap, rasterx.Round)
	stroker.SetColor(green)
	head := 12 * float64(scale)
	stroker.Start(rasterx.ToFixedP(x0, y))
	stroker.Line(rasterx.ToFixedP(x1, y))
	stroker.Stop(false)
	stroker.Start(rasterx.ToFixedP(x1-head, y-head))
	stroker.Line(rasterx.ToFixedP(x1, y))
	stroker.Line(rasterx.ToFixedP(x1-head, y+head))
	stroker.Stop(false)
	stroker.Draw()
}
