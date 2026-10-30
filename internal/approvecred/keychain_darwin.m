//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <Security/Security.h>
#include <string.h>
#include "keychain_darwin.h"

static NSDictionary *wasync_query(const char *service, const char *account) {
	return @{
		(__bridge id)kSecClass: (__bridge id)kSecClassGenericPassword,
		(__bridge id)kSecAttrService: [NSString stringWithUTF8String:service],
		(__bridge id)kSecAttrAccount: [NSString stringWithUTF8String:account],
	};
}

int wasync_kc_get(const char *service, const char *account, char *out, int outlen) {
	@autoreleasepool {
		NSMutableDictionary *q = [wasync_query(service, account) mutableCopy];
		q[(__bridge id)kSecReturnData] = @YES;
		q[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;
		CFTypeRef result = NULL;
		OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)q, &result);
		if (status == errSecItemNotFound) {
			return 1;
		}
		if (status == errSecUserCanceled || status == errSecAuthFailed || status == errSecInteractionNotAllowed) {
			return -3;
		}
		if (status != errSecSuccess || result == NULL) {
			return -1;
		}
		NSData *data = (__bridge_transfer NSData *)result;
		if ((int)data.length >= outlen) {
			return -2;
		}
		memcpy(out, data.bytes, data.length);
		out[data.length] = 0;
		return 0;
	}
}

int wasync_kc_set(const char *service, const char *account, const char *secret) {
	@autoreleasepool {
		NSData *data = [NSData dataWithBytes:secret length:strlen(secret)];
		NSMutableDictionary *add = [wasync_query(service, account) mutableCopy];
		add[(__bridge id)kSecValueData] = data;
		add[(__bridge id)kSecAttrLabel] = @"wasync approval credential";
		OSStatus status = SecItemAdd((__bridge CFDictionaryRef)add, NULL);
		if (status == errSecDuplicateItem) {
			status = SecItemUpdate((__bridge CFDictionaryRef)wasync_query(service, account),
			                       (__bridge CFDictionaryRef)@{(__bridge id)kSecValueData: data});
		}
		return status == errSecSuccess ? 0 : -1;
	}
}

int wasync_kc_delete(const char *service, const char *account) {
	@autoreleasepool {
		OSStatus status = SecItemDelete((__bridge CFDictionaryRef)wasync_query(service, account));
		if (status == errSecItemNotFound) {
			return 1;
		}
		return status == errSecSuccess ? 0 : -1;
	}
}
