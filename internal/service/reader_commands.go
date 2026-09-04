package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

const LocalReaderPrincipal = "local-user"

const (
	maxReaderAnnotationText = 64 << 10
	maxReaderCuratedNote    = 512 << 10
)

// ReaderProjector is the only integration seam with the parallel Reader query
// task. Command persistence does not depend on its repository or SQL shape.
type ReaderProjector interface {
	ProjectReaderItem(context.Context, string, string) (capturecontract.ReaderItem, error)
}

type ReaderEffectExecutor interface {
	RouteFragment(context.Context, string, string) (domain.RouteApplyItem, error)
	MaterializeFragmentToDestinationID(context.Context, string, string) (domain.FragmentMaterializeResult, error)
}

type ReaderCommandExecution struct {
	Item             capturecontract.ReaderItem
	Receipt          domain.ReaderCommandReceipt
	CanonicalID      string
	IdempotentReplay bool
}

type ReaderCommandService struct {
	repository  *repository.ReaderCommandRepository
	acquisition *AssetAcquisitionService
	effects     ReaderEffectExecutor
	projector   ReaderProjector
	now         func() time.Time
}

func NewReaderCommandService(repo *repository.ReaderCommandRepository, acquisition *AssetAcquisitionService, effects ReaderEffectExecutor, projector ReaderProjector) *ReaderCommandService {
	return &ReaderCommandService{repository: repo, acquisition: acquisition, effects: effects, projector: projector, now: time.Now}
}

type InvalidReaderCommandError struct{ Err error }

func (e *InvalidReaderCommandError) Error() string { return "invalid reader command: " + e.Err.Error() }
func (e *InvalidReaderCommandError) Unwrap() error { return e.Err }

type ReaderCommandExecutionError struct {
	Code   string
	Detail string
}

func (e *ReaderCommandExecutionError) Error() string { return e.Detail }

var ErrReaderProjectorUnavailable = errors.New("reader projector is unavailable")

func IsInvalidReaderCommand(err error) bool {
	var target *InvalidReaderCommandError
	return errors.As(err, &target)
}

func (s *ReaderCommandService) Execute(ctx context.Context, principalID, fragmentID string, command capturecontract.ReaderCommand) (ReaderCommandExecution, error) {
	if s == nil || s.repository == nil {
		return ReaderCommandExecution{}, fmt.Errorf("execute reader command: repository is required")
	}
	if s.projector == nil {
		return ReaderCommandExecution{}, ErrReaderProjectorUnavailable
	}
	principalID = strings.TrimSpace(principalID)
	fragmentID = strings.TrimSpace(fragmentID)
	if principalID == "" || fragmentID == "" {
		return ReaderCommandExecution{}, invalidReaderCommandf("principal and fragment are required")
	}
	canonicalID, err := s.repository.ResolveCanonicalFragmentID(ctx, fragmentID)
	if err != nil {
		return ReaderCommandExecution{}, err
	}
	write, err := normalizeReaderCommand(principalID, canonicalID, command, s.now().UTC())
	if err != nil {
		return ReaderCommandExecution{}, err
	}

	var persisted repository.ReaderCommandResult
	switch command.Command {
	case "add_tag", "remove_tag", "append_capture_note", "update_curated_note",
		"set_reading_progress", "mark_read", "mark_unread":
		persisted, err = s.repository.ApplyLocal(ctx, write)
	case "request_asset_acquisition":
		persisted, err = s.executeAssetAcquisition(ctx, write, command)
	case "route", "materialize":
		persisted, err = s.executeEffect(ctx, write)
	default:
		return ReaderCommandExecution{}, invalidReaderCommandf("unsupported discriminator %q", command.Command)
	}
	if err != nil {
		return ReaderCommandExecution{}, err
	}
	item, err := s.projector.ProjectReaderItem(ctx, principalID, persisted.CanonicalID)
	if err != nil {
		return ReaderCommandExecution{}, fmt.Errorf("project reader command result: %w", err)
	}
	return ReaderCommandExecution{Item: item, Receipt: persisted.Receipt,
		CanonicalID: persisted.CanonicalID, IdempotentReplay: persisted.IdempotentReplay}, nil
}

