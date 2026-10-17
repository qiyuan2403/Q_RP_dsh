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
	"strings"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/session"
)

// jniString converts a Go string to a JVM string and releases the C copy.
//
// C.CString allocates memory outside the Go heap, and NewStringUTF copies the
// bytes into the JVM, so the C copy is dead the moment the call returns.
// Without the free every JNI call that returns a string leaks; pollEvent runs
// every 100ms while a turn streams, so even its empty-string answer would leak
// continuously.
func jniString(env *C.JNIEnv, s string) C.jstring {
	c := C.CString(s)
	defer C.free(unsafe.Pointer(c))
	return C.jni_string(env, c)
}

//export Java_com_sta1n_qrpdsh_Native_version
func Java_com_sta1n_qrpdsh_Native_version(env *C.JNIEnv, clazz C.jclass) C.jstring {
	return jniString(env, Version)
}

//export Java_com_sta1n_qrpdsh_Native_runTurn
func Java_com_sta1n_qrpdsh_Native_runTurn(env *C.JNIEnv, clazz C.jclass, task, home, workDir, baseURL, apiKey, model, nodePath, sessionID, searchAPIKey C.jstring) C.jstring {
	// Each Java String is copied out before use: the JNI reference is only
	// valid while the JVM keeps it alive, so the Go side must not retain it.
	args := []C.jstring{task, home, workDir, baseURL, apiKey, model, nodePath, sessionID, searchAPIKey}
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
		Task:         C.GoString(cp[0]),
		Home:         C.GoString(cp[1]),
		WorkDir:      C.GoString(cp[2]),
		BaseURL:      C.GoString(cp[3]),
		APIKey:       C.GoString(cp[4]),
		Model:        C.GoString(cp[5]),
		NodePath:     C.GoString(cp[6]),
		SessionID:    C.GoString(cp[7]),
		SearchAPIKey: C.GoString(cp[8]),
	}
	out := runTurnJSON(req)
	return jniString(env, out)
}

// Java_com_sta1n_qrpdsh_Native_cancelTurn requests cancellation of the running
// turn for one session.
//
// The call is deliberately fire-and-forget: the Java side cancels from a UI
// thread while the turn runs on its own worker, so there is nothing to wait
// for. An unknown session is a no-op — the turn may already have finished.
//
//export Java_com_sta1n_qrpdsh_Native_cancelTurn
func Java_com_sta1n_qrpdsh_Native_cancelTurn(env *C.JNIEnv, clazz C.jclass, sessionID C.jstring) {
	cID := C.jni_cstr(env, sessionID)
	goID := C.GoString(cID)
	C.free(unsafe.Pointer(cID))
	cancelTurnForSession(goID)
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
				return jniString(env, sessionJSON(log, name))
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
		return jniString(env, `{"error":"`+err.Error()+`"}`)
	}
	sessions[id] = log
	return jniString(env, sessionJSON(log, id))
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
// returns it as JSON. The channel is the session's merged stream: durable log
// events and the temporary agent/assistant-stream frames of the running turn
// arrive through this one call. Returns an empty string when the stream is over
// — the turn finished or the session is unknown. The caller polls on a
// dedicated Java worker thread, so every JNIEnv use here belongs to the JVM: no
// Go thread ever calls back into it.
//
//export Java_com_sta1n_qrpdsh_Native_pollEvent
func Java_com_sta1n_qrpdsh_Native_pollEvent(env *C.JNIEnv, clazz C.jclass, sessionID C.jstring, timeoutMs C.jint) C.jstring {
	cID := C.jni_cstr(env, sessionID)
	defer C.free(unsafe.Pointer(cID))
	goID := C.GoString(cID)

	streamsMu.Lock()
	stream, ok := eventStreams[goID]
	streamsMu.Unlock()
	if !ok {
		return jniString(env, "")
	}
	ch := stream.ch

	timeout := time.Duration(timeoutMs) * time.Millisecond
	select {
	case e, open := <-ch:
		if !open {
			return jniString(env, "")
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return jniString(env, "")
		}
		return jniString(env, string(raw))
	case <-time.After(timeout):
		// No event within the window: an empty string means "poll again".
		return jniString(env, "")
	}
}

