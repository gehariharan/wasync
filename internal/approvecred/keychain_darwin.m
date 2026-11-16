//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <Security/Security.h>
#include <stdlib.h>
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

int wasync_kc_add(const char *service, const char *account, const char *secret, const char *folder) {
	@autoreleasepool {
		NSMutableDictionary *add = [wasync_query(service, account) mutableCopy];
		add[(__bridge id)kSecValueData] = [NSData dataWithBytes:secret length:strlen(secret)];
		add[(__bridge id)kSecAttrLabel] = @"wasync approval credential";
		add[(__bridge id)kSecAttrComment] = [NSString stringWithUTF8String:folder];
		return (int)SecItemAdd((__bridge CFDictionaryRef)add, NULL);
	}
}

int wasync_kc_update(const char *service, const char *account, const char *secret, const char *folder) {
	@autoreleasepool {
		NSDictionary *change = @{
			(__bridge id)kSecValueData: [NSData dataWithBytes:secret length:strlen(secret)],
			(__bridge id)kSecAttrComment: [NSString stringWithUTF8String:folder],
		};
		return (int)SecItemUpdate((__bridge CFDictionaryRef)wasync_query(service, account), (__bridge CFDictionaryRef)change);
	}
}

int wasync_kc_delete(const char *service, const char *account) {
	@autoreleasepool {
		return (int)SecItemDelete((__bridge CFDictionaryRef)wasync_query(service, account));
	}
}

int wasync_kc_list(const char *service, char **out, int *outlen) {
	@autoreleasepool {
		*out = NULL;
		*outlen = 0;
		NSDictionary *q = @{
			(__bridge id)kSecClass: (__bridge id)kSecClassGenericPassword,
			(__bridge id)kSecAttrService: [NSString stringWithUTF8String:service],
			(__bridge id)kSecReturnAttributes: @YES,
			(__bridge id)kSecMatchLimit: (__bridge id)kSecMatchLimitAll,
		};
		CFTypeRef result = NULL;
		OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)q, &result);
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
