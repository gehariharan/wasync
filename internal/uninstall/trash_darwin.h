#ifndef WASYNC_TRASH_DARWIN_H
#define WASYNC_TRASH_DARWIN_H

// wasync_trash moves path to the Bin. It returns 0 on success; otherwise it
// returns 1 and fills msg (NUL terminated, at most msglen-1 bytes) with the
// system's description of what went wrong.
int wasync_trash(const char *path, char *msg, int msglen);

#endif
