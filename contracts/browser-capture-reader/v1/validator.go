package capturecontract

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

const schemaBaseURL = "https://schemas.hollis-labs.dev/fragments-engine/browser-capture-reader/v1/"

type SchemaName string

const (
	SchemaCaptureEnvelope   SchemaName = "capture-envelope"
	SchemaCaptureResponse   SchemaName = "capture-response"
	SchemaCaptureCompletion SchemaName = "capture-completion"
	SchemaCaptureStatus     SchemaName = "capture-status"
	SchemaPlayback          SchemaName = "playback"
	SchemaReadingState      SchemaName = "reading-state"
	SchemaReaderItem        SchemaName = "reader-item"
	SchemaReaderList        SchemaName = "reader-list"
	SchemaReaderCommand     SchemaName = "reader-command"
	SchemaReaderContext     SchemaName = "reader-context"
	SchemaConversationRef   SchemaName = "conversation-ref"
	SchemaAPIProblem        SchemaName = "error"
	SchemaCapabilities      SchemaName = "capabilities"
)

var schemaFiles = map[SchemaName]string{
	SchemaCaptureEnvelope:   "capture-envelope.schema.json",
	SchemaCaptureResponse:   "capture-response.schema.json",
	SchemaCaptureCompletion: "capture-completion.schema.json",
	SchemaCaptureStatus:     "capture-status.schema.json",
	SchemaPlayback:          "playback.schema.json",
	SchemaReadingState:      "reading-state.schema.json",
	SchemaReaderItem:        "reader-item.schema.json",
	SchemaReaderList:        "reader-list.schema.json",
	SchemaReaderCommand:     "reader-command.schema.json",
	SchemaReaderContext:     "reader-context.schema.json",
	SchemaConversationRef:   "conversation-ref.schema.json",
	SchemaAPIProblem:        "error.schema.json",
	SchemaCapabilities:      "capabilities.schema.json",
}

//go:embed contract-manifest.json openapi.json schema/*.schema.json fixtures/*.json
var artifacts embed.FS

// Artifacts returns the exact OpenAPI, JSON Schema, and fixture files embedded
// into the Go binary. Consumers should pin the same files by FE commit.
func Artifacts() fs.FS {
	return artifacts
}

// OpenAPI returns the canonical OpenAPI 3.1 document.
func OpenAPI() ([]byte, error) {
	return artifacts.ReadFile("openapi.json")
}

// SchemaID returns the stable absolute $id for a supported validation root.
func SchemaID(name SchemaName) (string, bool) {
	filename, ok := schemaFiles[name]
	if !ok {
		return "", false
	}
	return schemaBaseURL + filename, true
}

// SupportedSchemas lists the validation roots in deterministic order.
func SupportedSchemas() []SchemaName {
	names := make([]SchemaName, 0, len(schemaFiles))
	for name := range schemaFiles {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

// ValidationError identifies the failed public schema while retaining the
// validator's detailed instance location and constraint information.
type ValidationError struct {
	Schema SchemaName
	Err    error
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s contract validation failed: %v", e.Schema, e.Err)
}

func (e *ValidationError) Unwrap() error { return e.Err }

type schemaRegistry struct {
	resolved map[SchemaName]*jsonschema.Resolved
}

var (
	registryOnce sync.Once
	registry     *schemaRegistry
	registryErr  error
)

func getRegistry() (*schemaRegistry, error) {
	registryOnce.Do(func() {
		registry, registryErr = compileSchemas()
	})
	return registry, registryErr
}

func compileSchemas() (*schemaRegistry, error) {
	r := &schemaRegistry{resolved: make(map[SchemaName]*jsonschema.Resolved, len(schemaFiles))}
	for name, filename := range schemaFiles {
		schema, err := loadEmbeddedSchema(filename)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		resolved, err := schema.Resolve(&jsonschema.ResolveOptions{
			BaseURI: schemaBaseURL + filename,
			Loader:  schemaLoader,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", name, err)
		}
		r.resolved[name] = resolved
	}
	return r, nil
}

func schemaLoader(uri *url.URL) (*jsonschema.Schema, error) {
	if uri.Scheme != "https" || uri.Host != "schemas.hollis-labs.dev" {
		return nil, fmt.Errorf("external schema reference is not allowed: %s", uri)
	}
	const prefix = "/fragments-engine/browser-capture-reader/v1/"
	if !strings.HasPrefix(uri.Path, prefix) {
		return nil, fmt.Errorf("schema reference is outside contract v1: %s", uri)
	}
	filename := path.Base(uri.Path)
	if filename == "." || filename == "/" || !strings.HasSuffix(filename, ".schema.json") {
		return nil, fmt.Errorf("invalid schema reference: %s", uri)
	}
	return loadEmbeddedSchema(filename)
}

func loadEmbeddedSchema(filename string) (*jsonschema.Schema, error) {
	raw, err := artifacts.ReadFile("schema/" + filename)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return &schema, nil
}

// ValidateJSON validates one complete JSON document against a canonical schema.
// Unknown schema names, malformed JSON, and trailing values are rejected.
func ValidateJSON(name SchemaName, raw []byte) error {
	r, err := getRegistry()
	if err != nil {
		return err
	}
	resolved, ok := r.resolved[name]
	if !ok {
		return fmt.Errorf("unknown contract schema %q", name)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var instance any
	if err := decoder.Decode(&instance); err != nil {
		return &ValidationError{Schema: name, Err: err}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return &ValidationError{Schema: name, Err: err}
	}
	if err := resolved.Validate(instance); err != nil {
		return &ValidationError{Schema: name, Err: err}
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values are not allowed")
	}
	return err
}

// DecodeCaptureEnvelope validates the language-neutral contract before binding
// it to the Go edge type.
func DecodeCaptureEnvelope(raw []byte) (CaptureEnvelope, error) {
	var value CaptureEnvelope
	if err := validateAndDecode(SchemaCaptureEnvelope, raw, &value); err != nil {
		return CaptureEnvelope{}, err
	}
	return value, nil
}

// DecodeCaptureCompletion validates a capture completion report before binding.
func DecodeCaptureCompletion(raw []byte) (CaptureCompletionRequest, error) {
	var value CaptureCompletionRequest
	if err := validateAndDecode(SchemaCaptureCompletion, raw, &value); err != nil {
		return CaptureCompletionRequest{}, err
	}
	return value, nil
}

// DecodeReaderCommand validates the discriminator and command-specific fields
// before binding the closed command union to its Go edge type.
func DecodeReaderCommand(raw []byte) (ReaderCommand, error) {
	var value ReaderCommand
	if err := validateAndDecode(SchemaReaderCommand, raw, &value); err != nil {
		return ReaderCommand{}, err
	}
	return value, nil
}

// DecodeConversationRef validates that FE is retaining only an external
// conversation reference, never message or session state.
func DecodeConversationRef(raw []byte) (ConversationRef, error) {
	var value ConversationRef
	if err := validateAndDecode(SchemaConversationRef, raw, &value); err != nil {
		return ConversationRef{}, err
	}
	return value, nil
}

func validateAndDecode(name SchemaName, raw []byte, dst any) error {
	if err := ValidateJSON(name, raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return &ValidationError{Schema: name, Err: err}
	}
	return nil
}
