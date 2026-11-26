package whatsapp

import (
	"encoding/json"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// unwrap examines wrappers without retaining raw protobufs in our archive.
func unwrap(m *waE2E.Message) *waE2E.Message {
	for range 12 {
		var next *waE2E.Message
		switch {
		case m.GetDeviceSentMessage() != nil:
			next = m.GetDeviceSentMessage().GetMessage()
		case m.GetEphemeralMessage() != nil:
			next = m.GetEphemeralMessage().GetMessage()
		case m.GetEditedMessage() != nil:
			next = m.GetEditedMessage().GetMessage()
		case m.GetDocumentWithCaptionMessage() != nil:
			next = m.GetDocumentWithCaptionMessage().GetMessage()
		default:
			return m
		}
		if next == nil {
			return m
		}
		m = next
	}
	return nil
}

// Normalize deliberately excludes quoted bodies, thumbnails, download URLs and
// media decryption keys. The original sender and group identifiers remain opaque.
func Normalize(e *events.Message, source string, now time.Time) (storage.Message, bool) {
	if e == nil || !SupportedChat(e.Info.Chat) || e.Info.ID == "" || e.Message == nil {
		return storage.Message{}, false
	}
	out := storage.Message{GroupID: e.Info.Chat.String(), MessageID: e.Info.ID, SenderID: e.Info.Sender.String(), Timestamp: e.Info.Timestamp, FromMe: e.Info.IsFromMe, Source: source, Type: "unknown"}
	if !e.Info.SenderAlt.IsEmpty() {
		out.SenderAlt = e.Info.SenderAlt.String()
	}
	m := e.Message
	raw := unwrap(e.RawMessage)
	protocol := raw.GetProtocolMessage()
	if protocol == nil {
		protocol = m.GetProtocolMessage()
	}
	if protocol != nil {
		// Explicit type presence avoids treating an unrelated empty protocol message as a revoke.
		if protocol.Type == nil {
			return out, false
		}
		if remote := protocol.GetKey().GetRemoteJID(); remote != "" && remote != out.GroupID {
			return out, false
		}
		switch protocol.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			out.Kind = "delete"
			out.MessageID = protocol.GetKey().GetID()
			out.Type = "deleted"
			out.MutationAt = e.Info.Timestamp
			if protocol.GetTimestampMS() > 0 {
				out.MutationAt = time.UnixMilli(protocol.GetTimestampMS())
			}
			return out, out.MessageID != "" && !out.MutationAt.IsZero()
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			out.Kind = "edit"
			out.MessageID = protocol.GetKey().GetID()
			out.MutationAt = e.Info.Timestamp
			if protocol.GetTimestampMS() > 0 {
				out.MutationAt = time.UnixMilli(protocol.GetTimestampMS())
			}
			edited := protocol.GetEditedMessage()
			if edited == nil || out.MessageID == "" || viewOnceMessage(edited) {
				return out, false
			}
			// New content can be wrapped like a new message (a group mention,
			// spoiler or captioned document); unwrap it the way whatsmeow
			// unwraps received messages.
			m = events.UnwrapMessage(edited)
		default:
			return out, false
		}
	} else if e.IsEdit {
		// A wrapper with no explicit target is ambiguous: do not overwrite an original.
		return out, false
	}
	if viewOnceEvent(e) || viewOnceMessage(m) {
		return out, false
	}
	var contextInfo *waE2E.ContextInfo
	type mediaInfo struct {
		MIME      string `json:"mime,omitempty"`
		Filename  string `json:"filename,omitempty"`
		Size      uint64 `json:"size,omitempty"`
		Width     uint32 `json:"width,omitempty"`
		Height    uint32 `json:"height,omitempty"`
		Duration  uint32 `json:"duration_seconds,omitempty"`
		VoiceNote bool   `json:"voice_note,omitempty"`
		Animated  bool   `json:"animated,omitempty"`
		Lottie    bool   `json:"lottie,omitempty"`
		Pages     uint32 `json:"pages,omitempty"`
		Title     string `json:"title,omitempty"`
	}
	var metadata any
	switch {
	case m.GetConversation() != "":
		out.Type = "text"
		out.Text = m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		v := m.GetExtendedTextMessage()
		out.Type = "text"
		out.Text = v.GetText()
		contextInfo = v.GetContextInfo()
	case m.GetImageMessage() != nil:
		v := m.GetImageMessage()
		out.Type = "image"
		out.Text = v.GetCaption()
		contextInfo = v.GetContextInfo()
		metadata = &mediaInfo{MIME: v.GetMimetype(), Size: v.GetFileLength(), Width: v.GetWidth(), Height: v.GetHeight()}
	case m.GetVideoMessage() != nil:
		v := m.GetVideoMessage()
		out.Type = "video"
		out.Text = v.GetCaption()
		contextInfo = v.GetContextInfo()
		metadata = &mediaInfo{MIME: v.GetMimetype(), Size: v.GetFileLength(), Width: v.GetWidth(), Height: v.GetHeight(), Duration: v.GetSeconds(), Animated: v.GetGifPlayback()}
	case m.GetDocumentMessage() != nil:
		v := m.GetDocumentMessage()
		out.Type = "document"
		out.Text = v.GetCaption()
		contextInfo = v.GetContextInfo()
		metadata = &mediaInfo{MIME: v.GetMimetype(), Filename: v.GetFileName(), Size: v.GetFileLength(), Pages: v.GetPageCount(), Title: v.GetTitle()}
	case m.GetAudioMessage() != nil:
		v := m.GetAudioMessage()
		out.Type = "audio"
		contextInfo = v.GetContextInfo()
		metadata = &mediaInfo{MIME: v.GetMimetype(), Size: v.GetFileLength(), Duration: v.GetSeconds(), VoiceNote: v.GetPTT()}
	case m.GetStickerMessage() != nil:
		v := m.GetStickerMessage()
		out.Type = "sticker"
		contextInfo = v.GetContextInfo()
		metadata = &mediaInfo{MIME: v.GetMimetype(), Size: v.GetFileLength(), Width: v.GetWidth(), Height: v.GetHeight(), Animated: v.GetIsAnimated(), Lottie: v.GetIsLottie()}
	case m.GetReactionMessage() != nil:
		v := m.GetReactionMessage()
		target := v.GetKey()
		if target.GetID() == "" || !reactionTargetMatches(e, target.GetRemoteJID()) {
			return out, false
		}
		out.Type = "reaction"
		out.Text = v.GetText()
		out.ReplyTo = target.GetID()
		// Preserve a reaction event, including removal, rather than claiming it is
		// the latest aggregate state when live/history deliveries can be reordered.
		metadata = struct {
			Target      string `json:"target_message_id"`
			Participant string `json:"target_participant,omitempty"`
			FromMe      bool   `json:"target_from_me"`
			Emoji       string `json:"emoji"`
			Removed     bool   `json:"removed"`
			Timestamp   int64  `json:"sender_timestamp_ms,omitempty"`
		}{target.GetID(), target.GetParticipant(), target.GetFromMe(), v.GetText(), v.GetText() == "", v.GetSenderTimestampMS()}
	}
	if out.Kind == "edit" && (out.Type == "sticker" || out.Type == "reaction") {
		return out, false
	}
	if contextInfo != nil {
		out.ReplyTo = contextInfo.GetStanzaID()
	}
	if metadata != nil {
		data, _ := json.Marshal(metadata)
		out.Media = string(data)
	}
	// Ephemeral content expires locally. If the deadline is unavailable, skip it.
	duration := contextInfo.GetExpiration()
	if duration == 0 {
		duration = e.SourceWebMsg.GetEphemeralDuration()
	}
	if e.IsEphemeral || duration > 0 || e.SourceWebMsg.GetEphemeralExpirationTimestamp() > 0 {
		if out.Kind == "edit" && e.SourceWebMsg.GetEphemeralExpirationTimestamp() == 0 {
			return out, false
		}
		var expiry time.Time
		if absolute := e.SourceWebMsg.GetEphemeralExpirationTimestamp(); absolute > 0 {
			expiry = time.Unix(int64(absolute), 0)
		} else if duration > 0 && !e.Info.Timestamp.IsZero() {
			expiry = e.Info.Timestamp.Add(time.Duration(duration) * time.Second)
		} else {
			return out, false
		}
		if !expiry.After(now) {
			return out, false
		}
		out.ExpiresAt = &expiry
	}
	return out, !out.Timestamp.IsZero()
}

