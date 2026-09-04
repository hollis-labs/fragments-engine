package capturecontract

// DefaultCapabilities reports the versioned contract surface published by this
// package. Runtime operation flags deliberately remain false until the service
// tasks that implement those operations replace them with configured readiness.
func DefaultCapabilities(serverVersion string) CapabilityDiscovery {
	if serverVersion == "" {
		serverVersion = "development"
	}
	return CapabilityDiscovery{
		SchemaVersion: CapabilitiesVersion,
		ServerVersion: serverVersion,
		Contracts: []ContractCapability{
			contractCapability("capture", CaptureVersion, SchemaCaptureEnvelope),
			contractCapability("capture_result", CaptureResultVersion, SchemaCaptureResponse),
			contractCapability("capture_completion", CaptureCompletionVersion, SchemaCaptureCompletion),
			contractCapability("capture_status", CaptureStatusVersion, SchemaCaptureStatus),
			contractCapability("reader_item", ReaderItemVersion, SchemaReaderItem),
			contractCapability("reader_list", ReaderListVersion, SchemaReaderList),
			contractCapability("reader_command", ReaderCommandVersion, SchemaReaderCommand),
			contractCapability("reader_context", ReaderContextVersion, SchemaReaderContext),
			contractCapability("conversation_ref", ConversationRefVersion, SchemaConversationRef),
			contractCapability("api_problem", "rfc9457+fe.v1", SchemaAPIProblem),
			contractCapability("capabilities", CapabilitiesVersion, SchemaCapabilities),
		},
		Operations: OperationReadiness{},
		Capture: CaptureCapabilities{
			MediaKinds:          []string{"image", "video", "audio", "document", "timed_text", "other"},
			VariantKinds:        []string{"original", "preview", "thumbnail", "poster", "audio", "subtitles", "transcript"},
			CustodyModes:        []string{"reference", "cache", "mirror", "adopted"},
			TransferPreferences: []string{"browser_preferred", "server_preferred", "reference_only"},
		},
		Reader: ReaderCapabilities{
			SchemaVersions: []string{ReaderItemVersion, ReaderListVersion, ReaderCommandVersion, ReaderContextVersion, ConversationRefVersion},
			Scopes:         []string{"inbox", "library", "all"},
			Renderers:      []string{"article", "image", "gallery", "video", "audio", "document", "text", "unknown"},
			PlaybackKinds:  []string{"provider_embed", "blob_stream", "external_stream"},
			Commands: []string{
				"add_tag",
				"remove_tag",
				"append_capture_note",
				"update_curated_note",
				"set_reading_progress",
				"mark_read",
				"mark_unread",
				"request_asset_acquisition",
				"route",
				"materialize",
			},
		},
		Providers:     []ProviderCapability{},
		OptionalTools: []OptionalToolCapability{},
	}
}

func contractCapability(name, version string, schema SchemaName) ContractCapability {
	id, _ := SchemaID(schema)
	return ContractCapability{
		Name:             name,
		AcceptedVersions: []string{version},
		PreferredVersion: version,
		SchemaID:         id,
	}
}
