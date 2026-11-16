#ifndef WASYNC_KEYCHAIN_DARWIN_H
#define WASYNC_KEYCHAIN_DARWIN_H

// Each function returns the system's own result code (0 on success, otherwise
// the Keychain OSStatus). wasync_kc_get returns 1, which no OSStatus uses, when
// the stored value does not fit; it fills out (NUL terminated) with at most
// outlen-1 bytes.
int wasync_kc_get(const char *service, const char *account, char *out, int outlen);
int wasync_kc_add(const char *service, const char *account, const char *secret, const char *folder);
int wasync_kc_update(const char *service, const char *account, const char *secret, const char *folder);
int wasync_kc_delete(const char *service, const char *account);

// wasync_kc_list reads the attributes, never a secret, so it raises no prompt.
// On success *out points to malloc'ed bytes (the caller frees them) holding, for
// each item, its account and its folder, each followed by a NUL; the folder is
// empty for an item that has none.
int wasync_kc_list(const char *service, char **out, int *outlen);

#endif
