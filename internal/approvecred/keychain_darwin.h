#ifndef WASYNC_KEYCHAIN_DARWIN_H
#define WASYNC_KEYCHAIN_DARWIN_H

// Each function returns the system's own result code (0 on success, otherwise
// the Keychain OSStatus). wasync_kc_get returns 1, which no OSStatus uses, when
// the stored value does not fit; it fills out (NUL terminated) with at most
// outlen-1 bytes.
//
// path is a keychain file to use instead of the person's own Keychain, or NULL
// for theirs. None of these functions ever shows a window: where macOS would
// have to ask the person (the item belongs to another program, or the Keychain
// is locked), the call fails instead.
int wasync_kc_get(const char *path, const char *service, const char *account, char *out, int outlen);
int wasync_kc_add(const char *path, const char *service, const char *account, const char *secret, const char *folder);
int wasync_kc_update(const char *path, const char *service, const char *account, const char *secret, const char *folder);
int wasync_kc_delete(const char *path, const char *service, const char *account);

// wasync_kc_list reads the attributes, never a secret, so it raises no prompt.
// On success *out points to malloc'ed bytes (the caller frees them) holding, for
// each item, its account and its folder, each followed by a NUL; the folder is
// empty for an item that has none.
int wasync_kc_list(const char *path, const char *service, char **out, int *outlen);

// wasync_kc_create makes a new, unlocked keychain file with the given password
// that stays unlocked; it is not added to the person's keychain list. Tests use it.
int wasync_kc_create(const char *path, const char *password);

// wasync_kc_signer fills out (NUL terminated) with "TEAM.identifier" for a
// program signed by a developer team, and with "" for one that is not (an
// ad-hoc or unsigned build).
int wasync_kc_signer(char *out, int outlen);

#endif
