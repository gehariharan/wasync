# Media and attachments

wasync archives attachment *details* (type, size, filename and so on) for every synced chat, and
by default also downloads the files: the **Also save photos, videos and files** box on Choose chats
starts ticked. Untick it to keep text only. You can still choose per chat, and download a single
attachment on request.

## Choosing what downloads

Download selection has two layers:

- Enable capture for the chats you want to archive.
- Pick how files download in each chat: a saved per-chat policy, a one-time request for a
  single message, or the wasync default for chats whose policy is `inherit`. The default is the
  **Also save photos, videos and files** setting (on unless you untick it), and the
  `--download-media` flag turns it on whatever the setting says. With the default off and no
  per-chat override, attachments remain available for manual download.

In the web app (`wasync web`), an attachment that is not downloaded yet has a **Download** button
in a syncing chat, and the chat header's **Downloads** setting sets that chat's policy ("Only
when I ask", "Always in this chat", "Same as wasync default"). Downloaded photos, videos and
voice notes show in the chat; documents open on request. In the [terminal UI](tui.md), **m** on a
chat cycles `manual` → `auto` → `inherit`, and **m** on a message requests its attachment once.

### Per-chat policies

**Chat policies are saved configuration.** They survive restarts and are isolated
by WhatsApp account. Capture selection remains independent and must be enabled.

| Policy | Behavior |
| --- | --- |
| `manual` | No automatic file downloads, even with the wasync default on. Request individual attachments. |
| `auto` | Automatically download newly delivered eligible attachments in this chat, even with the wasync default off. |
| `inherit` (default) | Follow the wasync default: the **Also save photos, videos and files** setting, or `--download-media` / `WASYNC_DOWNLOAD_MEDIA`. With both off, download manually. |

```sh
wasync media policy --chat CHAT_ID --mode manual
wasync media policy --chat CHAT_ID --mode auto
wasync media policy --chat CHAT_ID --mode inherit
wasync media policy --chat CHAT_ID
```

Policy changes work with or without a running wasync. They do not change capture
selection, remove downloaded files, or bulk-download old attachments. Disabling chat capture
stops its downloads too. Media policies survive a local chat purge, so re-enabling capture
restores the previous media behavior.

### The wasync default

The **Also save photos, videos and files** box on the Choose chats page (`/ui/chats/pick`) is the
default for every chat whose policy is `inherit`. It is saved in `settings.json` in the data
directory, starts ticked, and a change applies to the next attachment that arrives, with no
restart. It never downloads attachments that arrived before it was ticked.

```sh
wasync --download-media serve
# Equivalent environment setting:
WASYNC_DOWNLOAD_MEDIA=1 wasync serve
```

The flag is a setting for that process launch, not a preference written into every chat. An
environment variable remains effective for as long as you keep it configured. It turns the
default on for chats whose policy is `inherit`, even when the box is unticked.

### One-time downloads

**A per-message request is one-time**, and changes no policy:

```sh
# Keep wasync running (serve, the setup page or the terminal UI):
wasync serve
# In another terminal:
wasync media download --chat CHAT_ID --message-id WHATSAPP_MESSAGE_ID
wasync query messages --chat CHAT_ID
```

The command returns `queued` or `already_downloaded`; queued is not a success
receipt for the transfer. Inspect `media_metadata.download_state` for `available`,
`queued`, `downloading`, `downloaded`, `failed`, `queue_full` or `interrupted`.
Repeated active requests are deduplicated. Re-request an interrupted/failed file
to retry. Commands reuse the running wasync's signed local channel and `send.token`;
read-only query commands never trigger downloads. Media-control endpoints require
`send.token` and reject `api.token`. With `--no-api`, use the terminal UI instead. Offline mode
can edit policies and browse the archive, but cannot download from WhatsApp.

## What downloads and where

Images, documents, videos, audio/voice notes and ordinary WebP stickers are
eligible in enabled chats. Lottie stickers retain metadata only. View-once and disappearing
attachments are excluded. Downloads run in a bounded background queue and
use whatsmeow's authenticated decryption and integrity checking. Files are streamed to disk
without a wasync-specific size cap and stored under the private `media/ACCOUNT/CHAT_HASH/`
directory. Sender filenames are display metadata only and are never used as paths: downloads
use hashed names and a fixed safe MIME-to-extension mapping. Unknown types use `.bin`.

Attachment metadata (`media_metadata` in query and API results) shows MIME type, original
filename/title when supplied, size, dimensions, duration, document page count,
voice-note/animation flags, `download_state`, `local_path` and `relative_path`.
`relative_path` is authoritative relative to the current data directory's `media/`
root, so moving your archive preserves file identity; `local_path` records the
absolute path at download time. States such as
`failed`, `queue_full` and `invalid_metadata` describe incomplete attempts; downloads
are best effort and require available WhatsApp media. `queue_full`/`failed` states have no
automatic retry; request the file again, or another delivery of valid WhatsApp media metadata
is needed. Missing files are not retrieved merely by searching the local archive.

Documents such as PDF, Word, Excel and PowerPoint are retained as files, not parsed
or indexed; text search covers message text and attachment captions, not
PDF/OCR/document contents or metadata-only filenames/titles. Local media files are plaintext
and are never opened or executed automatically.

Sending an attachment to an enabled chat with downloads enabled may also download your own
upload. Purging a chat removes its local download directory. Observed deletions remove matching
downloaded files.

Download cleanup failures (for example, a file open in another Windows program)
do not stop message capture. A background sweep retries removal of orphaned
managed downloads at startup and every thirty seconds while the media folders hold files;
once a sweep finds them empty it rests until wasync writes a file there again. Unrecognized
files placed in the media directory are not swept. Close other programs holding downloaded
files if cleanup remains pending.

## Download keys (`media-refs/`)

For newly captured eligible media, the private `media-refs/ACCOUNT/CHAT_HASH/`
store retains only the download path/key, hashes, type/size and opaque identities, so one-time
downloads work after a restart. It contains credentials, not searchable message text.
Archive/API queries never return these secrets. These files are plaintext credentials
protected by filesystem permissions/ACLs, like the device session. Include this directory only
in private backups; it is not a file to share with agents or other archive consumers. Same-user
filesystem access is not isolated by directory permissions, and these credentials are not
encrypted at rest. References can outlive remote WhatsApp media availability.

Clear/purge/revoke removes corresponding references; background cleanup retries
orphaned references after interruptions. Disabled capture preserves existing
references and downloaded data, but downloading requires re-enabling capture.
View-once, disappearing and edited attachment references are excluded; missing
references require redelivery of valid original attachment metadata. Old metadata-only rows
from earlier versions, and keys discarded by older versions, cannot be recovered by updating or
by starting with downloads enabled: WhatsApp must deliver the original attachment metadata
again. Logout preserves references with the archived data; clear/purge the corresponding local
data to remove them. Remote media availability may expire independently of either local policy
or pairing status.

## Media size

wasync has no fixed 25 MiB sending or download limit. Uploads use whatsmeow's
streaming encryption/upload API; downloads write to disk and verify the message's
advertised length plus upstream cryptographic integrity. WhatsApp's current
service/type limits and media availability still apply; removing an application
cap does not make WhatsApp accept an unsupported file. Cancellation and normal
network/protocol failures remain possible. Large transfers need disk space for
temporary encrypted data in addition to their final file.

Images and videos sent inline are the exceptions: an image sent with `--type image` (or by an
agent) must be a JPEG or PNG of at most 16 MiB, and a video sent with `--type video` (or by an
agent) must be an MP4 with H.264 video and AAC audio of at most 64 MiB, the most WhatsApp's linked
devices send as a playable video. Send a larger or different file as a document; see
[Sending](cli.md#sending-replying-reacting-and-editing).

For a long explicit upload, increase the CLI's command timeout, for example:

```sh
wasync --timeout 30m send --to CHAT_ID --file ./large-report.pdf
```

The running wasync's authenticated send route honors client cancellation rather
than imposing the ordinary short query timeout. Only an explicit send invocation
uploads a file; enabling downloads never sends a conversation message.