// Reject view-once wrappers and inline media flags even when an event was not
// unwrapped by the library (for example when processing historical deliveries).
func viewOnceMessage(message *waE2E.Message) bool {
	m := unwrap(message)
	return m.GetViewOnceMessage() != nil || m.GetViewOnceMessageV2() != nil || m.GetViewOnceMessageV2Extension() != nil || m.GetImageMessage().GetViewOnce() || m.GetVideoMessage().GetViewOnce() || m.GetAudioMessage().GetViewOnce()
}
func viewOnceEvent(e *events.Message) bool {
	return e.IsViewOnce || e.IsViewOnceV2 || e.IsViewOnceV2Extension || viewOnceMessage(e.Message) || viewOnceMessage(e.RawMessage)
}
func reactionTargetMatches(e *events.Message, remote string) bool {
	if remote == "" || remote == e.Info.Chat.String() {
		return true
	}
	if e.Info.IsGroup || e.Info.Chat.Server == types.GroupServer {
		return false
	}
	alternate := e.Info.SenderAlt
	if e.Info.IsFromMe {
		alternate = e.Info.RecipientAlt
	}
	return !alternate.IsEmpty() && SupportedChat(alternate.ToNonAD()) && alternate.Server != types.GroupServer && remote == alternate.ToNonAD().String()
}