func (s *ReaderCommandService) executeAssetAcquisition(ctx context.Context, write repository.ReaderCommandWrite, command capturecontract.ReaderCommand) (repository.ReaderCommandResult, error) {
	if s.acquisition == nil {
		return repository.ReaderCommandResult{}, fmt.Errorf("execute reader asset acquisition: service is required")
	}
	persisted, err := s.repository.ReserveAssetCommand(ctx, write)
	if err != nil {
		return repository.ReaderCommandResult{}, err
	}
	if persisted.Receipt.State.Terminal() {
		if persisted.Receipt.State != domain.ReaderCommandSucceeded {
			return repository.ReaderCommandResult{}, &ReaderCommandExecutionError{Code: persisted.Receipt.ErrorCode, Detail: persisted.Receipt.ErrorDetail}
		}
		return persisted, nil
	}
	result, requestErr := s.acquisition.Request(ctx, AssetAcquisitionCommand{
		CommandID: write.CommandID, IdempotencyKey: write.IdempotencyKey,
		MediaAssetID: write.TargetID, VariantKind: domain.AssetVariantKind(command.VariantKind),
		RequestedCustody: domain.CustodyMode(command.RequestedCustody), RequestedBy: write.PrincipalID,
		Reason: "reader command for fragment " + persisted.CanonicalID,
	})
	state := domain.ReaderCommandSucceeded
	resultJSON := `{}`
	errorCode, errorDetail := "", ""
	if requestErr != nil {
		state = domain.ReaderCommandFailed
		errorCode, errorDetail = "asset_acquisition_failed", requestErr.Error()
		var conflict *repository.MediaConflictError
		if errors.As(requestErr, &conflict) {
			errorCode = "conflict"
		}
	} else if raw, marshalErr := json.Marshal(result); marshalErr != nil {
		return repository.ReaderCommandResult{}, marshalErr
	} else {
		resultJSON = string(raw)
	}
	receipt, finalizeErr := s.repository.FinalizeExternal(ctx, write.CommandID, state, resultJSON, errorCode, errorDetail, s.now().UTC())
	if finalizeErr != nil {
		return repository.ReaderCommandResult{}, finalizeErr
	}
	persisted.Receipt = receipt
	persisted.IdempotentReplay = persisted.IdempotentReplay || result.IdempotentReplay
	if requestErr != nil {
		return repository.ReaderCommandResult{}, &ReaderCommandExecutionError{Code: errorCode, Detail: errorDetail}
	}
	return persisted, nil
}

func (s *ReaderCommandService) executeEffect(ctx context.Context, write repository.ReaderCommandWrite) (repository.ReaderCommandResult, error) {
	if s.effects == nil {
		return repository.ReaderCommandResult{}, fmt.Errorf("execute reader effect: executor is required")
	}
	persisted, err := s.repository.ReserveEffect(ctx, write)
	if err != nil {
		return repository.ReaderCommandResult{}, err
	}
	if persisted.Receipt.State.Terminal() || persisted.Receipt.State == domain.ReaderCommandExecuting {
		return persisted, nil
	}
	effect, claimed, err := s.repository.ClaimEffect(ctx, write.CommandID, s.now().UTC())
	if err != nil {
		return repository.ReaderCommandResult{}, err
	}
	if !claimed {
		persisted.Receipt.State = effect.State
		return persisted, nil
	}
	var result any
	var effectErr error
	switch write.Command {
	case "route":
		result, effectErr = s.effects.RouteFragment(ctx, persisted.CanonicalID, write.TargetID)
	case "materialize":
		result, effectErr = s.effects.MaterializeFragmentToDestinationID(ctx, persisted.CanonicalID, write.TargetID)
	}
	state := domain.ReaderCommandSucceeded
	errorCode, errorDetail := "", ""
	if effectErr != nil {
		state = domain.ReaderCommandFailed
		errorCode, errorDetail = "effect_failed", effectErr.Error()
		if errors.Is(effectErr, context.Canceled) || errors.Is(effectErr, context.DeadlineExceeded) {
			state = domain.ReaderCommandUncertain
			errorCode = "effect_uncertain"
		}
	}
	resultJSON := `{}`
	if result != nil {
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return repository.ReaderCommandResult{}, marshalErr
		}
		resultJSON = string(raw)
	}
	receipt, err := s.repository.FinalizeExternal(context.WithoutCancel(ctx), write.CommandID,
		state, resultJSON, errorCode, errorDetail, s.now().UTC())
	if err != nil {
		return repository.ReaderCommandResult{}, err
	}
	persisted.Receipt = receipt
	return persisted, nil
}

