package capturecontract

import (
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestCanonicalSchemasCompile(t *testing.T) {
	r, err := getRegistry()
	if err != nil {
		t.Fatalf("compile schemas: %v", err)
	}
	if got, want := len(r.resolved), len(schemaFiles); got != want {
		t.Fatalf("compiled %d schemas, want %d", got, want)
	}
	for _, name := range SupportedSchemas() {
		if _, ok := SchemaID(name); !ok {
			t.Errorf("missing schema ID for %q", name)
		}
	}
}

func TestValidFixtures(t *testing.T) {
	tests := []struct {
		file   string
		schema SchemaName
	}{
		{"valid-capture-youtube.json", SchemaCaptureEnvelope},
		{"valid-reader-video.json", SchemaReaderItem},
		{"valid-reader-non-web.json", SchemaReaderItem},
		{"valid-capture-completion.json", SchemaCaptureCompletion},
		{"valid-reader-command.json", SchemaReaderCommand},
		{"valid-reader-context.json", SchemaReaderContext},
		{"valid-conversation-ref.json", SchemaConversationRef},
		{"valid-api-problem.json", SchemaAPIProblem},
		{"valid-capabilities.json", SchemaCapabilities},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			raw := readFixture(t, test.file)
			if err := ValidateJSON(test.schema, raw); err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
		})
	}
}

func TestInvalidFixturesProveStrictnessAndDiscriminators(t *testing.T) {
	tests := []struct {
		file   string
		schema SchemaName
	}{
		{"invalid-capture-unnamespaced-extension.json", SchemaCaptureEnvelope},
		{"invalid-capture-media-discriminator.json", SchemaCaptureEnvelope},
		{"invalid-browser-capture-missing-urls.json", SchemaCaptureEnvelope},
		{"invalid-reader-command-patch.json", SchemaReaderCommand},
		{"invalid-playback-html.json", SchemaPlayback},
		{"invalid-reading-position-discriminator.json", SchemaReadingState},
		{"invalid-api-problem-missing-errors.json", SchemaAPIProblem},
		{"invalid-reader-context-messages.json", SchemaReaderContext},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			err := ValidateJSON(test.schema, readFixture(t, test.file))
			if err == nil {
				t.Fatal("invalid fixture was accepted")
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error type = %T, want *ValidationError: %v", err, err)
			}
		})
	}
}

func TestTypedEdgeDecodersValidateBeforeBinding(t *testing.T) {
	envelope, err := DecodeCaptureEnvelope(readFixture(t, "valid-capture-youtube.json"))
	if err != nil {
		t.Fatalf("decode valid capture: %v", err)
	}
	if envelope.SchemaVersion != CaptureVersion || envelope.Source.ProviderItemID != "3RmtNXqnreI" || envelope.Source.SourceItemKey != "youtube:3RmtNXqnreI" {
		t.Fatalf("unexpected capture binding: %#v", envelope)
	}

	if _, err := DecodeCaptureEnvelope(readFixture(t, "invalid-capture-unnamespaced-extension.json")); err == nil {
		t.Fatal("typed decoder accepted invalid provider extension")
	}
	command, err := DecodeReaderCommand(readFixture(t, "valid-reader-command.json"))
	if err != nil {
		t.Fatalf("decode valid command: %v", err)
	}
	if command.Command != "set_reading_progress" || command.Position == nil || command.Position.Kind != "gallery" {
		t.Fatalf("unexpected command binding: %#v", command)
	}
	conversation, err := DecodeConversationRef(readFixture(t, "valid-conversation-ref.json"))
	if err != nil {
		t.Fatalf("decode valid conversation ref: %v", err)
	}
	if conversation.PinnedStartingRevisionID != "revision-3" {
		t.Fatalf("conversation is not pinned as expected: %#v", conversation)
	}
}

func TestTypedReaderBindingPreservesNonWebSourceIdentity(t *testing.T) {
	var item ReaderItem
	if err := json.Unmarshal(readFixture(t, "valid-reader-non-web.json"), &item); err != nil {
		t.Fatalf("decode non-web Reader item: %v", err)
	}
	if item.Source.SourceItemKey != "vault-entry-42" || item.Source.SourceLocator != "nil://work-notes/entry-42" {
		t.Fatalf("non-web source identity was not bound: %#v", item.Source)
	}
	if item.Source.SubmittedURL != "" || item.Source.CanonicalURL != "" {
		t.Fatalf("non-web Reader source unexpectedly has URLs: %#v", item.Source)
	}
	assertMarshaledValueValid(t, SchemaReaderItem, item)
}

