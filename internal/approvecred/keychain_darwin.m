//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <Security/Security.h>
#include <limits.h>
#include <stdlib.h>
#include <string.h>
#include "keychain_darwin.h"

// SecKeychainOpen, SecKeychainCreate, SecKeychainUnlock and the user
// interaction switch are the file-based Keychain's own calls. Apple marks them
// deprecated but has no replacement for the login keychain, which is where the
// approval credential lives; they are still what SecItem uses underneath.
#pragma clang diagnostic ignored "-Wdeprecated-declarations"

// wasync_open returns the keychain at path, or NULL for the person's own.
// *status is set when path cannot be opened.
static SecKeychainRef wasync_open(const char *path, OSStatus *status) {
	*status = errSecSuccess;
	if (path == NULL) {
		return NULL;
	}
	SecKeychainRef kc = NULL;
	*status = SecKeychainOpen(path, &kc);
	return kc;
}

static NSMutableDictionary *wasync_query(SecKeychainRef kc, const char *service, const char *account) {
	NSMutableDictionary *q = [@{
		(__bridge id)kSecClass: (__bridge id)kSecClassGenericPassword,
		(__bridge id)kSecAttrService: [NSString stringWithUTF8String:service],
	} mutableCopy];
	if (account != NULL) {
		q[(__bridge id)kSecAttrAccount] = [NSString stringWithUTF8String:account];
	}
	if (kc != NULL) {
		q[(__bridge id)kSecMatchSearchList] = @[(__bridge id)kc];
	}
	return q;
}

// wasync_locked reports whether the keychain (kc, or the person's default one)
// is locked. With the windows off a locked keychain answers errSecAuthFailed,
// just like an item another program made, so the lock is checked first.
static Boolean wasync_locked(SecKeychainRef kc) {
	SecKeychainRef def = NULL;
	if (kc == NULL) {
		if (SecKeychainCopyDefault(&def) != errSecSuccess) {
			return false;
		}
		kc = def;
	}
	SecKeychainStatus state = 0;
	OSStatus status = SecKeychainGetStatus(kc, &state);
	if (def != NULL) {
		CFRelease(def);
	}
	return status == errSecSuccess && !(state & kSecUnlockStateStatus);
}

// wasync_quiet runs call with the Keychain's windows turned off, so a call
// that would have to ask the person fails (errSecAuthFailed when the item
// belongs to another program, errSecInteractionNotAllowed when the keychain is
// locked) instead of showing a password dialog. The switch is per process; the
// Go side runs one Keychain call at a time.
static OSStatus wasync_quiet(OSStatus (^call)(void)) {
	Boolean was = true;
	SecKeychainGetUserInteractionAllowed(&was);
	SecKeychainSetUserInteractionAllowed(false);
	OSStatus status = call();
	SecKeychainSetUserInteractionAllowed(was);
	return status;
}

