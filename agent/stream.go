package agent

import (
	"fmt"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
)

// AssistantStreamFrame is one process-local live notification.
//
// Frames exist for live observers only: the durable fact of an assistant reply
// stays the session `assistant/message` event, and a reader that missed frames
// rebuilds the same conversation from the log. Nothing here is written to the
// log, so a frame is never a second source of truth.
type AssistantStreamFrame struct {
	// Type is "start", "chunk", or "end".
	Type string `json:"type"`
	// AttemptID identifies one model call: "<sessionID>:<n>", n from 1.
	AttemptID string `json:"attemptId"`
	// Revision is monotone across one Agent lifecycle, starting at 1.
	//
	// It spans attempts and turns, so an observer can tell a dropped frame
	// from the first frame of a new attempt.
	Revision int `json:"revision"`
	// Turn is the 1-based turn number; set on start only.
	//
	// Turn, Step, and Index are pointers so a required zero survives encoding:
	// the upstream union requires the field on its frame type, while step 0 and
	// index 0 are ordinary values. A plain int with omitempty would drop them
	// and make a present zero indistinguishable from a field the frame type
	// does not own.
	Turn *int `json:"turn,omitempty"`
	// Step is the 0-based step within the turn; set on start only.
	Step *int `json:"step,omitempty"`
	// Index is the dense 0-based chunk position, or the chunk count on end.
	//
	// It is set on chunk and end frames; an attempt that delivered no chunk
	// reports index 0, not an absent index.
	Index *int `json:"index,omitempty"`
	// Time is the chunk's Unix millisecond timestamp; set on chunk only.
	//
	// It is taken once when the loop observes the chunk, so a slow observer
	// cannot move an already-delivered chunk in time.
	Time int64 `json:"time,omitempty"`
	// Chunk is the incremental payload; set on chunk only.
	Chunk *AssistantStreamChunk `json:"chunk,omitempty"`
	// Outcome is the settlement; set on end only.
	Outcome *AssistantStreamOutcome `json:"outcome,omitempty"`
}

// AssistantStreamChunk is one frame's incremental model output.
type AssistantStreamChunk struct {
	// TextDelta appends visible assistant text.
	TextDelta string `json:"textDelta,omitempty"`
	// ToolCallDelta is one fragment of a requested tool call.
	ToolCallDelta *AssistantToolCallDelta `json:"toolCallDelta,omitempty"`
	// Usage is present on the chunk that reports final token accounting.
	Usage *AssistantUsage `json:"usage,omitempty"`
}

// AssistantToolCallDelta is a partial tool call.
//
// Argument text arrives as ordered fragments because the provider streams JSON
// it has not finished generating; only their concatenation is valid JSON.
type AssistantToolCallDelta struct {
	// Index selects the call this fragment belongs to.
	Index int `json:"index"`
	// ID and Name are usually supplied on the first fragment only.
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// ArgumentsDelta appends raw JSON argument text.
	ArgumentsDelta string `json:"argumentsDelta,omitempty"`
}

// AssistantUsage is token accounting reported by the provider.
type AssistantUsage struct {
	// PromptTokens counts the request tokens.
	PromptTokens int `json:"promptTokens,omitempty"`
	// CompletionTokens counts the generated tokens.
	CompletionTokens int `json:"completionTokens,omitempty"`
	// TotalTokens is the provider's own total, when it reports one.
	TotalTokens int `json:"totalTokens,omitempty"`
}

// AssistantStreamOutcome explains how an attempt ended.
type AssistantStreamOutcome struct {
	// Kind is "committed" or "abandoned".
	Kind string `json:"kind"`
	// EventType is the durable settlement type; committed only.
	EventType string `json:"eventType,omitempty"`
	// Seq is the committed event's sequence number; committed only.
	//
	// An observer uses it to attach the live frames to the exact log event
	// they produced instead of guessing from timing.
	Seq int64 `json:"seq,omitempty"`
}

// streamAttempt is one model call's live framing state.
//
// It owns the chunk counter and the terminal latch: a model call can fail at
// several points, and the observer must still see exactly one end frame. The
// first settlement wins and any later one is ignored, so the terminal frame is
// neither duplicated nor moved after the fact.
type streamAttempt struct {
	agent  *Agent
	id     string
	chunks int
	ended  bool
}

