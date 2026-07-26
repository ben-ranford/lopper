//go:build linux && cgo

package safeio

/*
#define _GNU_SOURCE
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

static char *safeio_copy_go_string(_GoString_ source) {
	size_t length = (size_t)_GoStringLen(source);
	char *copy = malloc(length + 1);
	if (copy == NULL) {
		return NULL;
	}
	memcpy(copy, _GoStringPtr(source), length);
	copy[length] = '\0';
	return copy;
}

static int safeio_renameat_noreplace(
	int dirfd,
	_GoString_ old_name_source,
	_GoString_ new_name_source,
	int *saved_errno
) {
	char *old_name = safeio_copy_go_string(old_name_source);
	if (old_name == NULL) {
		*saved_errno = ENOMEM;
		return -1;
	}
	char *new_name = safeio_copy_go_string(new_name_source);
	if (new_name == NULL) {
		free(old_name);
		*saved_errno = ENOMEM;
		return -1;
	}
	int result = (int)syscall(SYS_renameat2, dirfd, old_name, dirfd, new_name, RENAME_NOREPLACE);
	*saved_errno = errno;
	free(old_name);
	free(new_name);
	return result;
}
*/
import "C"

import "syscall"

func renameNoReplaceInDirectory(fd uintptr, oldName, newName string) error {
	var savedErrno C.int
	if C.safeio_renameat_noreplace(C.int(fd), oldName, newName, &savedErrno) != 0 {
		return syscall.Errno(savedErrno)
	}
	return nil
}
