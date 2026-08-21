package ingestiondiag

import (
	"context"

	"github.com/ibldzn/go-admin/internal/ingestionrun"
)

type Recorder func(context.Context, ingestionrun.TechnicalEvent, bool)

type Scope struct {
	RunID          uint64
	JobKey         string
	Class          string
	Step           string
	Operation      string
	ItemIdentifier string
	MemberKey      string
}

type state struct {
	recorder Recorder
	scope    Scope
}

type contextKey struct{}

func WithRecorder(ctx context.Context, recorder Recorder, runID uint64, jobKey string) context.Context {
	return context.WithValue(ctx, contextKey{}, state{recorder: recorder, scope: Scope{RunID: runID, JobKey: jobKey}})
}

func WithScope(ctx context.Context, scope Scope) context.Context {
	current, _ := ctx.Value(contextKey{}).(state)
	if scope.RunID == 0 {
		scope.RunID = current.scope.RunID
	}
	if scope.JobKey == "" {
		scope.JobKey = current.scope.JobKey
	}
	current.scope = scope
	return context.WithValue(ctx, contextKey{}, current)
}

func Event(ctx context.Context) ingestionrun.TechnicalEvent {
	current, _ := ctx.Value(contextKey{}).(state)
	return ingestionrun.TechnicalEvent{RunID: current.scope.RunID, JobKey: current.scope.JobKey, Class: current.scope.Class,
		Step: current.scope.Step, Operation: current.scope.Operation, ItemIdentifier: current.scope.ItemIdentifier, MemberKey: current.scope.MemberKey}
}

func Record(ctx context.Context, event ingestionrun.TechnicalEvent, aggregate bool) {
	current, _ := ctx.Value(contextKey{}).(state)
	if current.recorder == nil {
		return
	}
	if event.RunID == 0 {
		event.RunID = current.scope.RunID
	}
	if event.JobKey == "" {
		event.JobKey = current.scope.JobKey
	}
	if event.Class == "" {
		event.Class = current.scope.Class
	}
	if event.Step == "" {
		event.Step = current.scope.Step
	}
	if event.Operation == "" {
		event.Operation = current.scope.Operation
	}
	if event.ItemIdentifier == "" {
		event.ItemIdentifier = current.scope.ItemIdentifier
	}
	if event.MemberKey == "" {
		event.MemberKey = current.scope.MemberKey
	}
	current.recorder(ctx, event, aggregate)
}
