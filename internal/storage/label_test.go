package storage

import "testing"

func TestDisplayNameGivesUnnamedGroupsAReadableLabel(t *testing.T) {
	cases := []struct {
		group Group
		want  string
	}{
		{Group{ID: "120363266948712648@g.us", Name: ""}, "Unnamed group …712648"},
		{Group{ID: "120363266948712648@g.us", Name: "   "}, "Unnamed group …712648"},
		{Group{ID: "120363266948712648@g.us", Name: "120363266948712648@g.us"}, "Unnamed group …712648"},
		{Group{ID: "120363266948000001@g.us", Name: ""}, "Unnamed group …000001"},
		{Group{ID: "12@g.us"}, "Unnamed group …12"},
		{Group{ID: "120363266948712648@g.us", Name: "Family"}, "Family"},
		{Group{ID: "15551234567@s.whatsapp.net", Name: ""}, "15551234567@s.whatsapp.net"},
		{Group{ID: "15551234567@s.whatsapp.net", Name: "Sam"}, "Sam"},
	}
	for _, c := range cases {
		if got := c.group.DisplayName(); got != c.want {
			t.Errorf("DisplayName(%+v) = %q, want %q", c.group, got, c.want)
		}
	}
}
