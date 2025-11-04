package fsm

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	otelAttrFSMName      = "fsm.name"
	otelAttrStateFrom    = "fsm.state.from"
	otelAttrStateTo      = "fsm.state.to"
	otelAttrEvent        = "fsm.event"
	otelAttrSessionID    = "fsm.session.id"
	otelAttrDataType     = "fsm.data.type"
	otelAttrTransition   = "fsm.transition"
	otelSpanNameTemplate = "%s %v --%v--> %v"
)

// WithOTelActionSpans wraps every action in an OpenTelemetry span.
// Attributes adicionadas:
//   - fsm.name, fsm.state.from, fsm.state.to, fsm.event, fsm.session.id
//   - fsm.data.type (quando há payload)
//
// O span é finalizado com status codes.Ok ou codes.Error, registrando o erro emitido pela action.
func WithOTelActionSpans[StateT, EventT Comparable, CtxT any](tr trace.Tracer) Middleware[StateT, EventT, CtxT] {
	if tr == nil {
		tr = otel.Tracer("fsm")
	}
	return func(next ActionFn[StateT, EventT, CtxT]) ActionFn[StateT, EventT, CtxT] {
		return func(ctx context.Context, s *Session[StateT, EventT, CtxT]) error {
			spanName := fmt.Sprintf(otelSpanNameTemplate, s.fsm.name, s.StateFrom, s.Event, s.StateTo)
			ctx, span := tr.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindInternal))

			attrs := []attribute.KeyValue{
				attribute.String(otelAttrFSMName, s.fsm.name),
				attribute.String(otelAttrStateFrom, fmt.Sprint(s.StateFrom)),
				attribute.String(otelAttrStateTo, fmt.Sprint(s.StateTo)),
				attribute.String(otelAttrEvent, fmt.Sprint(s.Event)),
				attribute.String(otelAttrSessionID, s.ID),
				attribute.String(otelAttrTransition, fmt.Sprintf("%v --%v--> %v", s.StateFrom, s.Event, s.StateTo)),
			}
			if s.Data != nil {
				attrs = append(attrs, attribute.String(otelAttrDataType, fmt.Sprintf("%T", s.Data)))
			}
			span.SetAttributes(attrs...)

			err := next(ctx, s)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			} else {
				span.SetStatus(codes.Ok, "")
			}
			span.End()
			return err
		}
	}
}
