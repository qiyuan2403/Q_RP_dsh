// Package session persists a conversation as an append-only event log.
//
// WHY EVENTS RATHER THAN MESSAGES. A conversation is not a list of messages:
// it is a sequence of things that happened — a turn started, a model was
// called, a tool ran, a compaction rewrote history, a permission was decided.
// Storing the events keeps every one of those auditable and lets a projection
// derive the message list, rather than storing a derived view and losing the
// reasons behind it.
//
// COMPATIBILITY. The event vocabulary and the log format are aligned to the
// official harness (packages/core/session/src: `KNOWN_SESSION_EVENT_TYPES`,
// `SESSION_FORMAT_VERSION = 4`). A log written here must be readable there, and
// vice versa, so both the type names and the envelope field names are fixed.
package session

// FormatVersion is the logical log format version this build writes and reads.
//
// Aligned to the official `SESSION_FORMAT_VERSION`. A log carrying a different
// version is refused rather than guessed at: misreading a log reconstructs a
// wrong conversation, which is worse than refusing it.
const FormatVersion = 4

// KnownEventTypes is every event type this build understands.
//
// Copied from the official `KNOWN_SESSION_EVENT_TYPES` (which is itself
// generated from the event map). The read path REFUSES a log containing a type
// outside this set unless the event carries the `ignorable` marker: a log with
// an unknown required event was written by a newer harness, and skipping it
// would silently reconstruct a different session.
//
// This is why the list is explicit rather than derived at runtime: membership
// is a compatibility statement, not an implementation detail.
var KnownEventTypes = map[string]bool{
	"agent-preset/selected":                  true,
	"agent/inbox/spliced":                    true,
	"approval/asked":                         true,
	"approval/decided":                       true,
	"approval/policy":                        true,
	"assistant/attempt":                      true,
	"assistant/message":                      true,
	"command/done":                           true,
	"command/run":                            true,
	"compaction/end":                         true,
	"compaction/prune":                       true,
	"compaction/start":                       true,
	"compaction/summary":                     true,
	"deliverables/presented":                 true,
	"developer/message":                      true,
	"feedback/message-delete":                true,
	"feedback/message-put":                   true,
	"feedback/record":                        true,
	"goal/change":                            true,
	"hook/invoked":                           true,
	"hook/result":                            true,
	"image/offload":                          true,
	"job/registered":                         true,
	"job/settled":                            true,
	"llm/retry":                              true,
	"llm/retry-started":                      true,
	"model/selection":                        true,
	"permission/preset":                      true,
	"plan/mode":                              true,
	"request/context":                        true,
	"request/header":                         true,
	"sandbox/mode":                           true,
	"schedule/change":                        true,
	"session-log-deepseek/delivery-accepted": true,
	"session/end-seed":                       true,
	"session/title":                          true,
	"session/title-llm-request":              true,
	"step/end":                               true,
	"step/start":                             true,
	"subagent/catalog":                       true,
	"subagent/descriptor":                    true,
	"subagent/model-selection-policy":        true,
	"system/message":                         true,
	"team/member":                            true,
	"team/message/delivered":                 true,
	"team/message/queued":                    true,
	"team/task":                              true,
	"todo/write":                             true,
	"tool-workflow/agent-end":                true,
	"tool-workflow/agent-start":              true,
	"tool-workflow/run-end":                  true,
	"tool-workflow/run-start":                true,
	"tool/call":                              true,
	"tool/ptc-dispatch":                      true,
	"tool/ptc-dispatch-start":                true,
	"tool/result":                            true,
	"turn/end":                               true,
	"turn/start":                             true,
	"user/message":                           true,
	"web/deepseek-search-llm-request":        true,
	"working-directory/change":               true,
	"workspace/changes":                      true,
}

// SurfaceEventTypes are the event types whose events produce model-visible
// content.
//
// A surface event carries a surface intent (append, or a replace over a range);
// the rest are observational and never change what the model sees. Copied
// verbatim from the official `SurfaceEventType` union, which has exactly five
// members: a `tool/call` announces a call but its RESULT is what the model
// sees, and a compaction event rewrites history through other events rather
// than being one itself.
var SurfaceEventTypes = map[string]bool{
	"system/message":    true,
	"developer/message": true,
	"user/message":      true,
	"assistant/message": true,
	"tool/result":       true,
}

// MessageProjectionEventTypes are surface events whose model-visible effect
// requires an explicit interpreter rather than a plain append.
//
// Copied from the official `MESSAGE_PROJECTION_EVENT_TYPES`.
var MessageProjectionEventTypes = map[string]bool{
	"image/offload": true,
}

// IsKnown reports whether a type is part of this build's vocabulary.
func IsKnown(eventType string) bool { return KnownEventTypes[eventType] }

// IsSurface reports whether an event type produces model-visible content.
func IsSurface(eventType string) bool { return SurfaceEventTypes[eventType] }