func normalizeReaderCommand(principalID, canonicalID string, command capturecontract.ReaderCommand, now time.Time) (repository.ReaderCommandWrite, error) {
	command.CommandID = strings.TrimSpace(command.CommandID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.Command = strings.TrimSpace(command.Command)
	if command.SchemaVersion != capturecontract.ReaderCommandVersion || command.CommandID == "" || command.IdempotencyKey == "" || command.ExpectedRevision < 0 {
		return repository.ReaderCommandWrite{}, invalidReaderCommandf("schema version, command identity, idempotency key, and non-negative expected revision are required")
	}
	write := repository.ReaderCommandWrite{CommandID: command.CommandID,
		IdempotencyKey: command.IdempotencyKey, PrincipalID: principalID,
		FragmentID: canonicalID, Command: command.Command, ExpectedRevision: command.ExpectedRevision,
		CreatedAt: now.UTC()}

	payload := map[string]any{"expected_revision": command.ExpectedRevision}
	switch command.Command {
	case "add_tag", "remove_tag":
		write.Tag = strings.TrimSpace(command.Tag)
		write.NormalizedTag = strings.ToLower(write.Tag)
		if write.Tag == "" || utf8.RuneCountInString(write.Tag) > 128 {
			return repository.ReaderCommandWrite{}, invalidReaderCommandf("tag must contain 1 to 128 characters")
		}
		payload["tag"] = write.NormalizedTag
	case "append_capture_note":
		write.AnnotationID = strings.TrimSpace(command.AnnotationID)
		write.AnnotationText = strings.TrimSpace(command.Text)
		if write.AnnotationID == "" || write.AnnotationText == "" || len(write.AnnotationText) > maxReaderAnnotationText {
			return repository.ReaderCommandWrite{}, invalidReaderCommandf("annotation ID and bounded non-empty text are required")
		}
		if command.Selector != nil {
			selector := domain.TextQuoteSelector{Exact: strings.TrimSpace(command.Selector.Exact), Prefix: command.Selector.Prefix, Suffix: command.Selector.Suffix}
			if selector.Exact == "" {
				return repository.ReaderCommandWrite{}, invalidReaderCommandf("annotation selector exact text is required")
			}
			write.AnnotationSelector = &selector
		}
		payload["annotation_id"], payload["text"], payload["selector"] = write.AnnotationID, write.AnnotationText, write.AnnotationSelector
	case "update_curated_note":
		if command.ExpectedNoteRevision == nil || *command.ExpectedNoteRevision < 0 || len(command.BodyMarkdown) > maxReaderCuratedNote {
			return repository.ReaderCommandWrite{}, invalidReaderCommandf("non-negative expected note revision and bounded body are required")
		}
		write.ExpectedNote = *command.ExpectedNoteRevision
		write.BodyMarkdown = command.BodyMarkdown
		payload["expected_note_revision"], payload["body_markdown"] = write.ExpectedNote, write.BodyMarkdown
	case "set_reading_progress":
		if command.Position == nil {
			return repository.ReaderCommandWrite{}, invalidReaderCommandf("reading position is required")
		}
		write.Position = contractReadingPosition(*command.Position)
		if err := validateReadingPosition(write.Position); err != nil {
			return repository.ReaderCommandWrite{}, err
		}
		payload["position"] = write.Position
	case "mark_read", "mark_unread":
	case "request_asset_acquisition":
		write.TargetID = strings.TrimSpace(command.MediaAssetID)
		if write.TargetID == "" || !domain.AssetVariantKind(command.VariantKind).Valid() {
			return repository.ReaderCommandWrite{}, invalidReaderCommandf("owned media asset and valid variant kind are required")
		}
		custody := domain.CustodyMode(command.RequestedCustody)
		if custody != domain.CustodyCache && custody != domain.CustodyMirror && custody != domain.CustodyAdopted {
			return repository.ReaderCommandWrite{}, invalidReaderCommandf("custody must be cache, mirror, or adopted")
		}
		payload["media_asset_id"], payload["variant_kind"], payload["requested_custody"] = write.TargetID, command.VariantKind, command.RequestedCustody
	case "route":
		write.TargetID = strings.TrimSpace(command.RouteID)
		if write.TargetID == "" {
			return repository.ReaderCommandWrite{}, invalidReaderCommandf("route ID is required")
		}
		payload["route_id"] = write.TargetID
	case "materialize":
		write.TargetID = strings.TrimSpace(command.DestinationID)
		if write.TargetID == "" {
			return repository.ReaderCommandWrite{}, invalidReaderCommandf("destination ID is required")
		}
		payload["destination_id"] = write.TargetID
	default:
		return repository.ReaderCommandWrite{}, invalidReaderCommandf("unsupported discriminator %q", command.Command)
	}
	semantic := struct {
		PrincipalID string         `json:"principal_id"`
		FragmentID  string         `json:"fragment_id"`
		Command     string         `json:"command"`
		Payload     map[string]any `json:"payload"`
	}{principalID, canonicalID, command.Command, payload}
	raw, err := json.Marshal(semantic)
	if err != nil {
		return repository.ReaderCommandWrite{}, fmt.Errorf("encode reader command semantics: %w", err)
	}
	write.SemanticDigest = domain.DigestText(string(raw))
	return write, nil
}

func contractReadingPosition(value capturecontract.ReadingPosition) domain.ReadingPosition {
	return domain.ReadingPosition{Kind: domain.ReadingPositionKind(value.Kind), Progress: value.Progress,
		BlockAnchor: strings.TrimSpace(value.BlockAnchor), LocalOffset: value.LocalOffset,
		ElapsedSeconds: value.ElapsedSeconds, DurationSeconds: value.DurationSeconds,
		ProviderMediaID: strings.TrimSpace(value.ProviderMediaID), AttachmentID: strings.TrimSpace(value.AttachmentID),
		Index: value.Index, Page: value.Page}
}

func validateReadingPosition(p domain.ReadingPosition) error {
	invalid := func(message string) error { return invalidReaderCommandf("%s position %s", p.Kind, message) }
	if !p.Kind.Valid() {
		return invalid("has an invalid discriminator")
	}
	noProgress := p.Progress == nil && p.BlockAnchor == "" && p.LocalOffset == nil
	noTimed := p.ElapsedSeconds == nil && p.DurationSeconds == nil && p.ProviderMediaID == ""
	noGallery := p.AttachmentID == "" && p.Index == nil
	noDocument := p.Page == nil
	switch p.Kind {
	case domain.ReadingPositionNone:
		if !noProgress || !noTimed || !noGallery || !noDocument {
			return invalid("contains fields from another discriminator")
		}
	case domain.ReadingPositionArticle:
		if p.Progress == nil || !finiteRange(*p.Progress, 0, 1) || (p.LocalOffset != nil && *p.LocalOffset < 0) || !noTimed || !noGallery || !noDocument {
			return invalid("must contain only progress and optional article anchor fields")
		}
	case domain.ReadingPositionVideo:
		if p.ElapsedSeconds == nil || !finiteNonNegative(*p.ElapsedSeconds) || (p.DurationSeconds != nil && (!finitePositive(*p.DurationSeconds) || *p.ElapsedSeconds > *p.DurationSeconds)) || !noProgress || !noGallery || !noDocument {
			return invalid("must contain only valid elapsed/duration/provider fields")
		}
	case domain.ReadingPositionGallery:
		if p.AttachmentID == "" || p.Index == nil || *p.Index < 0 || !noProgress || !noTimed || !noDocument {
			return invalid("must contain only an attachment ID and non-negative index")
		}
	case domain.ReadingPositionDocument:
		if p.Page == nil || *p.Page < 1 || (p.Progress != nil && !finiteRange(*p.Progress, 0, 1)) || p.BlockAnchor != "" || p.LocalOffset != nil || !noTimed || !noGallery {
			return invalid("must contain only a positive page and optional progress")
		}
	case domain.ReadingPositionAudio:
		if p.ElapsedSeconds == nil || !finiteNonNegative(*p.ElapsedSeconds) || (p.DurationSeconds != nil && (!finitePositive(*p.DurationSeconds) || *p.ElapsedSeconds > *p.DurationSeconds)) || !noProgress || p.ProviderMediaID != "" || !noGallery || !noDocument {
			return invalid("must contain only valid elapsed and duration fields")
		}
	}
	return nil
}

func finiteRange(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func finiteNonNegative(value float64) bool { return finiteRange(value, 0, math.MaxFloat64) }
func finitePositive(value float64) bool {
	return finiteRange(value, math.SmallestNonzeroFloat64, math.MaxFloat64)
}

func invalidReaderCommandf(format string, args ...any) error {
	return &InvalidReaderCommandError{Err: fmt.Errorf(format, args...)}
}

func IsReaderNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
