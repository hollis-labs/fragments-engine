package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	queue "github.com/hollis-labs/go-queue"
	qsqlite "github.com/hollis-labs/go-queue/driver/sqlite"
)

const (
	deliveryQueueName   = "destination_delivery"
	deliveryQueueJobKey = "destination_delivery"
)

type DeliveryQueueService struct {
	db          *sql.DB
	queue       queue.Queue
	routes      *repository.RoutingRepository
	fragments   *repository.FragmentRepository
	entities    *repository.EntityRepository
	attachments *repository.AttachmentRepository
	inbox       *repository.InboxRepository
	delivery    config.DeliveryConfig
	cfg         config.QueueConfig
}

type deliveryJobPayload struct {
	FragmentID    string `json:"fragment_id"`
	RouteID       string `json:"route_id"`
	DestinationID string `json:"destination_id"`
	BackoffMS     int    `json:"backoff_ms"`
}

type DeliveryQueueStats struct {
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
}

type queueEventDetail struct {
	Error    string `json:"error,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
	Ref      string `json:"ref,omitempty"`
}

func (s *DeliveryQueueService) ListPending(ctx context.Context, destinationID string, limit int) ([]domain.PendingDeliveryJob, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
SELECT id, queue, type, payload, attempts, max_tries, available_at, reserved_at, created_at
FROM delivery_jobs
WHERE (? = '' OR json_extract(payload, '$.destination_id') = ?)
ORDER BY id ASC
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, destinationID, destinationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending delivery jobs: %w", err)
	}
	defer rows.Close()

	var out []domain.PendingDeliveryJob
	for rows.Next() {
		var (
			item        domain.PendingDeliveryJob
			payloadRaw  []byte
			availableAt int64
			reservedAt  sql.NullInt64
			createdAt   int64
		)
		if err := rows.Scan(&item.ID, &item.Queue, &item.Type, &payloadRaw, &item.Attempts, &item.MaxTries, &availableAt, &reservedAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan pending delivery job: %w", err)
		}
		item.AvailableAt = time.Unix(availableAt, 0).UTC()
		item.CreatedAt = time.Unix(createdAt, 0).UTC()
		if reservedAt.Valid {
			ts := time.Unix(reservedAt.Int64, 0).UTC()
			item.ReservedAt = &ts
		}
		var payload deliveryJobPayload
		if err := json.Unmarshal(payloadRaw, &payload); err == nil {
			item.FragmentID = payload.FragmentID
			item.RouteID = payload.RouteID
			item.DestinationID = payload.DestinationID
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *DeliveryQueueService) ListFailed(ctx context.Context, destinationID string, limit int) ([]domain.FailedDeliveryJob, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
SELECT id, queue, type, payload, error, attempts, failed_at
FROM delivery_failed_jobs
WHERE (? = '' OR json_extract(payload, '$.destination_id') = ?)
ORDER BY id DESC
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, destinationID, destinationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list failed delivery jobs: %w", err)
	}
	defer rows.Close()

	var out []domain.FailedDeliveryJob
	for rows.Next() {
		var (
			item       domain.FailedDeliveryJob
			payloadRaw []byte
			failedAt   int64
		)
		if err := rows.Scan(&item.ID, &item.Queue, &item.Type, &payloadRaw, &item.Error, &item.Attempts, &failedAt); err != nil {
			return nil, fmt.Errorf("scan failed delivery job: %w", err)
		}
		item.FailedAt = time.Unix(failedAt, 0).UTC()
		var payload deliveryJobPayload
		if err := json.Unmarshal(payloadRaw, &payload); err == nil {
			item.FragmentID = payload.FragmentID
			item.RouteID = payload.RouteID
			item.DestinationID = payload.DestinationID
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *DeliveryQueueService) ListEvents(ctx context.Context, destinationID string, limit int) ([]domain.QueueJobEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, failed_job_id, fragment_id, route_id, destination_id, event_type, detail_json, created_at
FROM queue_job_events
WHERE (? = '' OR destination_id = ?)
ORDER BY id DESC
LIMIT ?`, destinationID, destinationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list queue job events: %w", err)
	}
	defer rows.Close()

	var out []domain.QueueJobEvent
	for rows.Next() {
		var (
			item        domain.QueueJobEvent
			failedJobID sql.NullInt64
			createdAt   string
		)
		if err := rows.Scan(&item.ID, &failedJobID, &item.FragmentID, &item.RouteID, &item.DestinationID, &item.EventType, &item.DetailJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan queue job event: %w", err)
		}
		if failedJobID.Valid {
			v := failedJobID.Int64
			item.FailedJobID = &v
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *DeliveryQueueService) ListDestinationSummaries(ctx context.Context, limit int) ([]domain.QueueDestinationSummary, error) {
	destinations, err := s.routes.ListDestinations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list destinations for queue summary: %w", err)
	}
	if limit > 0 && len(destinations) > limit {
		destinations = destinations[:limit]
	}

	out := make([]domain.QueueDestinationSummary, 0, len(destinations))
	for _, destination := range destinations {
		status, err := s.getDestinationStatus(ctx, destination.ID)
		if err != nil {
			return nil, fmt.Errorf("destination status for queue summary: %w", err)
		}
		summary, err := s.destinationSummary(ctx, destination, status)
		if err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	return out, nil
}

func (s *DeliveryQueueService) ReplayFailed(ctx context.Context, id int64, force bool) error {
	var (
		queueName  string
		jobType    string
		payloadRaw []byte
	)
	if err := s.db.QueryRowContext(ctx, `
SELECT queue, type, payload
FROM delivery_failed_jobs
WHERE id = ?`, id).Scan(&queueName, &jobType, &payloadRaw); err != nil {
		return fmt.Errorf("get failed delivery job: %w", err)
	}
	var payload deliveryJobPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return fmt.Errorf("decode failed delivery payload: %w", err)
	}
	destination, err := s.routes.GetDestination(ctx, payload.DestinationID)
	if err != nil {
		return fmt.Errorf("get replay destination: %w", err)
	}
	if !force {
		status, err := s.getDestinationStatus(ctx, destination.ID)
		if err != nil {
			return fmt.Errorf("preflight destination status: %w", err)
		}
		if !status.ConfigValid {
			return fmt.Errorf("destination %s config invalid: %s; retry with force to override", destination.ID, status.ConfigError)
		}
		if !status.Reachable {
			reason := status.Reachability
			if reason == "" {
				reason = "unreachable"
			}
			return fmt.Errorf("destination %s unreachable: %s; retry with force to override", destination.ID, reason)
		}
		if err := s.validateReplayPolicy(ctx, destination); err != nil {
			return fmt.Errorf("%w; retry with force to override", err)
		}
	}
	retry := destinationRetryConfig(destination, s.delivery)
	if retry.MaxAttempts <= 0 {
		retry.MaxAttempts = 3
	}
	if err := s.queue.Push(ctx, jobType, payloadRaw,
		queue.OnQueue(queueName),
		queue.WithMaxTries(retry.MaxAttempts),
	); err != nil {
		return fmt.Errorf("requeue failed delivery job: %w", err)
	}
	if err := s.logEvent(ctx, &id, payload, "replay", queueEventDetail{}); err != nil {
		return fmt.Errorf("log replay queue event: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM delivery_failed_jobs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete failed delivery job after replay: %w", err)
	}
	return nil
}

