package main

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// The receiving advice is part of the real write path: the Writer the
// composition root hands to CloseReceipt carries the AdviceStore and the SLA
// (ADR 0023). A nil store would silently issue no advice.
func TestWriterCarriesTheAdviceStoreAndTheSLA(t *testing.T) {
	ad := testDoubleAdapters(t)
	w := ad.writer()
	if ad.advice == nil || w.Advice == nil {
		t.Fatal("the writer must carry the AdviceStore")
	}
	if w.AdviceSLA != 24*time.Hour {
		t.Fatalf("AdviceSLA = %v, want the 24h default", w.AdviceSLA)
	}
}

func TestReceivingAdviceSLAFromTheEnvironment(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for raw, want := range map[string]time.Duration{
		"":     24 * time.Hour, // unset: the default
		"36h":  36 * time.Hour,
		"90m":  90 * time.Minute,
		"3600": time.Hour,      // plain seconds
		"0":    24 * time.Hour, // zero would make every advice late: the default
		"-1h":  24 * time.Hour,
		"soon": 24 * time.Hour,
	} {
		env := func(k string) string {
			if k == "RECEIVING_ADVICE_SLA" {
				return raw
			}
			return ""
		}
		if got := adviceSLA(env, logger); got != want {
			t.Errorf("RECEIVING_ADVICE_SLA=%q -> %v, want %v", raw, got, want)
		}
	}
}
