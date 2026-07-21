package main

import (
	"fmt"
	"sync"
	"time"
)

type fallbackSink struct {
	mu             sync.Mutex
	broker         messageSink
	fallback       messageSink
	retryInterval  time.Duration
	now            func() time.Time
	brokerDisabled time.Time
}

func newFallbackSink(broker, fallback messageSink, retryInterval time.Duration) *fallbackSink {
	return newFallbackSinkWithClock(broker, fallback, retryInterval, time.Now)
}

func newFallbackSinkWithClock(broker, fallback messageSink, retryInterval time.Duration, now func() time.Time) *fallbackSink {
	if retryInterval <= 0 {
		retryInterval = time.Second
	}
	if now == nil {
		now = time.Now
	}
	return &fallbackSink{
		broker:        broker,
		fallback:      fallback,
		retryInterval: retryInterval,
		now:           now,
	}
}

func (sink *fallbackSink) WriteMessage(message string) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()

	now := sink.now()
	if !now.Before(sink.brokerDisabled) {
		if err := sink.broker.WriteMessage(message); err == nil {
			sink.brokerDisabled = time.Time{}
			return nil
		} else {
			sink.brokerDisabled = now.Add(sink.retryInterval)
			if fallbackErr := sink.fallback.WriteMessage(message); fallbackErr == nil {
				return nil
			} else {
				return fmt.Errorf("broker: %v; syslog fallback: %w", err, fallbackErr)
			}
		}
	}
	if err := sink.fallback.WriteMessage(message); err != nil {
		return fmt.Errorf("broker retry backoff; syslog fallback: %w", err)
	}
	return nil
}

func (sink *fallbackSink) Close() error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	brokerErr := sink.broker.Close()
	fallbackErr := sink.fallback.Close()
	if brokerErr != nil && fallbackErr != nil {
		return fmt.Errorf("broker close: %v; syslog close: %w", brokerErr, fallbackErr)
	}
	if brokerErr != nil {
		return brokerErr
	}
	return fallbackErr
}
