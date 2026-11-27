package api

import (
	"errors"
	"html"
	"net/url"
	"strings"
	"testing"
)

func TestSettingsShowWhereTheDataIsAndOpenTheFolder(t *testing.T) {
	var opened []string
	fail := false
	f := setupAccessOwner(t, nil, func(o *Owner) {
		o.ShowFolder = func(dir string) error {
			opened = append(opened, dir)
			if fail {
				return errors.New("no file manager")
			}
			return nil
		}
	})
	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	code, page, _ := c.get("/ui/settings")
	if code != 200 {
		t.Fatalf("setup page: %d", code)
	}
	if !strings.Contains(page, "Your data is stored in <code>"+html.EscapeString(f.dir)+"</code>") || !strings.Contains(page, "<button>Show folder</button>") {
		t.Fatalf("the page must say where the data is, with Show folder:\n%s", page)
	}
	csrf := csrfOf(t, page)
	if code, _, hd := c.post("/ui/setup/folder", url.Values{"csrf": {csrf}}); code != 303 || notice(hd) != "" || len(opened) != 1 || opened[0] != f.dir {
		t.Fatalf("Show folder: %d %q opened %v", code, hd.Get("Location"), opened)
	}
	fail = true
	if _, _, hd := c.post("/ui/setup/folder", url.Values{"csrf": {csrf}}); notice(hd) != "nofolder" {
		t.Fatalf("a folder that cannot be opened is reported: %q", hd.Get("Location"))
	}
	if code, _, _ := c.post("/ui/setup/folder", url.Values{"csrf": {"forged"}}); code != 403 || len(opened) != 2 {
		t.Fatalf("a forged form opened the folder: %d %v", code, opened)
	}

	// Another device sees where the data is, but opening a window there
	// would show it to nobody, so it cannot.
	allowTailnet(t, f)
	remote := newTailnetClient(t, f, tailnetLogin)
	code, page, _ = remote.get("/ui/settings")
	if code != 200 || !strings.Contains(page, "Your data is stored in") || strings.Contains(page, "Show folder") {
		t.Fatalf("remote setup page: %d\n%s", code, page)
	}
	if _, _, hd := remote.post("/ui/setup/folder", url.Values{"csrf": {csrfOf(t, page)}}); notice(hd) != "folderhere" || len(opened) != 2 {
		t.Fatalf("remote Show folder: %q opened %v", hd.Get("Location"), opened)
	}
}