func TestResponseAndListSchemasComposeReaderItem(t *testing.T) {
	var reader any
	if err := json.Unmarshal(readFixture(t, "valid-reader-video.json"), &reader); err != nil {
		t.Fatalf("decode reader fixture: %v", err)
	}
	response := map[string]any{
		"schema_version":       CaptureResultVersion,
		"capture_id":           "capture-1",
		"fragment_id":          "fragment-1",
		"fragment_revision_id": "revision-1",
		"capture_attempt_id":   "attempt-1",
		"completion":           "transferring",
		"idempotent_replay":    false,
		"asset_instructions": []any{
			map[string]any{
				"client_variant_id": "poster:maxresdefault",
				"asset_variant_id":  "variant-poster",
				"action":            "request_upload",
				"upload_href":       "/v1/captures/capture-1/assets/poster:maxresdefault/content",
			},
		},
		"warnings":    []any{},
		"reader_item": reader,
	}
	assertMarshaledValueValid(t, SchemaCaptureResponse, response)
	assertMarshaledValueValid(t, SchemaReaderList, map[string]any{
		"schema_version": ReaderListVersion,
		"scope":          "inbox",
		"items":          []any{reader},
	})
	assertMarshaledValueValid(t, SchemaCaptureStatus, map[string]any{
		"schema_version":       CaptureStatusVersion,
		"capture_id":           "capture-1",
		"fragment_id":          "fragment-1",
		"fragment_revision_id": "revision-1",
		"capture_attempt_id":   "attempt-1",
		"completion":           "partial",
		"assets": []any{
			map[string]any{"client_variant_id": "poster:maxresdefault", "outcome": "already_available"},
			map[string]any{"client_variant_id": "video:reference", "outcome": "not_available", "reason": "reference-only"},
		},
		"enrichment_in_progress": true,
		"warnings":               []any{},
	})
}

func TestDefaultCapabilitiesAreSchemaValidAndNotPrematurelyReady(t *testing.T) {
	discovery := DefaultCapabilities("test-version")
	assertMarshaledValueValid(t, SchemaCapabilities, discovery)
	if discovery.Operations.CaptureManifest || discovery.Operations.ReaderQuery {
		t.Fatalf("later-wave operations advertised as ready: %#v", discovery.Operations)
	}
	if len(discovery.Contracts) != 11 {
		t.Fatalf("contracts = %d, want 11", len(discovery.Contracts))
	}
}

func TestEveryReadingPositionVariant(t *testing.T) {
	positions := []map[string]any{
		{"kind": "none"},
		{"kind": "article", "progress": 0.7, "block_anchor": "paragraph-12", "local_offset": 9},
		{"kind": "video", "elapsed_seconds": 12.5, "duration_seconds": 180, "provider_media_id": "video-1"},
		{"kind": "gallery", "attachment_id": "attachment-2", "index": 1},
		{"kind": "document", "page": 4, "progress": 0.25},
		{"kind": "audio", "elapsed_seconds": 30, "duration_seconds": 240},
	}
	for _, position := range positions {
		t.Run(position["kind"].(string), func(t *testing.T) {
			assertMarshaledValueValid(t, SchemaReadingState, map[string]any{
				"principal_id": "principal-1",
				"fragment_id":  "fragment-1",
				"state":        "in_progress",
				"position":     position,
				"revision":     1,
			})
		})
	}
}

func TestEveryPlaybackVariant(t *testing.T) {
	playback := []map[string]any{
		{"kind": "provider_embed", "provider": "youtube", "provider_item_id": "video-1", "start_seconds": 12},
		{"kind": "blob_stream", "asset_variant_id": "variant-1", "mime_type": "video/mp4"},
		{"kind": "external_stream", "url": "https://media.example.test/video.m3u8", "mime_type": "application/vnd.apple.mpegurl", "policy": "signed_source"},
	}
	for _, value := range playback {
		t.Run(value["kind"].(string), func(t *testing.T) {
			assertMarshaledValueValid(t, SchemaPlayback, value)
		})
	}
}

