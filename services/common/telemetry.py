import contextlib
from typing import Optional
from opentelemetry import trace
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.sampling import ALWAYS_ON

# Initialize global TracerProvider
_provider = TracerProvider(sampler=ALWAYS_ON)
trace.set_tracer_provider(_provider)
tracer = trace.get_tracer("arxivlens-worker")
_propagator = TraceContextTextMapPropagator()


def extract_traceparent(traceparent: Optional[str]) -> trace.SpanContext:
    """Extract OpenTelemetry SpanContext from a W3C traceparent string."""
    if not traceparent:
        return trace.INVALID_SPAN_CONTEXT
    carrier = {"traceparent": traceparent}
    ctx = _propagator.extract(carrier=carrier)
    return trace.get_current_span(ctx).get_span_context()


def inject_traceparent(span: trace.Span) -> Optional[str]:
    """Inject W3C traceparent string from an active span."""
    if not span or not span.get_span_context().is_valid:
        return None
    carrier = {}
    _propagator.inject(carrier=carrier)
    return carrier.get("traceparent")


@contextlib.contextmanager
def start_trace_span(span_name: str, parent_traceparent: Optional[str] = None):
    """Context manager for tracing across distributed Kafka/HTTP boundaries."""
    if parent_traceparent:
        carrier = {"traceparent": parent_traceparent}
        parent_ctx = _propagator.extract(carrier=carrier)
        with tracer.start_as_current_span(span_name, context=parent_ctx) as span:
            yield span
    else:
        with tracer.start_as_current_span(span_name) as span:
            yield span
