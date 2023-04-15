package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/prometheus/client_golang/prometheus"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/instrument"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/aggregation"
	semconv "go.opentelemetry.io/otel/semconv/v1.18.0"
)

const (
	requestDurationName      = "http_request_duration_seconds"
	responseSizeName         = "http_response_size_bytes"
	FlagdProviderName        = "flagd"
	FeatureFlagReasonKeyName = "feature_flag.reason"
	ExceptionTypeKeyName     = "exception.type"
	FeatureFlagReasonKey     = attribute.Key(FeatureFlagReasonKeyName)
	ExceptionTypeKey         = attribute.Key(ExceptionTypeKeyName)
)

type MetricsRecorder struct {
	httpRequestDurHistogram   instrument.Float64Histogram
	httpResponseSizeHistogram instrument.Float64Histogram
	httpRequestsInflight      instrument.Int64UpDownCounter
	impressions               instrument.Int64Counter
	reasons                   instrument.Int64Counter
}

func (r MetricsRecorder) HTTPAttributes(svcName, url, method, code string) []attribute.KeyValue {
	return []attribute.KeyValue{
		semconv.ServiceNameKey.String(svcName),
		semconv.HTTPURLKey.String(url),
		semconv.HTTPMethodKey.String(method),
		semconv.HTTPStatusCodeKey.String(code),
	}
}

func (r MetricsRecorder) HTTPRequestDuration(ctx context.Context, duration time.Duration, attrs []attribute.KeyValue) {
	r.httpRequestDurHistogram.Record(ctx, duration.Seconds(), attrs...)
}

func (r MetricsRecorder) HTTPResponseSize(ctx context.Context, sizeBytes int64, attrs []attribute.KeyValue) {
	r.httpResponseSizeHistogram.Record(ctx, float64(sizeBytes), attrs...)
}

func (r MetricsRecorder) InFlightRequestStart(ctx context.Context, attrs []attribute.KeyValue) {
	r.httpRequestsInflight.Add(ctx, 1, attrs...)
}

func (r MetricsRecorder) InFlightRequestEnd(ctx context.Context, attrs []attribute.KeyValue) {
	r.httpRequestsInflight.Add(ctx, -1, attrs...)
}

func (r MetricsRecorder) Impressions(ctx context.Context, reason, variant, key string) {
	r.impressions.Add(ctx, 1, append(SemConvFeatureFlagAttributes(key, variant), FeatureFlagReason(reason))...)
}

func (r MetricsRecorder) Reasons(ctx context.Context, reason string, err error) {
	attrs := []attribute.KeyValue{
		semconv.FeatureFlagProviderName(FlagdProviderName),
		FeatureFlagReason(reason),
	}
	if err != nil {
		attrs = append(attrs, ExceptionType(err.Error()))
	}
	r.reasons.Add(ctx, 1, attrs...)
}

func (r MetricsRecorder) RecordEvaluation(ctx context.Context, err error, reason, variant, key string) {
	if err == nil {
		r.Impressions(ctx, reason, variant, key)
	}
	r.Reasons(ctx, reason, err)
}

func getDurationView(svcName, viewName string, bucket []float64) metric.View {
	return metric.NewView(
		metric.Instrument{
			// we change aggregation only for instruments with this name and scope
			Name: viewName,
			Scope: instrumentation.Scope{
				Name: svcName,
			},
		},
		metric.Stream{Aggregation: aggregation.ExplicitBucketHistogram{
			Boundaries: bucket,
		}},
	)
}

func FeatureFlagReason(val string) attribute.KeyValue {
	return FeatureFlagReasonKey.String(val)
}

func ExceptionType(val string) attribute.KeyValue {
	return ExceptionTypeKey.String(val)
}

// NewOTelRecorder creates a MetricsRecorder based on the provided metric.Reader. Note that, metric.NewMeterProvider is
// created here but not registered globally as this is the only place we derive a metric.Meter. Consider global provider
// registration if we need more meters
func NewOTelRecorder(exporter metric.Reader, resource *resource.Resource, serviceName string) *MetricsRecorder {
	// create a metric provider with custom bucket size for histograms
	provider := metric.NewMeterProvider(
		metric.WithReader(exporter),
		// for the request duration metric we use the default bucket size which are tailored for response time in seconds
		metric.WithView(getDurationView(requestDurationName, serviceName, prometheus.DefBuckets)),
		// for response size we want 8 exponential bucket starting from 100 Bytes
		metric.WithView(getDurationView(responseSizeName, serviceName, prometheus.ExponentialBuckets(100, 10, 8))),
		// set entity producing telemetry
		metric.WithResource(resource),
	)

	meter := provider.Meter(serviceName)

	// we can ignore errors from OpenTelemetry since they could occur if we select the wrong aggregator
	hduration, _ := meter.Float64Histogram(
		requestDurationName,
		instrument.WithDescription("The latency of the HTTP requests"),
	)
	hsize, _ := meter.Float64Histogram(
		responseSizeName,
		instrument.WithDescription("The size of the HTTP responses"),
		instrument.WithUnit("By"),
	)
	reqCounter, _ := meter.Int64UpDownCounter(
		"http_requests_inflight",
		instrument.WithDescription("The number of inflight requests being handled at the same time"),
	)
	impressions, _ := meter.Int64Counter(
		"impressions",
		instrument.WithDescription("The number of evaluations for a given flag"),
	)
	reasons, _ := meter.Int64Counter(
		"reasons",
		instrument.WithDescription("The number of evaluations for a given reason"),
	)
	return &MetricsRecorder{
		httpRequestDurHistogram:   hduration,
		httpResponseSizeHistogram: hsize,
		httpRequestsInflight:      reqCounter,
		impressions:               impressions,
		reasons:                   reasons,
	}
}