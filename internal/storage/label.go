package storage

import "strings"

// DisplayName is the name to show people for a chat. WhatsApp does not always
// supply a group subject (for example the parent entry of a community, or a
// group seen before its details synced), and an empty row is unreadable, so a
// group without a usable name is shown as "Unnamed group" plus the tail of its
// id, which is enough to tell two such groups apart.
func (g Group) DisplayName() string {
	name := strings.TrimSpace(g.Name)
	if ChatKind(g.ID) != "group" {
		if name == "" {
			return g.ID
		}
		return name
	}
	if name != "" && name != g.ID {
		return name
	}
	user, _, _ := strings.Cut(g.ID, "@")
	if len(user) > 6 {
		user = user[len(user)-6:]
	}
	if user == "" {
		return "Unnamed group"
	}
	return "Unnamed group …" + user
}
