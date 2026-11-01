package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	inboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/inbound/kafka"
	outboundkafka "github.com/claudioed/inbound-receiving/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/inbound-receiving/internal/adapters/outbound/outbox"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// worker is a started background loop, the channel closed once it returned
// and the function that asks it to stop.
type worker struct {
	name string
	done chan struct{}
	stop func()
}

// startWorker runs run in a goroutine with a cancellable context. closeFn
// (reader/sink close) runs after the context is cancelled.
func startWorker(logger *slog.Logger, name string, run func(ctx context.Context) error, closeFn func()) *worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{name: name, done: make(chan struct{})}
	w.stop = func() {
		cancel()
		closeFn()
	}
	go func() {
		defer close(w.done)
		if err := run(ctx); !errors.Is(err, context.Canceled) {
			logger.Error(name+" stopped", "error", err)
		}
	}()
	return w
}

// startConsumers starts the three local-copy consumers; they are always on. A
// reader dials lazily inside Run, so a broker outage never blocks boot.
func startConsumers(ad adapters, cfg config, logger *slog.Logger) []*worker {
	apply := &usecases.ApplyProductRegistered{UoW: ad.uow, ProcessedEvents: ad.processed, Skus: ad.skus}
	pc := inboundkafka.NewProductConsumer(cfg.kafkaBrokers, cfg.productGroup, apply, logger)
	logger.Info("product consumer running", "topic", inboundkafka.ProductTopic, "group_id", cfg.productGroup, "brokers", cfg.kafkaBrokers)

	reg := &usecases.ApplyLocationSlotRegistered{UoW: ad.uow, ProcessedEvents: ad.processed, Doors: ad.doors}
	dec := &usecases.ApplyLocationSlotDecommissioned{UoW: ad.uow, ProcessedEvents: ad.processed, Doors: ad.doors}
	dc := inboundkafka.NewDockDoorConsumer(cfg.kafkaBrokers, cfg.doorGroup, reg, dec, logger)
	logger.Info("dock door consumer running", "topic", inboundkafka.FacilityTopic, "group_id", cfg.doorGroup, "brokers", cfg.kafkaBrokers)

	declared := &usecases.ApplyPackLevelDeclared{UoW: ad.uow, ProcessedEvents: ad.processed, Packaging: ad.packaging}
	retired := &usecases.ApplyPackLevelRetired{UoW: ad.uow, ProcessedEvents: ad.processed, Packaging: ad.packaging}
	pk := inboundkafka.NewPackagingConsumer(cfg.kafkaBrokers, cfg.packagingGroup, declared, retired, logger)
	logger.Info("packaging consumer running", "topic", inboundkafka.ProductTopic, "group_id", cfg.packagingGroup, "brokers", cfg.kafkaBrokers)

	return []*worker{
		startWorker(logger, "product-consumer", pc.Run, func() { _ = pc.Close() }),
		startWorker(logger, "dock-door-consumer", dc.Run, func() { _ = dc.Close() }),
		startWorker(logger, "packaging-consumer", pk.Run, func() { _ = pk.Close() }),
	}
}

// startOutboxRelay starts the always-on relay goroutine over the Kafka sink.
// Kafka is dialled lazily on the first send, never here.
func startOutboxRelay(store outboxrelay.Store, brokers []string, logger *slog.Logger) (*worker, func(), error) {
	if len(brokers) == 0 {
		return nil, nil, errMissingBrokers
	}
	sink := outboundkafka.NewRelaySink(brokers)
	closeSink := func() {
		if err := sink.Close(); err != nil {
			logger.Error("kafka relay sink close failed", "error", err)
		}
	}
	interval := parseRelayInterval(os.Getenv("OUTBOX_RELAY_INTERVAL"), logger)
	maxAttempts := outboxMaxAttempts(os.Getenv("OUTBOX_MAX_ATTEMPTS"), logger)
	relay := outboxrelay.NewRelay(store, sink, logger, outboxrelay.WithInterval(interval), outboxrelay.WithMaxAttempts(maxAttempts))

	logger.Info("outbox relay running", "brokers", brokers, "interval", interval, "max_attempts", maxAttempts,
		"topics", []string{outboundkafka.Topic, outboundkafka.AnalyticsTopic})
	w := startWorker(logger, "outbox-relay", relay.Run, func() {})
	go func() {
		<-w.done
		closeSink()
	}()
	return w, w.stop, nil
}

// watchWorkers returns a channel that receives the name of every worker that
// returns. Workers only return when stopped or on an unrecoverable failure, so
// a value read while the service is serving means a worker died.
func watchWorkers(workers ...*worker) <-chan string {
	died := make(chan string, len(workers))
	for _, w := range workers {
		if w == nil {
			continue
		}
		go func() {
			<-w.done
			died <- w.name
		}()
	}
	return died
}

// await waits (bounded) for w to return; nil w is a no-op.
func await(logger *slog.Logger, w *worker) {
	if w == nil {
		return
	}
	select {
	case <-w.done:
	case <-time.After(workerDrainTimeout):
		logger.Warn("worker did not stop before the shutdown drain deadline", "worker", w.name)
	}
}