func (s *DeliveryQueueService) getDestinationStatus(ctx context.Context, destinationID string) (domain.DestinationStatus, error) {
	item, err := s.routes.GetDestination(ctx, destinationID)
	if err != nil {
		return domain.DestinationStatus{}, err
	}
	statusSvc := NewRoutingService(s.routes, s.fragments, s.entities, s.inbox, s.delivery, s.cfg)
	statusSvc.SetAttachmentRepository(s.attachments)
	return statusSvc.destinationStatus(ctx, item)
}

func (s *DeliveryQueueService) PurgeFailed(ctx context.Context, id int64) error {
	var payloadRaw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM delivery_failed_jobs WHERE id = ?`, id).Scan(&payloadRaw); err != nil {
		return fmt.Errorf("get failed delivery job for purge: %w", err)
	}
	var payload deliveryJobPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return fmt.Errorf("decode failed delivery payload for purge: %w", err)
	}
	if err := s.logEvent(ctx, &id, payload, "purge", queueEventDetail{}); err != nil {
		return fmt.Errorf("log purge queue event: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM delivery_failed_jobs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("purge failed delivery job: %w", err)
	}
	return nil
}

func NewDeliveryQueueService(db *sql.DB, routes *repository.RoutingRepository, fragments *repository.FragmentRepository, entities *repository.EntityRepository, inbox *repository.InboxRepository, delivery config.DeliveryConfig, cfg config.QueueConfig) (*DeliveryQueueService, error) {
	driver, err := qsqlite.New(db, qsqlite.Opts{
		Table:       "delivery_jobs",
		FailedTable: "delivery_failed_jobs",
		RetryAfter:  60 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("open delivery queue: %w", err)
	}
	return &DeliveryQueueService{
		db:        db,
		queue:     driver,
		routes:    routes,
		fragments: fragments,
		entities:  entities,
		inbox:     inbox,
		delivery:  delivery,
		cfg:       cfg,
	}, nil
}

func (s *DeliveryQueueService) SetAttachmentRepository(repo *repository.AttachmentRepository) {
	s.attachments = repo
}

func (s *DeliveryQueueService) EnqueueDestinationRetry(ctx context.Context, fragmentID, routeID, destinationID string, retry domain.DeliveryRetryConfig) error {
	payload := deliveryJobPayload{
		FragmentID:    fragmentID,
		RouteID:       routeID,
		DestinationID: destinationID,
		BackoffMS:     retry.BackoffMS,
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode delivery queue payload: %w", err)
	}
	maxTries := retry.MaxAttempts
	if maxTries <= 0 {
		maxTries = 3
	}
	if err := s.queue.Push(ctx, deliveryQueueJobKey, payloadRaw,
		queue.OnQueue(deliveryQueueName),
		queue.WithMaxTries(maxTries),
	); err != nil {
		return err
	}
	if err := s.logEvent(ctx, nil, payload, "enqueue", queueEventDetail{}); err != nil {
		return fmt.Errorf("log enqueue queue event: %w", err)
	}
	return nil
}

func (s *DeliveryQueueService) Drain(ctx context.Context, limit int) (int, error) {
	processed := 0
	for limit <= 0 || processed < limit {
		job, err := s.queue.Pop(ctx, deliveryQueueName)
		if err != nil {
			return processed, fmt.Errorf("pop delivery job: %w", err)
		}
		if job == nil {
			return processed, nil
		}
		if err := s.processJob(ctx, job); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (s *DeliveryQueueService) Stats(ctx context.Context) (DeliveryQueueStats, error) {
	pending, err := s.queue.Size(ctx, deliveryQueueName)
	if err != nil {
		return DeliveryQueueStats{}, fmt.Errorf("delivery queue size: %w", err)
	}
	var failed int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_failed_jobs`).Scan(&failed); err != nil {
		return DeliveryQueueStats{}, fmt.Errorf("delivery failed queue size: %w", err)
	}
	return DeliveryQueueStats{Pending: pending, Failed: failed}, nil
}

