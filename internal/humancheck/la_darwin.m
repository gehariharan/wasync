//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <LocalAuthentication/LocalAuthentication.h>
#include "la_darwin.h"

int wasync_la_available(void) {
	@autoreleasepool {
		LAContext *context = [[LAContext alloc] init];
		NSError *error = nil;
		return [context canEvaluatePolicy:LAPolicyDeviceOwnerAuthentication error:&error] ? 1 : 0;
	}
}

int wasync_la_verify(const char *reason, int timeout_ms) {
	@autoreleasepool {
		LAContext *context = [[LAContext alloc] init];
		NSError *error = nil;
		if (![context canEvaluatePolicy:LAPolicyDeviceOwnerAuthentication error:&error]) {
			return -1;
		}
		NSString *text = [NSString stringWithUTF8String:reason];
		if (text == nil) {
			return -1;
		}
		dispatch_semaphore_t done = dispatch_semaphore_create(0);
		__block int result = 0;
		[context evaluatePolicy:LAPolicyDeviceOwnerAuthentication
		        localizedReason:text
		                  reply:^(BOOL success, NSError *err) {
			result = success ? 1 : 0;
			dispatch_semaphore_signal(done);
		}];
		long timedOut = dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, (int64_t)timeout_ms * NSEC_PER_MSEC));
		if (timedOut) {
			[context invalidate];
			return 0;
		}
		return result;
	}
}
