//go:build darwin || windows

// Command wasync-desktop is the wasync app: wasync's own local pages in a
// window of the system web view (WKWebView on macOS, WebView2 on Windows), and
// a menu-bar or tray icon to open them, see how wasync is doing, and quit.
// Closing the window leaves wasync running behind the icon. Only one copy runs
// for each person; opening the app again shows its window.
//
// The app starts `wasync setup --app` beside it (see internal/desktop), or
// shows the pages of a wasync already running for the data directory, such
// as one started in a terminal.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/buildinfo"
	"github.com/gehariharan/wasync/internal/cli"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/desktop"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed icons/tray-template.png
var trayTemplateIcon []byte

//go:embed icons/tray.png
var trayIcon []byte

//go:embed icons/app.png
var appIcon []byte

// singleInstanceID names the app to the operating system so a second copy
// hands over to the first. It is per person, like the app itself.
const singleInstanceID = "com.gehariharan.wasync.app"

// stopWait is how long quitting waits for wasync to finish cleanly.
const stopWait = 20 * time.Second

// starting is what the window shows until wasync gives a page.
const starting = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>wasync</title></head><body><p>Starting wasync…</p></body></html>`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wasync:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Default()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("wasync-desktop", flag.ContinueOnError)
	background := flags.Bool(service.BackgroundFlag[2:], false, "start in the menu bar or notification area only, as at login")
	flags.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "dedicated private data directory")
	flags.StringVar(&cfg.APIAddr, "api-addr", cfg.APIAddr, "local API loopback address for the wasync the app starts")
	wasync := flags.String("wasync", "", "the wasync command to start (default: the one beside this app)")
	version := flags.Bool("version", false, "show installed build information")
	if err = flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *version {
		fmt.Println(buildinfo.String())
		return nil
	}
	if cfg, err = cfg.Validate(); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if *wasync == "" {
		*wasync = filepath.Join(filepath.Dir(self), wasyncName())
	}
	if err = localfs.EnsureDir(cfg.DataDir); err != nil {
		return err
	}
	logFile, err := localfs.OpenFile(filepath.Join(cfg.DataDir, "desktop.log"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	if _, err = logFile.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(logFile, nil))
	log.Info("wasync app starting", "version", buildinfo.Version, "data_dir", cfg.DataDir, "background", *background)

	host := &desktop.Host{Wasync: *wasync, App: self, Config: cfg, Log: logFile, CLI: &cli.App{Out: io.Discard, Err: logFile}, Command: wasyncCommand}
	d := &desktopApp{host: host, log: log}
	return d.run(*background)
}

type desktopApp struct {
	host *desktop.Host
	log  *slog.Logger
	app  *application.App

	mu     sync.Mutex
	window *application.WebviewWindow
	status *application.MenuItem
}

func (d *desktopApp) run(background bool) error {
	policy := application.ActivationPolicyRegular
	if background {
		policy = application.ActivationPolicyAccessory
	}
	d.app = application.New(application.Options{
		Name:        "wasync",
		Description: "wasync keeps the WhatsApp chats you choose on this computer for you and your AI agents.",
		Icon:        appIcon,
		Logger:      d.log,
		Mac: application.MacOptions{
			ActivationPolicy: policy,
			// Closing the window keeps wasync running behind the menu-bar icon.
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: singleInstanceID,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				// Opening the app again shows it; a second start at login does not.
				for _, arg := range data.Args[1:] {
					if arg == service.BackgroundFlag {
						return
					}
				}
				d.openWindow()
			},
		},
		OnShutdown: func() {
			if err := d.host.Stop(stopWait); err != nil {
				d.log.Warn("stopping wasync", "error", err)
			}
			d.log.Info("wasync app quit")
		},
	})
	d.trayIcon()
	d.app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { d.openWindow() })
	d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go d.keepStatus()
		if background {
			go func() {
				if _, err := d.host.Ensure(context.Background()); err != nil {
					d.log.Warn("starting wasync", "error", err)
				}
			}()
			return
		}
		d.openWindow()
	})
	return d.app.Run()
}

// trayIcon adds the menu-bar (macOS) or notification-area (Windows) icon.
func (d *desktopApp) trayIcon() {
	tray := d.app.SystemTray.New()
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayTemplateIcon)
	} else {
		tray.SetIcon(trayIcon)
	}
	tray.SetTooltip("wasync")
	menu := d.app.NewMenu()
	menu.Add("Open wasync").OnClick(func(*application.Context) { d.openWindow() })
	d.status = menu.Add("Starting…").SetEnabled(false)
	menu.AddSeparator()
	menu.Add("Quit wasync").OnClick(func(*application.Context) { d.app.Quit() })
	tray.SetMenu(menu)
	if runtime.GOOS == "windows" {
		// A click opens the window; the menu is on the right button.
		tray.OnClick(d.openWindow)
	}
}

// keepStatus refreshes the status line in the icon's menu.
func (d *desktopApp) keepStatus() {
	last := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		line := d.host.Status(ctx)
		cancel()
		if line != last {
			last = line
			application.InvokeAsync(func() { d.status.SetLabel(line) })
		}
		time.Sleep(5 * time.Second)
	}
}

// openWindow shows the window, opening it on wasync's start page when it is
// not open yet. Each new window signs in with a fresh one-time link.
func (d *desktopApp) openWindow() {
	d.mu.Lock()
	if w := d.window; w != nil {
		d.mu.Unlock()
		showInDock(true)
		w.Show()
		w.Restore()
		w.Focus()
		return
	}
	w := d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "wasync",
		Title:            "wasync",
		Width:            1100,
		Height:           760,
		MinWidth:         720,
		MinHeight:        520,
		HTML:             starting,
		BackgroundColour: application.NewRGB(255, 255, 255),
	})
	d.window = w
	d.mu.Unlock()
	// Closing the window frees its web view; wasync keeps running behind the icon.
	w.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		d.mu.Lock()
		if d.window == w {
			d.window = nil
		}
		d.mu.Unlock()
		showInDock(false)
	})
	showInDock(true)
	w.Show()
	w.Focus()
	go d.load(w)
}

// load starts wasync if needed and opens its start page in w.
func (d *desktopApp) load(w *application.WebviewWindow) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	link, err := d.host.Link(ctx, d.host.StartPage(ctx))
	if err != nil {
		d.log.Warn("opening wasync", "error", err)
		if !errors.Is(err, desktop.ErrStopped) {
			w.SetHTML(problemPage(err))
		}
		return
	}
	w.SetURL(link)
}

func problemPage(err error) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>wasync</title></head><body><h1>wasync could not start</h1><p>` +
		html.EscapeString(err.Error()) + `</p><p>Close this window and open wasync again from its icon to try again.</p></body></html>`
}