func (s *DeliveryQueueService) processJob(ctx context.Context, job *queue.QueuedJob) error {
	var payload deliveryJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		_ = s.queue.Failed(ctx, job, err.Error())
		return fmt.Errorf("decode delivery job payload: %w", err)
	}

	fragment, err := s.fragments.GetByID(ctx, payload.FragmentID)
	if err != nil {
		return s.failJob(ctx, job, payload, fmt.Errorf("get fragment: %w", err))
	}
	payload.FragmentID = fragment.ID
	destination, err := s.routes.GetDestination(ctx, payload.DestinationID)
	if err != nil {
		return s.failJob(ctx, job, payload, fmt.Errorf("get destination: %w", err))
	}
	var attachments []domain.FragmentAttachment
	if s.attachments != nil {
		attachments, err = s.attachments.ListByFragment(ctx, payload.FragmentID)
		if err != nil {
			return s.failJob(ctx, job, payload, fmt.Errorf("get attachments: %w", err))
		}
	}

	routed := fragment
	routed.Status = domain.FragmentStatusRouted
	result, err := ingest.ExecuteDestinationWithRetry(ctx, destination, routed, attachments)
	if err == nil {
		if err := s.fragments.UpdateStatus(ctx, payload.FragmentID, domain.FragmentStatusRouted); err != nil {
			return fmt.Errorf("update routed status: %w", err)
		}
		if s.attachments != nil && len(result.PublishedAttachments) > 0 {
			if err := s.attachments.UpdateFragmentAttachmentStoragePaths(ctx, payload.FragmentID, result.PublishedAttachments); err != nil {
				return fmt.Errorf("update queued attachment storage paths: %w", err)
			}
		}
		if err := s.inbox.Remove(ctx, payload.FragmentID); err != nil {
			return fmt.Errorf("remove inbox item: %w", err)
		}
		if err := s.routes.LogDecision(ctx, domain.RouteLogEntry{
			FragmentID:    payload.FragmentID,
			RouteID:       payload.RouteID,
			DestinationID: payload.DestinationID,
			Decision:      "queued_route",
			Reason:        ingest.EncodeQueuedDeliverySuccess(result.Attempts, result.Ref),
			CreatedAt:     time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("log queued delivery success: %w", err)
		}
		if err := s.queue.Delete(ctx, job.ID); err != nil {
			return fmt.Errorf("delete queued job: %w", err)
		}
		if err := s.logEvent(ctx, nil, payload, "delivered", queueEventDetail{
			Attempts: result.Attempts,
			Ref:      result.Ref,
		}); err != nil {
			return fmt.Errorf("log delivered queue event: %w", err)
		}
		return nil
	}

	maxTries := job.MaxTries
	if maxTries <= 0 {
		maxTries = 3
	}
	if job.Attempts >= maxTries {
		return s.failJob(ctx, job, payload, err)
	}
	if err := s.queue.Release(ctx, job.ID, time.Duration(payload.BackoffMS)*time.Millisecond); err != nil {
		return fmt.Errorf("release queued job: %w", err)
	}
	return nil
}