int wasync_kc_get(const char *path, const char *service, const char *account, char *out, int outlen) {
	@autoreleasepool {
		OSStatus status;
		SecKeychainRef kc = wasync_open(path, &status);
		if (status != errSecSuccess) {
			return (int)status;
		}
		if (wasync_locked(kc)) {
			if (kc != NULL) {
				CFRelease(kc);
			}
			return (int)errSecInteractionNotAllowed;
		}
		NSMutableDictionary *q = wasync_query(kc, service, account);
		q[(__bridge id)kSecReturnData] = @YES;
		q[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;
		__block CFTypeRef result = NULL;
		status = wasync_quiet(^{ return SecItemCopyMatching((__bridge CFDictionaryRef)q, &result); });
		if (kc != NULL) {
			CFRelease(kc);
		}
		if (status != errSecSuccess) {
			return (int)status;
		}
		if (result == NULL) {
			return (int)errSecInternalError;
		}
		NSData *data = (__bridge_transfer NSData *)result;
		if ((int)data.length >= outlen) {
			return 1;
		}
		memcpy(out, data.bytes, data.length);
		out[data.length] = 0;
		return 0;
	}
}

int wasync_kc_add(const char *path, const char *service, const char *account, const char *secret, const char *folder) {
	@autoreleasepool {
		OSStatus status;
		SecKeychainRef kc = wasync_open(path, &status);
		if (status != errSecSuccess) {
			return (int)status;
		}
		if (wasync_locked(kc)) {
			if (kc != NULL) {
				CFRelease(kc);
			}
			return (int)errSecInteractionNotAllowed;
		}
		NSMutableDictionary *add = wasync_query(NULL, service, account);
		if (kc != NULL) {
			add[(__bridge id)kSecUseKeychain] = (__bridge id)kc;
		}
		add[(__bridge id)kSecValueData] = [NSData dataWithBytes:secret length:strlen(secret)];
		add[(__bridge id)kSecAttrLabel] = @"wasync approval credential";
		add[(__bridge id)kSecAttrComment] = [NSString stringWithUTF8String:folder];
		status = wasync_quiet(^{ return SecItemAdd((__bridge CFDictionaryRef)add, NULL); });
		if (kc != NULL) {
			CFRelease(kc);
		}
		return (int)status;
	}
}

int wasync_kc_update(const char *path, const char *service, const char *account, const char *secret, const char *folder) {
	@autoreleasepool {
		OSStatus status;
		SecKeychainRef kc = wasync_open(path, &status);
		if (status != errSecSuccess) {
			return (int)status;
		}
		if (wasync_locked(kc)) {
			if (kc != NULL) {
				CFRelease(kc);
			}
			return (int)errSecInteractionNotAllowed;
		}
		NSDictionary *q = wasync_query(kc, service, account);
		NSDictionary *change = @{
			(__bridge id)kSecValueData: [NSData dataWithBytes:secret length:strlen(secret)],
			(__bridge id)kSecAttrComment: [NSString stringWithUTF8String:folder],
		};
		status = wasync_quiet(^{ return SecItemUpdate((__bridge CFDictionaryRef)q, (__bridge CFDictionaryRef)change); });
		if (kc != NULL) {
			CFRelease(kc);
		}
		return (int)status;
	}
}

int wasync_kc_delete(const char *path, const char *service, const char *account) {
	@autoreleasepool {
		OSStatus status;
		SecKeychainRef kc = wasync_open(path, &status);
		if (status != errSecSuccess) {
			return (int)status;
		}
		if (wasync_locked(kc)) {
			if (kc != NULL) {
				CFRelease(kc);
			}
			return (int)errSecInteractionNotAllowed;
		}
		NSDictionary *q = wasync_query(kc, service, account);
		status = wasync_quiet(^{ return SecItemDelete((__bridge CFDictionaryRef)q); });
		if (kc != NULL) {
			CFRelease(kc);
		}
		return (int)status;
	}
}

int wasync_kc_list(const char *path, const char *service, char **out, int *outlen) {
	@autoreleasepool {
		*out = NULL;
		*outlen = 0;
		OSStatus status;
		SecKeychainRef kc = wasync_open(path, &status);
		if (status != errSecSuccess) {
			return (int)status;
		}
		NSMutableDictionary *q = wasync_query(kc, service, NULL);
		q[(__bridge id)kSecReturnAttributes] = @YES;
		q[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitAll;
		__block CFTypeRef result = NULL;
		status = wasync_quiet(^{ return SecItemCopyMatching((__bridge CFDictionaryRef)q, &result); });
		if (kc != NULL) {
			CFRelease(kc);
		}
		if (status == errSecItemNotFound) {
			return 0;
		}
		if (status != errSecSuccess) {
			return (int)status;
		}
		NSArray *items = (__bridge_transfer NSArray *)result;
		NSMutableData *buf = [NSMutableData data];
		const char zero = 0;
		for (NSDictionary *item in items) {
			NSString *account = item[(__bridge id)kSecAttrAccount];
			NSString *folder = item[(__bridge id)kSecAttrComment];
			if (![account isKindOfClass:[NSString class]]) {
				continue;
			}
			if (![folder isKindOfClass:[NSString class]]) {
				folder = @"";
			}
			const char *a = [account UTF8String];
			const char *f = [folder UTF8String];
			[buf appendBytes:a length:strlen(a)];
			[buf appendBytes:&zero length:1];
			[buf appendBytes:f length:strlen(f)];
			[buf appendBytes:&zero length:1];
		}
		if (buf.length == 0) {
			return 0;
		}
		*out = malloc(buf.length);
		if (*out == NULL) {
			return (int)errSecAllocate;
		}
		memcpy(*out, buf.bytes, buf.length);
		*outlen = (int)buf.length;
		return 0;
	}
}

int wasync_kc_unlock(const char *path) {
	@autoreleasepool {
		OSStatus status;
		SecKeychainRef kc = wasync_open(path, &status);
		if (status != errSecSuccess) {
			return (int)status;
		}
		if (kc == NULL && (status = SecKeychainCopyDefault(&kc)) != errSecSuccess) {
			return (int)status;
		}
		SecKeychainStatus state = 0;
		if (SecKeychainGetStatus(kc, &state) == errSecSuccess && (state & kSecUnlockStateStatus)) {
			CFRelease(kc);
			return 0;
		}
		status = SecKeychainUnlock(kc, 0, NULL, false);
		CFRelease(kc);
		return (int)status;
	}
}

int wasync_kc_create(const char *path, const char *password) {
	@autoreleasepool {
		SecKeychainRef kc = NULL;
		OSStatus status = SecKeychainCreate(path, (UInt32)strlen(password), password, false, NULL, &kc);
		if (status != errSecSuccess) {
			return (int)status;
		}
		SecKeychainSettings settings = {SEC_KEYCHAIN_SETTINGS_VERS1, false, false, INT_MAX};
		status = SecKeychainSetSettings(kc, &settings);
		if (status == errSecSuccess) {
			status = SecKeychainUnlock(kc, (UInt32)strlen(password), password, true);
		}
		CFRelease(kc);
		return (int)status;
	}
}

int wasync_kc_signer(char *out, int outlen) {
	@autoreleasepool {
		out[0] = 0;
		SecCodeRef self = NULL;
		OSStatus status = SecCodeCopySelf(kSecCSDefaultFlags, &self);
		if (status != errSecSuccess) {
			return (int)status;
		}
		SecStaticCodeRef code = NULL;
		status = SecCodeCopyStaticCode(self, kSecCSDefaultFlags, &code);
		CFRelease(self);
		if (status != errSecSuccess) {
			return (int)status;
		}
		CFDictionaryRef info = NULL;
		status = SecCodeCopySigningInformation(code, kSecCSSigningInformation, &info);
		CFRelease(code);
		if (status != errSecSuccess) {
			return (int)status;
		}
		NSDictionary *d = (__bridge_transfer NSDictionary *)info;
		NSString *team = d[(__bridge id)kSecCodeInfoTeamIdentifier];
		NSString *ident = d[(__bridge id)kSecCodeInfoIdentifier];
		if (![team isKindOfClass:[NSString class]] || team.length == 0 || ![ident isKindOfClass:[NSString class]] || ident.length == 0) {
			return 0;
		}
		const char *s = [[NSString stringWithFormat:@"%@.%@", team, ident] UTF8String];
		if ((int)strlen(s) >= outlen) {
			return 1;
		}
		strcpy(out, s);
		return 0;
	}
}
