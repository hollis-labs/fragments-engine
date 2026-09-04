package domain

import "time"

type ReadingStatus string

const (
	ReadingUnread     ReadingStatus = "unread"
	ReadingInProgress ReadingStatus = "in_progress"
	ReadingRead       ReadingStatus = "read"
)

func (s ReadingStatus) Valid() bool {
	return s == ReadingUnread || s == ReadingInProgress || s == ReadingRead
}

type ReadingPositionKind string

const (
	ReadingPositionNone     ReadingPositionKind = "none"
	ReadingPositionArticle  ReadingPositionKind = "article"
	ReadingPositionVideo    ReadingPositionKind = "video"
	ReadingPositionGallery  ReadingPositionKind = "gallery"
	ReadingPositionDocument ReadingPositionKind = "document"
	ReadingPositionAudio    ReadingPositionKind = "audio"
)

func (k ReadingPositionKind) Valid() bool {
	switch k {
	case ReadingPositionNone, ReadingPositionArticle, ReadingPositionVideo,
		ReadingPositionGallery, ReadingPositionDocument, ReadingPositionAudio:
		return true
	default:
		return false
	}
}

type ReadingPosition struct {
	Kind            ReadingPositionKind `json:"kind"`
	Progress        *float64            `json:"progress,omitempty"`
	BlockAnchor     string              `json:"block_anchor,omitempty"`
	LocalOffset     *int                `json:"local_offset,omitempty"`
	ElapsedSeconds  *float64            `json:"elapsed_seconds,omitempty"`
	DurationSeconds *float64            `json:"duration_seconds,omitempty"`
	ProviderMediaID string              `json:"provider_media_id,omitempty"`
	AttachmentID    string              `json:"attachment_id,omitempty"`
	Index           *int                `json:"index,omitempty"`
	Page            *int                `json:"page,omitempty"`
}

type ReadingState struct {
	PrincipalID string          `json:"principal_id"`
	FragmentID  string          `json:"fragment_id"`
	State       ReadingStatus   `json:"state"`
	Position    ReadingPosition `json:"position"`
	LastOpened  *time.Time      `json:"last_opened_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Revision    int             `json:"revision"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type ReaderCommandState string

const (
	ReaderCommandPending   ReaderCommandState = "pending"
	ReaderCommandExecuting ReaderCommandState = "executing"
	ReaderCommandSucceeded ReaderCommandState = "succeeded"
	ReaderCommandFailed    ReaderCommandState = "failed"
	ReaderCommandUncertain ReaderCommandState = "uncertain"
)

func (s ReaderCommandState) Terminal() bool {
	return s == ReaderCommandSucceeded || s == ReaderCommandFailed || s == ReaderCommandUncertain
}

type ReaderCommandReceipt struct {
	CommandID         string             `json:"command_id"`
	IdempotencyKey    string             `json:"idempotency_key"`
	SemanticDigest    string             `json:"semantic_digest"`
	PrincipalID       string             `json:"principal_id"`
	FragmentID        string             `json:"fragment_id"`
	Command           string             `json:"command"`
	State             ReaderCommandState `json:"state"`
	AggregateRevision int                `json:"aggregate_revision"`
	ResultJSON        string             `json:"result_json"`
	ErrorCode         string             `json:"error_code,omitempty"`
	ErrorDetail       string             `json:"error_detail,omitempty"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

type ReaderTagOverlay struct {
	CommandID         string    `json:"command_id"`
	PrincipalID       string    `json:"principal_id"`
	FragmentID        string    `json:"fragment_id"`
	NormalizedValue   string    `json:"normalized_value"`
	DisplayValue      string    `json:"display_value"`
	Suppressed        bool      `json:"suppressed"`
	AggregateRevision int       `json:"aggregate_revision"`
	CreatedAt         time.Time `json:"created_at"`
}

type ReaderCommandEffect struct {
	CommandID   string             `json:"command_id"`
	PrincipalID string             `json:"principal_id"`
	FragmentID  string             `json:"fragment_id"`
	Kind        string             `json:"kind"`
	TargetID    string             `json:"target_id"`
	State       ReaderCommandState `json:"state"`
	ResultJSON  string             `json:"result_json"`
	ErrorDetail string             `json:"error_detail,omitempty"`
	ClaimedAt   *time.Time         `json:"claimed_at,omitempty"`
	CompletedAt *time.Time         `json:"completed_at,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}