func (s *DeliveryQueueService) failJob(ctx context.Context, job *queue.QueuedJob, payload deliveryJobPayload, err error) error {
	if failErr := s.queue.Failed(ctx, job, err.Error()); failErr != nil {
		return fmt.Errorf("mark delivery job failed: %w", failErr)
	}
	var failedJobID *int64
	var id int64
	if scanErr := s.db.QueryRowContext(ctx, `
SELECT id
FROM delivery_failed_jobs
WHERE queue = ? AND type = ? AND json_extract(payload, '$.fragment_id') = ? AND json_extract(payload, '$.destination_id') = ?
ORDER BY id DESC
LIMIT 1`, deliveryQueueName, deliveryQueueJobKey, payload.FragmentID, payload.DestinationID).Scan(&id); scanErr == nil {
		failedJobID = &id
	}
	if logErr := s.routes.LogDecision(ctx, domain.RouteLogEntry{
		FragmentID:    payload.FragmentID,
		RouteID:       payload.RouteID,
		DestinationID: payload.DestinationID,
		Decision:      "inbox",
		Reason:        ingest.EncodeQueuedDeliveryDeadLetter(job.Attempts, err.Error()),
		CreatedAt:     time.Now().UTC(),
	}); logErr != nil {
		return fmt.Errorf("log queued delivery dead-letter: %w", logErr)
	}
	if eventErr := s.logEvent(ctx, failedJobID, payload, "dead_letter", queueEventDetail{
		Attempts: job.Attempts,
		Error:    err.Error(),
	}); eventErr != nil {
		return fmt.Errorf("log dead-letter queue event: %w", eventErr)
	}
	return nil
}

func (s *DeliveryQueueService) logEvent(ctx context.Context, failedJobID *int64, payload deliveryJobPayload, eventType string, detail queueEventDetail) error {
	detailRaw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode queue event detail: %w", err)
	}
	var failedValue any
	if failedJobID != nil {
		failedValue = *failedJobID
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO queue_job_events (failed_job_id, fragment_id, route_id, destination_id, event_type, detail_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		failedValue,
		payload.FragmentID,
		payload.RouteID,
		payload.DestinationID,
		eventType,
		string(detailRaw),
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert queue event: %w", err)
	}
	return nil
}

