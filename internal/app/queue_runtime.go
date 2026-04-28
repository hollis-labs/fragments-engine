package app

import (
	"context"
	"log"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

func RunQueueDrainer(ctx context.Context, cfgPath string) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("queue drainer config error: %v", err)
		return
	}
	if !cfg.Queue.AutoDrain {
		return
	}

	interval := time.Duration(cfg.Queue.PollIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := DrainQueueOnce(ctx, cfgPath); err != nil && ctx.Err() == nil {
			log.Printf("queue drainer error: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func DrainQueueOnce(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	instance, err := Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	_, err = instance.Queue.Drain(ctx, cfg.Queue.BatchSize)
	return err
}
