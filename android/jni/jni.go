//go:build android

package main

/*
#include <stdlib.h>
#include <string.h>
#include <jni.h>

// A JNI method returning a Java String must return a jstring — a Java object
// reference — not a C char pointer. The JVM treats the return value as an
// object and hands it to the caller as such, so returning a `char*` makes ART
// dereference a data address as if it were a heap object.
//
// These helpers go through the JNIEnv function table, which is how a native
// method creates a Java string.
static jstring jni_string(JNIEnv *env, const char *utf8) {
	return (*env)->NewStringUTF(env, utf8);
}

// jni_cstr copies a Java String into a C string the caller must free.
// A null reference maps to an empty string so the Go side never sees a nil.
static char *jni_cstr(JNIEnv *env, jstring s) {
	if (s == NULL) {
		char *empty = (char *)malloc(1);
		if (empty != NULL) empty[0] = 0;
		return empty;
	}
	const char *utf = (*env)->GetStringUTFChars(env, s, NULL);
	if (utf == NULL) {
		char *empty = (char *)malloc(1);
		if (empty != NULL) empty[0] = 0;
		return empty;
	}
	char *copy = strdup(utf);
	(*env)->ReleaseStringUTFChars(env, s, utf);
	return copy;
}
*/
import "C"

import "unsafe"

//export Java_com_sta1n_qrpdsh_Native_version
func Java_com_sta1n_qrpdsh_Native_version(env *C.JNIEnv, clazz C.jclass) C.jstring {
	return C.jni_string(env, C.CString(Version))
}

//export Java_com_sta1n_qrpdsh_Native_runTurn
func Java_com_sta1n_qrpdsh_Native_runTurn(env *C.JNIEnv, clazz C.jclass, task, home, workDir, baseURL, apiKey, model, nodePath C.jstring) C.jstring {
	// Each Java String is copied out before use: the JNI reference is only
	// valid while the JVM keeps it alive, so the Go side must not retain it.
	args := []C.jstring{task, home, workDir, baseURL, apiKey, model, nodePath}
	cp := make([]*C.char, len(args))
	for i, a := range args {
		cp[i] = C.jni_cstr(env, a)
	}
	defer func() {
		for _, p := range cp {
			C.free(unsafe.Pointer(p))
		}
	}()

	req := runRequest{
		Task:     C.GoString(cp[0]),
		Home:     C.GoString(cp[1]),
		WorkDir:  C.GoString(cp[2]),
		BaseURL:  C.GoString(cp[3]),
		APIKey:   C.GoString(cp[4]),
		Model:    C.GoString(cp[5]),
		NodePath: C.GoString(cp[6]),
	}
	out := runTurnJSON(req)
	return C.jni_string(env, C.CString(out))
}
