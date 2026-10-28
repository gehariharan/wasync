package contract

import (
	"encoding/json"
	"testing"
)

func TestEnvelopeShapes(t *testing.T) {
	var ok Envelope
	if err := json.Unmarshal([]byte(`{"request_id":"r1","data":{"api_version":1,"api_minor":0,"wasync_version":"0.1.0","schema_version":6,"features":["chats"]},"page":{"next_cursor":"c","has_more":true}}`), &ok); err != nil {
		t.Fatal(err)
	}
	var v VersionInfo
	if ok.IsError() || json.Unmarshal(ok.Data, &v) != nil || v.API() != "1.0" || !v.HasFeature(FeatureChats) || v.HasFeature(FeatureChanges) || v.ArchiveSchemaVersion != nil || !ok.Page.HasMore {
		t.Fatalf("%+v %+v", ok, v)
	}
	var bad Envelope
	if err := json.Unmarshal([]byte(`{"request_id":"r2","error_code":"owner_offline","error":"no owner","help":"start wasync","state":"stopped"}`), &bad); err != nil {
		t.Fatal(err)
	}
	e := bad.Err()
	if !bad.IsError() || e.Code != CodeOwnerOffline || e.StateString() != "stopped" || e.Error() != "owner_offline: no owner" {
		t.Fatalf("%+v", e)
	}
	if len(WasyncCodes) != 17 {
		t.Fatalf("expected the 17 contract error codes, got %d", len(WasyncCodes))
	}
}

func TestIsMessageRef(t *testing.T) {
	for s, want := range map[string]bool{"m1_abc": true, "m1_": false, "m2_abc": false, "": false, "abc": false} {
		if IsMessageRef(s) != want {
			t.Errorf("%q", s)
		}
	}
}
