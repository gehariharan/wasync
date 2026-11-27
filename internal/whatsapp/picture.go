package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// ProfilePicture is a chat's small profile picture as WhatsApp reports it.
type ProfilePicture struct {
	// ID is WhatsApp's identifier for the current picture; "" when the chat
	// has none or hides it from this account.
	ID string
	// Data is the JPEG preview; nil when Unchanged or when there is none.
	Data []byte
	// Unchanged means the picture is still the one known by the ID asked with.
	Unchanged bool
}

// maxPictureBytes bounds a downloaded preview. WhatsApp's previews are a few
// kilobytes; anything far larger is not one.
const maxPictureBytes = 256 << 10

// pictureInfo is the library call that asks WhatsApp about a picture; a test seam.
type pictureInfo func(context.Context, types.JID, *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error)

// pictureGet downloads a picture from WhatsApp's picture server; a test seam.
type pictureGet func(context.Context, string) ([]byte, error)

// ProfilePicture asks WhatsApp for a chat's small preview picture. knownID is
// the picture already cached, if any: WhatsApp then answers "unchanged"
// without sending it again. Each call is one WhatsApp request, so callers pace
// them. A chat with no picture, or one hidden from this account, is not an
// error: it returns an empty ProfilePicture.
func (c *Client) ProfilePicture(ctx context.Context, chat, knownID string) (ProfilePicture, error) {
	jid, err := ParseChat(chat)
	if err != nil {
		return ProfilePicture{}, err
	}
	ask := c.pictureInfo
	if ask == nil {
		wa := c.wa()
		if wa == nil || !wa.IsConnected() {
			return ProfilePicture{}, fmt.Errorf("connect WhatsApp before fetching pictures")
		}
		ask = wa.GetProfilePictureInfo
	}
	info, err := ask(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: true, ExistingID: knownID})
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet), errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		return ProfilePicture{}, nil
	case err != nil:
		return ProfilePicture{}, err
	case info == nil && knownID != "":
		return ProfilePicture{ID: knownID, Unchanged: true}, nil
	case info == nil || info.URL == "":
		return ProfilePicture{}, nil
	}
	get := c.pictureGet
	if get == nil {
		get = downloadPicture
	}
	data, err := get(ctx, info.URL)
	if err != nil {
		return ProfilePicture{}, err
	}
	if !isJPEG(data) {
		return ProfilePicture{}, fmt.Errorf("profile picture is not a JPEG image")
	}
	return ProfilePicture{ID: info.ID, Data: data}, nil
}

func isJPEG(data []byte) bool { return bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}) }

// pictureClient downloads previews; it follows no redirects off WhatsApp's servers.
var pictureClient = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
	if len(via) >= 3 || !pictureHost(r.URL) {
		return fmt.Errorf("unexpected picture redirect")
	}
	return nil
}}

// pictureHost accepts only HTTPS addresses on WhatsApp's own domain, so a
// picture address can never point wasync at another server.
func pictureHost(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && u.User == nil && (host == "whatsapp.net" || strings.HasSuffix(host, ".whatsapp.net"))
}

func downloadPicture(ctx context.Context, address string) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil || !pictureHost(u) {
		return nil, fmt.Errorf("unexpected profile picture address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := pictureClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download profile picture: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download profile picture: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPictureBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download profile picture: %w", err)
	}
	if len(data) > maxPictureBytes {
		return nil, fmt.Errorf("profile picture is larger than a preview")
	}
	return data, nil
}

// PhoneNumber returns the phone number of a direct chat as "+digits" when
// WhatsApp has shared it: the chat's own number, or the number behind a hidden
// (@lid) address. It returns "" for groups and for hidden addresses whose number
// this account was never shown.
func (c *Client) PhoneNumber(ctx context.Context, chat string) string {
	jid, err := ParseChat(chat)
	if err != nil || jid.Server == types.GroupServer {
		return ""
	}
	if jid.Server == types.HiddenUserServer {
		src := c.nameSource()
		if src == nil {
			return ""
		}
		pn, err := src.GetPNForLID(ctx, jid)
		if err != nil || pn.Server != types.DefaultUserServer || pn.User == "" {
			return ""
		}
		jid = pn
	}
	if jid.User == "" || strings.Trim(jid.User, "0123456789") != "" {
		return ""
	}
	return "+" + jid.User
}
