package desktop

import (
	"net"
	"net/url"
	"strings"
)

// The app's window shows only wasync's own pages. A link to anywhere else
// opens in the default browser instead, where the person has their tabs,
// passwords and address bar: LinkScript runs in each page of the window and
// sends such a link to the app as a message, and ExternalLink checks it
// before the app opens it.

// openLinkPrefix starts a page's request to open a link in the browser.
const openLinkPrefix = "wasync:open-link:"

// maxLink is the longest link the app opens; longer messages are ignored.
const maxLink = 8 << 10

// LinkScript is the script the window runs in each page it loads. A click on a
// link to another website or an email address, or a script opening one with
// window.open, asks the app to open it in the browser and leaves the window
// where it is. A link to one of wasync's own pages that would open a new tab
// opens in the window instead, since the browser is not signed in to them.
// A click a page already handled (preventDefault) is left alone.
const LinkScript = `(function () {
  if (window.__wasyncLinks) return;
  window.__wasyncLinks = true;
  var prefix = "` + openLinkPrefix + `";
  function send(href) {
    var mac = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.external;
    if (mac) { mac.postMessage(prefix + href); return true; }
    if (window.chrome && window.chrome.webview) { window.chrome.webview.postMessage(prefix + href); return true; }
    return false;
  }
  function parse(href) {
    try { return new URL(String(href), document.baseURI); } catch (_) { return null; }
  }
  function web(u) { return u.protocol === "http:" || u.protocol === "https:"; }
  function elsewhere(u) { return u.protocol === "mailto:" || (web(u) && u.origin !== location.origin); }
  window.addEventListener("click", function (e) {
    if (e.defaultPrevented || e.button !== 0) return;
    var a = e.target instanceof Element ? e.target.closest("a[href]") : null;
    if (!a || a.hasAttribute("download")) return;
    var u = parse(a.getAttribute("href"));
    if (!u) return;
    if (elsewhere(u)) {
      if (send(u.href)) e.preventDefault();
      return;
    }
    var target = (a.getAttribute("target") || "").toLowerCase();
    if (web(u) && target !== "" && target !== "_self") {
      e.preventDefault();
      location.assign(u.href);
    }
  });
  window.open = function (href) {
    var u = href ? parse(href) : null;
    if (!u) return null;
    if (elsewhere(u)) send(u.href);
    else if (web(u)) location.assign(u.href);
    return null;
  };
})();`

// ExternalLink reads a message from a page in the window. It returns the link
// to open in the browser when the message is a request to open one, it came
// from one of wasync's pages on this computer, and the link is a web address
// or an email address. Anything else is ignored.
func ExternalLink(message, from string) (string, bool) {
	link, ok := strings.CutPrefix(message, openLinkPrefix)
	if !ok || len(link) > maxLink || !localPage(from) {
		return "", false
	}
	u, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	switch u.Scheme {
	case "http", "https":
		if u.Host == "" || u.User != nil {
			return "", false
		}
	case "mailto":
		if u.Opaque == "" {
			return "", false
		}
	default:
		return "", false
	}
	return u.String(), true
}

// localPage reports whether a page address is one of wasync's local pages: a
// plain-HTTP page on this computer's loopback address.
func localPage(page string) bool {
	u, err := url.Parse(page)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
