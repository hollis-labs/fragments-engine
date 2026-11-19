package app

import (
	"context"
	"log"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

// RunMediaServeReconcile checks, once at server start, the stored media
// variants that predate the serve check, so the Reader stops listing as
// available a file the media route refuses. It is a no-op once every stored
// variant has been checked.
func RunMediaServeReconcile(ctx context.Context, cfgPath string) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("media serve check config error: %v", err)
		return
	}
	instance, err := Open(ctx, cfg)
	if err != nil {
		log.Printf("media serve check: %v", err)
		return
	}
	defer instance.Close()
	checked, unserved, err := instance.Media.ReconcileServeChecks(ctx)
	if err != nil && ctx.Err() == nil {
		log.Printf("media serve check: stopped after %d variants: %v", checked, err)
		return
	}
	if checked > 0 {
		log.Printf("media serve check: checked %d stored variants, %d will not be served", checked, unserved)
	}
}