// intPtr marks a frame field present even when its value is zero.
//
// The position fields are pointers so present-zero can encode; the constructors
// below share this helper instead of repeating the address expression.
func intPtr(v int) *int { return &v }

// beginAttempt allocates one model call's identity and publishes its start
// frame before the provider is called.
//
// Starting before the call means an attempt that dies before its first chunk
// still has a matching terminal frame, which is what lets an observer close
// the live bubble it opened.
func (a *Agent) beginAttempt(turn, step int) *streamAttempt {
	a.attempts++
	attempt := &streamAttempt{
		agent: a,
		id:    fmt.Sprintf("%s:%d", a.sessionID(), a.attempts),
	}
	attempt.emit(AssistantStreamFrame{Type: "start", Turn: intPtr(turn), Step: intPtr(step)})
	return attempt
}

// push publishes one delivered chunk and advances the dense index.
//
// A chunk is framed before the loop interprets it: a malformed chunk the loop
// then rejects was still delivered to the observer, and the terminal frame's
// index must count it.
func (attempt *streamAttempt) push(chunk llm.StreamChunk) {
	attempt.emit(AssistantStreamFrame{
		Type:  "chunk",
		Index: intPtr(attempt.chunks),
		Time:  time.Now().UnixMilli(),
		Chunk: assistantStreamChunk(chunk),
	})
	attempt.chunks++
}

// commit publishes the terminal frame for a durable settlement that already
// succeeded.
//
// The seq is what makes this frame truthful: it is only emitted after the
// event is in the log, so a committed frame always names a durable event.
func (attempt *streamAttempt) commit(seq int64) {
	attempt.terminate(&AssistantStreamOutcome{
		Kind:      "committed",
		EventType: "assistant/message",
		Seq:       seq,
	})
}

// abandon publishes the terminal frame for an attempt with no durable
// settlement.
//
// It is safe on a nil attempt and safe to call twice: the non-streaming path
// has no attempt to settle, and a failure after a committed message must not
// retract it.
func (attempt *streamAttempt) abandon() {
	attempt.terminate(&AssistantStreamOutcome{Kind: "abandoned"})
}

func (attempt *streamAttempt) terminate(outcome *AssistantStreamOutcome) {
	if attempt == nil || attempt.ended {
		return
	}
	attempt.ended = true
	attempt.emit(AssistantStreamFrame{Type: "end", Index: intPtr(attempt.chunks), Outcome: outcome})
}

func (attempt *streamAttempt) emit(frame AssistantStreamFrame) {
	frame.AttemptID = attempt.id
	frame.Revision = attempt.agent.nextRevision()
	attempt.agent.emitStreamFrame(frame)
}

// nextRevision allocates the next frame revision.
//
// Revisions belong to the Agent, not to an attempt, so they stay monotone
// across model calls and turns.
func (a *Agent) nextRevision() int {
	a.revision++
	return a.revision
}

// emitStreamFrame delivers one frame to the configured observer.
//
// Delivery is synchronous on the turn's goroutine, which is also the only
// goroutine that emits frames: one Agent runs one turn at a time, so the
// observer sees a serial stream and needs no locking of its own.
func (a *Agent) emitStreamFrame(frame AssistantStreamFrame) {
	deliver := a.cfg.OnAssistantStream
	if deliver == nil {
		return
	}
	deliver(frame)
}

// sessionID is the id embedded in attempt ids.
//
// An agent built without a log still streams, so the id falls back to empty
// rather than dereferencing the missing log.
func (a *Agent) sessionID() string {
	if a.log == nil {
		return ""
	}
	return a.log.Header().ID
}

// assistantStreamChunk copies one provider chunk into its frame payload.
//
// The copy keeps the frame independent of the provider's buffers: a provider
// is free to reuse its chunk after delivery.
func assistantStreamChunk(chunk llm.StreamChunk) *AssistantStreamChunk {
	out := &AssistantStreamChunk{TextDelta: chunk.TextDelta}
	if delta := chunk.ToolCallDelta; delta != nil {
		out.ToolCallDelta = &AssistantToolCallDelta{
			Index:          delta.Index,
			ID:             delta.ID,
			Name:           delta.Name,
			ArgumentsDelta: delta.ArgumentsDelta,
		}
	}
	if usage := chunk.Usage; usage != nil {
		out.Usage = &AssistantUsage{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.TotalTokens,
		}
	}
	return out
}