func (s *DeliveryQueueService) destinationSummary(ctx context.Context, destination domain.Destination, status domain.DestinationStatus) (domain.QueueDestinationSummary, error) {
	policy := effectiveQueueConfig(destination, s.cfg)
	summary := domain.QueueDestinationSummary{
		DestinationID:   status.Destination.ID,
		DestinationName: status.Destination.Name,
		Provider:        status.Provider,
		ConfigValid:     status.ConfigValid,
		Reachable:       status.Reachable,
	}

	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM delivery_jobs
WHERE json_extract(payload, '$.destination_id') = ?`, status.Destination.ID).Scan(&summary.PendingCount); err != nil {
		return domain.QueueDestinationSummary{}, fmt.Errorf("count pending queue jobs by destination: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM delivery_failed_jobs
WHERE json_extract(payload, '$.destination_id') = ?`, status.Destination.ID).Scan(&summary.FailedCount); err != nil {
		return domain.QueueDestinationSummary{}, fmt.Errorf("count failed queue jobs by destination: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT event_type, detail_json, created_at
FROM queue_job_events
WHERE destination_id = ?
ORDER BY id DESC`, status.Destination.ID)
	if err != nil {
		return domain.QueueDestinationSummary{}, fmt.Errorf("list queue events by destination: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			eventType  string
			detailJSON string
			createdAt  string
		)
		if err := rows.Scan(&eventType, &detailJSON, &createdAt); err != nil {
			return domain.QueueDestinationSummary{}, fmt.Errorf("scan queue event for summary: %w", err)
		}
		ts, _ := time.Parse(time.RFC3339, createdAt)
		if summary.LastEventAt == nil {
			summary.LastEventAt = &ts
		}
		switch eventType {
		case "replay":
			summary.ReplayCount++
		case "purge":
			summary.PurgeCount++
		case "dead_letter":
			summary.DeadLetterCount++
			if summary.LastFailureAt == nil {
				summary.LastFailureAt = &ts
				var detail queueEventDetail
				if err := json.Unmarshal([]byte(detailJSON), &detail); err == nil {
					summary.LastFailureError = detail.Error
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return domain.QueueDestinationSummary{}, fmt.Errorf("iterate queue events for summary: %w", err)
	}

	switch {
	case !summary.ConfigValid:
		summary.Alert = true
		summary.AlertReason = "config_invalid"
	case !summary.Reachable:
		summary.Alert = true
		summary.AlertReason = "unreachable"
	case summary.FailedCount > 0:
		summary.Alert = true
		summary.AlertReason = "failed_jobs"
	case summary.PendingCount >= policy.AlertPendingThreshold:
		summary.Alert = true
		summary.AlertReason = "pending_threshold"
	case summary.DeadLetterCount >= policy.AlertDeadLetterThreshold:
		summary.Alert = true
		summary.AlertReason = "dead_letter_threshold"
	case summary.PendingCount > 0 && summary.DeadLetterCount > 0:
		summary.Alert = true
		summary.AlertReason = "pending_after_dead_letter_history"
	}

	return summary, nil
}

func (s *DeliveryQueueService) validateReplayPolicy(ctx context.Context, destination domain.Destination) error {
	policy := effectiveQueueConfig(destination, s.cfg)
	if policy.ReplayCooldownSeconds > 0 {
		var createdAt string
		err := s.db.QueryRowContext(ctx, `
SELECT created_at
FROM queue_job_events
WHERE destination_id = ? AND event_type = 'replay'
ORDER BY id DESC
LIMIT 1`, destination.ID).Scan(&createdAt)
		if err == nil {
			lastReplayAt, _ := time.Parse(time.RFC3339, createdAt)
			cooldownUntil := lastReplayAt.Add(time.Duration(policy.ReplayCooldownSeconds) * time.Second)
			if time.Now().UTC().Before(cooldownUntil) {
				return fmt.Errorf("destination %s replay cooldown active until %s", destination.ID, cooldownUntil.Format(time.RFC3339))
			}
		} else if err != sql.ErrNoRows {
			return fmt.Errorf("check replay cooldown: %w", err)
		}
	}

	windowStart := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	var replayCount int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM queue_job_events
WHERE destination_id = ? AND event_type = 'replay' AND created_at >= ?`, destination.ID, windowStart).Scan(&replayCount); err != nil {
		return fmt.Errorf("count recent replays: %w", err)
	}
	if replayCount >= policy.MaxReplaysPerHour {
		return fmt.Errorf("destination %s exceeded replay limit: %d in last hour", destination.ID, replayCount)
	}
	return nil
}
