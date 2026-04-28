package ingest

import (
	"context"
	"fmt"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest/chatgpt"
	"github.com/hollis-labs/fragments-engine/internal/ingest/claude"
	"github.com/hollis-labs/fragments-engine/internal/ingest/urlsource"
)

func DefaultSources() []Source {
	return []Source{
		claude.Source{},
		chatgpt.Source{},
		urlsource.Source{},
	}
}

func CollectWithDefaultSources(ctx context.Context, ingestCfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	for _, source := range DefaultSources() {
		if source.Kind() == ingestCfg.Kind {
			return source.Collect(ctx, ingestCfg)
		}
	}
	return nil, fmt.Errorf("unsupported ingest kind %q", ingestCfg.Kind)
}
