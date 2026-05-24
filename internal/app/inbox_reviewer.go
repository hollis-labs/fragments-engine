package app

import (
	"context"
	"log"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func RunInboxReviewer(ctx context.Context, cfgPath string) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("inbox reviewer config error: %v", err)
		return
	}
	if !cfg.Reviewer.Enabled {
		return
	}
	interval := time.Duration(cfg.Reviewer.PollIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ReviewInboxOnce(ctx, cfgPath); err != nil && ctx.Err() == nil {
			log.Printf("inbox reviewer error: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func ReviewInboxOnce(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	instance, err := Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	_, err = instance.InboxReviewer.ReviewOnce(ctx, cfg.Reviewer.BatchSize)
	return err
}