func TestEverySemanticCommandVariant(t *testing.T) {
	tests := []struct {
		command string
		fields  map[string]any
	}{
		{"add_tag", map[string]any{"tag": "research"}},
		{"remove_tag", map[string]any{"tag": "research"}},
		{"append_capture_note", map[string]any{"annotation_id": "annotation-1", "text": "Follow up"}},
		{"update_curated_note", map[string]any{"expected_note_revision": 2, "body_markdown": "Curated"}},
		{"set_reading_progress", map[string]any{"position": map[string]any{"kind": "article", "progress": 0.7}}},
		{"mark_read", nil},
		{"mark_unread", nil},
		{"request_asset_acquisition", map[string]any{"media_asset_id": "asset-1", "variant_kind": "original", "requested_custody": "mirror"}},
		{"route", map[string]any{"route_id": "route-1"}},
		{"materialize", map[string]any{"destination_id": "destination-1"}},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			command := map[string]any{
				"schema_version":    ReaderCommandVersion,
				"command":           test.command,
				"command_id":        "command-1",
				"idempotency_key":   "command-1",
				"expected_revision": 2,
			}
			for key, value := range test.fields {
				command[key] = value
			}
			assertMarshaledValueValid(t, SchemaReaderCommand, command)
		})
	}
}

func TestContractManifestReferencesEmbeddedArtifacts(t *testing.T) {
	raw, err := artifacts.ReadFile("contract-manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		OpenAPI string            `json:"openapi"`
		Schemas map[string]string `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	paths := []string{manifest.OpenAPI}
	for _, filename := range manifest.Schemas {
		paths = append(paths, filename)
	}
	for _, filename := range paths {
		if _, err := fs.Stat(artifacts, filename); err != nil {
			t.Errorf("manifest artifact %q is missing: %v", filename, err)
		}
	}
}

func TestOpenAPI31ReferencesPublishedSchemas(t *testing.T) {
	raw, err := OpenAPI()
	if err != nil {
		t.Fatalf("read OpenAPI: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	if got := document["openapi"]; got != "3.1.0" {
		t.Fatalf("openapi = %v, want 3.1.0", got)
	}
	if got := document["jsonSchemaDialect"]; got != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("jsonSchemaDialect = %v", got)
	}
	refs := collectRefs(document)
	if len(refs) == 0 {
		t.Fatal("OpenAPI document has no schema references")
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "./") {
			continue
		}
		filename := strings.TrimPrefix(strings.SplitN(ref, "#", 2)[0], "./")
		if _, err := fs.Stat(artifacts, filename); err != nil {
			t.Errorf("unresolved OpenAPI reference %q: %v", ref, err)
		}
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI paths are missing")
	}
	for _, required := range []string{
		"/v1/capabilities",
		"/v1/captures",
		"/v1/captures/{captureId}",
		"/v1/captures/{captureId}/assets/{clientVariantId}/content",
		"/v1/captures/{captureId}/complete",
		"/v1/reader/items",
		"/v1/reader/items/{fragmentId}",
		"/v1/reader/items/{fragmentId}/commands",
		"/v1/reader/items/{fragmentId}/context",
		"/v1/reader/items/{fragmentId}/conversations",
	} {
		if _, exists := paths[required]; !exists {
			t.Errorf("OpenAPI path %q is missing", required)
		}
	}
	assertRawBinaryAssetBodies(t, paths)
}

func assertRawBinaryAssetBodies(t *testing.T, paths map[string]any) {
	t.Helper()
	uploadPath := paths["/v1/captures/{captureId}/assets/{clientVariantId}/content"].(map[string]any)
	put := uploadPath["put"].(map[string]any)
	requestBody := put["requestBody"].(map[string]any)
	content := requestBody["content"].(map[string]any)
	if len(content) == 0 {
		t.Fatal("asset upload has no media types")
	}
	for mediaType, rawMedia := range content {
		media := rawMedia.(map[string]any)
		schema := media["schema"].(map[string]any)
		if got := schema["type"]; got != "string" {
			t.Errorf("%s upload type = %v, want string", mediaType, got)
		}
		if got := schema["format"]; got != "binary" {
			t.Errorf("%s upload format = %v, want binary", mediaType, got)
		}
		if _, encoded := schema["contentEncoding"]; encoded {
			t.Errorf("%s upload incorrectly describes base64-encoded text", mediaType)
		}
	}
}

func readFixture(t *testing.T, filename string) []byte {
	t.Helper()
	raw, err := artifacts.ReadFile("fixtures/" + filename)
	if err != nil {
		t.Fatalf("read fixture %s: %v", filename, err)
	}
	return raw
}

func assertMarshaledValueValid(t *testing.T, schema SchemaName, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture value: %v", err)
	}
	if err := ValidateJSON(schema, raw); err != nil {
		t.Fatalf("value does not satisfy %s: %v\n%s", schema, err, raw)
	}
}

func collectRefs(value any) []string {
	var refs []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "$ref" {
					if ref, ok := child.(string); ok {
						refs = append(refs, ref)
					}
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return refs
}
