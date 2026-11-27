//go:build darwin && cgo

#import <Foundation/Foundation.h>
#include <string.h>
#include "trash_darwin.h"

int wasync_trash(const char *path, char *msg, int msglen) {
	@autoreleasepool {
		NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
		NSError *error = nil;
		if ([[NSFileManager defaultManager] trashItemAtURL:url resultingItemURL:nil error:&error]) {
			return 0;
		}
		if (msglen > 0) {
			msg[0] = 0;
			const char *text = error ? [[error localizedDescription] UTF8String] : NULL;
			if (text) {
				strncpy(msg, text, msglen - 1);
				msg[msglen - 1] = 0;
			}
		}
		return 1;
	}
}
