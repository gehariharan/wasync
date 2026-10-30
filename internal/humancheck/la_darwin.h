#ifndef WASYNC_LA_DARWIN_H
#define WASYNC_LA_DARWIN_H

// wasync_la_available reports whether Touch ID or the account password can be asked for.
int wasync_la_available(void);

// wasync_la_verify shows the system prompt with reason and waits up to
// timeout_ms. It returns 1 when the person confirmed, 0 when they cancelled or
// failed, and -1 when no prompt could be shown.
int wasync_la_verify(const char *reason, int timeout_ms);

#endif
