package ingest

import "encoding/json"

type deliveryReasonPayload struct {
	EntityKind  string `json:"entity_kind,omitempty"`
	EntityValue string `json:"entity_value,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	Ref         string `json:"ref,omitempty"`
	Error       string `json:"error,omitempty"`
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
