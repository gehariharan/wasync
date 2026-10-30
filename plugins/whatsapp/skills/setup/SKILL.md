---
name: setup
description: Guide the user through setting up WhatsApp with wasync for the first time, from checking the installation to reading their first message.
---

# Set up WhatsApp

Run this when the person asks to set WhatsApp up or when `whatsapp_status` shows it is not ready. Use plain words; they may not use a terminal.

1. **Check.** Call `whatsapp_status`.
   - If wasync is not installed, tell them it needs installing once and stop. On a Mac: `brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git && brew install --cask gehariharan/tap/wasync`. On Windows: download the Setup program from https://github.com/gehariharan/wasync/releases and tick "Also install whatsapp-axi". Offer to wait while they do it.
2. **Open setup.** If WhatsApp is not linked or no agent is connected, call `whatsapp_start_setup`. Say: "I opened a page in your browser. Scan the QR code with WhatsApp on your phone (Settings, Linked devices, Link a device), choose the chats you want me to see, then press Connect. Your computer will ask for Touch ID, your password or Windows Hello to confirm. Tell me when you are done." Do not try to click or read that page yourself.
3. **Verify.** When they say they are done, call `whatsapp_doctor`, then `whatsapp_list_chats`. If a check fails, explain the repair hint simply and offer to open setup again.
4. **First message.** Ask which chat to look at, read it with `whatsapp_read_messages`, and summarise. If it is refused for access, use `whatsapp_request_access` as the `whatsapp` skill describes and wait for them to approve on their computer.
5. **Finish** by telling them what you can and cannot do: read the chats they chose; send only where they allow it; never approve anything for them. Mention that `wasync service install` (or the setup page's last step) starts wasync when they log in.

Never ask them to copy a credential or a link into the chat.
