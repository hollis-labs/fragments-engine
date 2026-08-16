package ingest

import "encoding/json"

type deliveryReasonPayload struct {
	EntityKind      string `json:"entity_kind,omitempty"`
	EntityValue     string `json:"entity_value,omitempty"`
	Attempts        int    `json:"attempts,omitempty"`
	Ref             string `json:"ref,omitempty"`
	Error           string `json:"error,omitempty"`
	DestinationKind string `json:"destination_kind,omitempty"`
}

func encodeDeliveryReason(prefix string, payload deliveryReasonPayload) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return prefix + ":" + `{"error":"encode_delivery_reason"}`
	}
	return prefix + ":" + string(raw)
}

func EncodeManualRouteSuccess(kind, value string, attempts int, ref string) string {
	return encodeDeliveryReason("manual_route_entity", deliveryReasonPayload{
		EntityKind:  kind,
		EntityValue: value,
		Attempts:    attempts,
		Ref:         ref,
	})
}

func EncodeManualRouteError(kind, value string, attempts int, errText string) string {
	return encodeDeliveryReason("manual_route_error", deliveryReasonPayload{
		EntityKind:  kind,
		EntityValue: value,
		Attempts:    attempts,
		Error:       errText,
	})
}

func EncodeQueuedDeliverySuccess(attempts int, ref string) string {
	return encodeDeliveryReason("queued_delivery_success", deliveryReasonPayload{
		Attempts: attempts,
		Ref:      ref,
	})
}

func EncodeQueuedDeliveryDeadLetter(attempts int, errText string) string {
	return encodeDeliveryReason("queued_delivery_dead_letter", deliveryReasonPayload{
		Attempts: attempts,
		Error:    errText,
	})
}

func EncodeDeliveryQueued(attempts int, errText string) string {
	return encodeDeliveryReason("delivery_queued", deliveryReasonPayload{
		Attempts: attempts,
		Error:    errText,
	})
}

// EncodeDeliveryQueuedByDesign records a delivery that was enqueued
// intentionally, without ever attempting an inline synchronous delivery
// first. This is the "callback" destination case: CallbackDestinationConfig
// must never fire synchronously against Curator's endpoint (see
// domain.CallbackDestinationConfig's doc comment), so the routing hot path
// skips straight to EnqueueDestinationRetry instead of trying-then-falling-
// back like the other four destination kinds.
//
// This is deliberately a distinct prefix from EncodeDeliveryQueued, which
// records a retry queued *after* an inline attempt failed. Both land the job
// in the same delivery_jobs queue, but they mean different things in
// route_log: "queued by design, no attempt was made" vs. "queued because the
// attempt just made failed". Collapsing them into one prefix would make
// every callback dispatch read as a failure in route_log, which it isn't.
func EncodeDeliveryQueuedByDesign(destinationKind string) string {
	return encodeDeliveryReason("delivery_queued_by_design", deliveryReasonPayload{
		DestinationKind: destinationKind,
	})
}
