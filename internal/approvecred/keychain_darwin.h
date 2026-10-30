#ifndef WASYNC_KEYCHAIN_DARWIN_H
#define WASYNC_KEYCHAIN_DARWIN_H

// Each function returns 0 on success, 1 when the item is not found, and a
// negative number for any other failure. wasync_kc_get fills out (NUL
// terminated) with at most outlen-1 bytes.
int wasync_kc_get(const char *service, const char *account, char *out, int outlen);
int wasync_kc_set(const char *service, const char *account, const char *secret);
int wasync_kc_delete(const char *service, const char *account);

#endif