// sessionRowJSON mirrors the official SessionNode fields the sidebar row
// renders: id, title, blank, running, updatedAt.
func sessionRowJSON(id, title string, blank, running bool, updatedAt int64) string {
	raw, err := json.Marshal(map[string]any{
		"id":        id,
		"title":     title,
		"blank":     blank,
		"running":   running,
		"updatedAt": updatedAt,
	})
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// sessionsNativeJSON lists every session under home, newest first
// (orderByRecency in ui-workspace tree.ts). The title is the first user
// message, truncated; a session with none is blank.
//
//export Java_com_sta1n_qrpdsh_Native_listSessions
func Java_com_sta1n_qrpdsh_Native_listSessions(env *C.JNIEnv, clazz C.jclass, home C.jstring) C.jstring {
	cHome := C.jni_cstr(env, home)
	defer C.free(unsafe.Pointer(cHome))
	goHome := C.GoString(cHome)

	entries, err := os.ReadDir(filepath.Join(goHome, "sessions"))
	if err != nil {
		return jniString(env, `[]`)
	}

	type row struct {
		json      string
		updatedAt int64
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		log, err := session.Open(filepath.Join(goHome, "sessions", id))
		if err != nil {
			continue
		}
		events, _ := log.Events()
		updatedAt := int64(0)
		title := ""
		running := false
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Time > updatedAt {
				updatedAt = events[i].Time
			}
		}
		for _, ev := range events {
			if ev.Type == "user/message" && title == "" {
				var data struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(ev.Data, &data) == nil && data.Text != "" {
					title = data.Text
					if len(title) > 60 {
						title = title[:60]
					}
				}
			}
		}
		header := log.Header()
		if header.CreatedAt > updatedAt {
			updatedAt = header.CreatedAt
		}
		_ = log.Close()
		rows = append(rows, row{
			json:      sessionRowJSON(id, title, title == "", running, updatedAt),
			updatedAt: updatedAt,
		})
	}
	// orderByRecency: newest first.
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].updatedAt > rows[i].updatedAt {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = r.json
	}
	return jniString(env, "["+strings.Join(parts, ",")+"]")
}

// openSessionIdNativeJSON opens one session by directory name and returns
// its replay document. A missing or unopenable session returns an error
// field — the caller refreshes the list rather than guessing.
//
//export Java_com_sta1n_qrpdsh_Native_openSessionId
func Java_com_sta1n_qrpdsh_Native_openSessionId(env *C.JNIEnv, clazz C.jclass, home, id C.jstring) C.jstring {
	cHome := C.jni_cstr(env, home)
	defer C.free(unsafe.Pointer(cHome))
	cID := C.jni_cstr(env, id)
	defer C.free(unsafe.Pointer(cID))
	goHome := C.GoString(cHome)
	goID := C.GoString(cID)

	// The id is a directory name; refuse traversal outright.
	if goID == "" || strings.Contains(goID, "/") || strings.Contains(goID, "..") {
		return jniString(env, `{"error":"invalid session id"}`)
	}

	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	// A session already open in this process keeps its live log — its
	// subscribers survive; reopening would orphan them.
	if log, ok := sessions[goID]; ok {
		return jniString(env, sessionJSON(log, goID))
	}
	log, err := session.Open(filepath.Join(goHome, "sessions", goID))
	if err != nil {
		return jniString(env, `{"error":"`+err.Error()+`"}`)
	}
	sessions[goID] = log
	return jniString(env, sessionJSON(log, goID))
}

// createSessionNativeJSON starts a new blank session (the official
// startSession): it does NOT reuse or resume anything.
//
//export Java_com_sta1n_qrpdsh_Native_createSession
func Java_com_sta1n_qrpdsh_Native_createSession(env *C.JNIEnv, clazz C.jclass, home C.jstring) C.jstring {
	cHome := C.jni_cstr(env, home)
	defer C.free(unsafe.Pointer(cHome))
	goHome := C.GoString(cHome)

	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	id := session.NewID()
	log, err := session.Create(filepath.Join(goHome, "sessions", id), session.Header{
		ID:        id,
		CreatedAt: time.Now().UnixMilli(),
		CWD:       goHome,
	})
	if err != nil {
		return jniString(env, `{"error":"`+err.Error()+`"}`)
	}
	sessions[id] = log
	return jniString(env, sessionJSON(log, id))
}
