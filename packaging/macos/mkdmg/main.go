// Command mkdmg builds the wasync disk image: a window with wasync.app, a
// link to Applications, and a background picture asking to drag one onto the
// other and then eject the disk. It writes the Finder layout itself, so it needs no Finder scripting
// and runs on a build machine with no one logged in.
//
//	go run ./packaging/macos/mkdmg -app dist/.../wasync.app -out wasync_V_darwin_ARCH.dmg
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leaanthony/dmg/dmg"
)

// The window's size and where its two icons sit, in points. They match the
// arrow drawn on the background (packaging/desktop/assetgen).
const (
	width, height = 660, 400
	appX, linkX   = 180, 480
	iconY         = 205
)

func main() {
	app := flag.String("app", "", "the signed wasync.app")
	out := flag.String("out", "", "the disk image to write")
	background := flag.String("background", filepath.Join("packaging", "macos", "dmg", "background.tiff"), "the window's background picture")
	icon := flag.String("icon", filepath.Join("packaging", "macos", "app", "wasync.icns"), "the mounted volume's icon")
	flag.Parse()
	if *app == "" || *out == "" || filepath.Base(*app) != "wasync.app" {
		fmt.Fprintln(os.Stderr, "usage: mkdmg -app PATH/wasync.app -out PATH.dmg")
		os.Exit(2)
	}
	if err := os.Remove(*out); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	err := dmg.Build(dmg.Options{
		VolumeName:             "wasync",
		OutputPath:             *out,
		Files:                  map[string]string{"wasync.app": *app},
		AddApplicationsSymlink: true,
		IconPositions:          map[string]dmg.IconPosition{"wasync.app": {X: appX, Y: iconY}, "Applications": {X: linkX, Y: iconY}},
		Window:                 dmg.WindowConfig{X: 200, Y: 120, Width: width, Height: height},
		Icon:                   dmg.IconConfig{Size: 128, TextSize: 13, GridSpace: 100},
		Background:             &dmg.BackgroundConfig{File: *background},
		Format:                 dmg.FormatUDZO,
		Filesystem:             dmg.FSHFSPlus,
		VolumeIcon:             *icon,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "build disk image:", err)
		os.Exit(1)
	}
}
