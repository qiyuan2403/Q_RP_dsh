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

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/session"
)

//export Java_com_sta1n_qrpdsh_Native_version
func Java_com_sta1n_qrpdsh_Native_version(env *C.JNIEnv, clazz C.jclass) C.jstring {
	return C.jni_string(env, C.CString(Version))
}

//export Java_com_sta1n_qrpdsh_Native_runTurn
func Java_com_sta1n_qrpdsh_Native_runTurn(env *C.JNIEnv, clazz C.jclass, task, home, workDir, baseURL, apiKey, model, nodePath, sessionID C.jstring) C.jstring {
	// Each Java String is copied out before use: the JNI reference is only
	// valid while the JVM keeps it alive, so the Go side must not retain it.
	args := []C.jstring{task, home, workDir, baseURL, apiKey, model, nodePath, sessionID}
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
		Task:      C.GoString(cp[0]),
		Home:      C.GoString(cp[1]),
		WorkDir:   C.GoString(cp[2]),
		BaseURL:   C.GoString(cp[3]),
		APIKey:    C.GoString(cp[4]),
		Model:     C.GoString(cp[5]),
		NodePath:  C.GoString(cp[6]),
		SessionID: C.GoString(cp[7]),
	}
	out := runTurnJSON(req)
	return C.jni_string(env, C.CString(out))
}

// openSessionNativeJSON opens (creating if needed) a session under home and
// remembers it. The response carries the session id and the full event
// history — the UI replays it to render the conversation it is resuming.
//
//export Java_com_sta1n_qrpdsh_Native_openSession
func Java_com_sta1n_qrpdsh_Native_openSession(env *C.JNIEnv, clazz C.jclass, home C.jstring) C.jstring {
	cHome := C.jni_cstr(env, home)
	goHome := C.GoString(cHome)
	defer C.free(unsafe.Pointer(cHome))
	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	// Reuse the newest existing session under home: the mobile client is
	// single-conversation, so "open" means "resume where we left off".
	entries, err := os.ReadDir(filepath.Join(goHome, "sessions"))
	if err == nil && len(entries) > 0 {
		// ReadDir is sorted by name; session ids are time-ordered ULIDs, so
		// the last entry is the newest.
		for i := len(entries) - 1; i >= 0; i-- {
			name := entries[i].Name()
			if log, err := session.Open(filepath.Join(goHome, "sessions", name)); err == nil {
				sessions[name] = log
				return C.jni_string(env, C.CString(sessionJSON(log, name)))
			}
		}
	}

	id := session.NewID()
	log, err := session.Create(filepath.Join(goHome, "sessions", id), session.Header{
		ID:        id,
		CreatedAt: time.Now().UnixMilli(),
		CWD:       goHome,
	})
	if err != nil {
		return C.jni_string(env, C.CString(`{"error":"`+err.Error()+`"}`))
	}
	sessions[id] = log
	return C.jni_string(env, C.CString(sessionJSON(log, id)))
}

// sessionJSON renders the id plus every recorded event for the UI replay.
func sessionJSON(log *session.Log, id string) string {
	events, err := log.Events()
	if events == nil {
		events = []session.Event{}
	}
	if err != nil {
		return `{"id":"` + id + `","error":"` + err.Error() + `"}`
	}
	raw, err := json.Marshal(map[string]any{"id": id, "events": events})
	if err != nil {
		return `{"id":"` + id + `","events":[]}`
	}
	return string(raw)
}

// closeSessionNative closes and forgets one session.
//
//export Java_com_sta1n_qrpdsh_Native_closeSession
func Java_com_sta1n_qrpdsh_Native_closeSession(env *C.JNIEnv, clazz C.jclass, id C.jstring) {
	cID := C.jni_cstr(env, id)
	goID := C.GoString(cID)
	defer C.free(unsafe.Pointer(cID))
	sessionsMu.Lock()
	log, ok := sessions[goID]
	delete(sessions, goID)
	sessionsMu.Unlock()
	if ok {
		_ = log.Close()
	}
}

// pollEventNative blocks until one live event for the session arrives, then
// returns it as JSON. Returns an empty string when the stream is over — the
// turn finished or the session is unknown. The caller polls on a dedicated
// Java worker thread, so every JNIEnv use here belongs to the JVM: no Go
// thread ever calls back into it.
//
//export Java_com_sta1n_qrpdsh_Native_pollEvent
func Java_com_sta1n_qrpdsh_Native_pollEvent(env *C.JNIEnv, clazz C.jclass, sessionID C.jstring, timeoutMs C.jint) C.jstring {
	cID := C.jni_cstr(env, sessionID)
	defer C.free(unsafe.Pointer(cID))
	goID := C.GoString(cID)

	streamsMu.Lock()
	ch, ok := eventStreams[goID]
	streamsMu.Unlock()
	if !ok {
		return C.jni_string(env, C.CString(""))
	}

	timeout := time.Duration(timeoutMs) * time.Millisecond
	select {
	case e, open := <-ch:
		if !open {
			return C.jni_string(env, C.CString(""))
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return C.jni_string(env, C.CString(""))
		}
		return C.jni_string(env, C.CString(string(raw)))
	case <-time.After(timeout):
		// No event within the window: an empty string means "poll again".
		return C.jni_string(env, C.CString(""))
	}
}
