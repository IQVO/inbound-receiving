package main

import (
	"errors"
	"strings"
	"testing"

	inboundhttp "github.com/claudioed/inbound-receiving/internal/adapters/inbound/http"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// RECEIVING_SITE_GLNS and RECEIVER_GS1_COMPANY_PREFIX are optional (ADR 0024): unset turns
// the destination rule off and answers 409 to AssignSscc, and a bad value refuses to boot.
func TestLoadConfigReadsTheLabelSettings(t *testing.T) {
	cfg, err := loadConfig(envOf(validEnv()))
	if err != nil || cfg.siteGLNs.Enabled() || !cfg.companyPrefix.IsZero() {
		t.Fatalf("unset: cfg = %+v, err = %v; want the rule off and no prefix", cfg, err)
	}
	cfg, err = loadConfig(envOf(withEnv(map[string]string{
		"RECEIVING_SITE_GLNS": "4012345000009, 6141410000014", "RECEIVER_GS1_COMPANY_PREFIX": "0614141",
	})))
	if err != nil || !cfg.siteGLNs.Enabled() || cfg.companyPrefix.String() != "0614141" {
		t.Fatalf("set: cfg = %+v, err = %v", cfg, err)
	}
	site, _ := gs1.ParseGLN("6141410000014")
	other, _ := gs1.ParseGLN("7612345000008")
	if !cfg.siteGLNs.Accepts(site) || cfg.siteGLNs.Accepts(other) {
		t.Fatal("the list was not read as given")
	}
	// Blank values are the same as unset (the chart renders empty values).
	if cfg, err := loadConfig(envOf(withEnv(map[string]string{"RECEIVING_SITE_GLNS": "", "RECEIVER_GS1_COMPANY_PREFIX": ""}))); err != nil || cfg.siteGLNs.Enabled() || !cfg.companyPrefix.IsZero() {
		t.Fatalf("blank: %+v, %v", cfg, err)
	}
}

func TestLoadConfigRefusesABadSiteGLNOrPrefixNamingTheVariable(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		variable string
		is       error
	}{
		{"a GLN with a bad check digit", map[string]string{"RECEIVING_SITE_GLNS": "4012345000008"}, "RECEIVING_SITE_GLNS", receipt.ErrInvalidSiteGLN},
		{"a GLN that is too short", map[string]string{"RECEIVING_SITE_GLNS": "401234500000"}, "RECEIVING_SITE_GLNS", receipt.ErrInvalidSiteGLN},
		{"one bad GLN in a list", map[string]string{"RECEIVING_SITE_GLNS": "4012345000009,abc"}, "RECEIVING_SITE_GLNS", receipt.ErrInvalidSiteGLN},
		{"a prefix of 6 digits", map[string]string{"RECEIVER_GS1_COMPANY_PREFIX": "061414"}, "RECEIVER_GS1_COMPANY_PREFIX", receipt.ErrInvalidCompanyPrefix},
		{"a prefix of 11 digits", map[string]string{"RECEIVER_GS1_COMPANY_PREFIX": "06141411234"}, "RECEIVER_GS1_COMPANY_PREFIX", receipt.ErrInvalidCompanyPrefix},
		{"a prefix with a letter", map[string]string{"RECEIVER_GS1_COMPANY_PREFIX": "061414A"}, "RECEIVER_GS1_COMPANY_PREFIX", receipt.ErrInvalidCompanyPrefix},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig(envOf(withEnv(tt.env)))
			if err == nil || !errors.Is(err, tt.is) || !strings.Contains(err.Error(), tt.variable) {
				t.Fatalf("loadConfig = %v, want %v naming %s", err, tt.is, tt.variable)
			}
		})
	}
	// The boundaries of a valid prefix.
	for _, p := range []string{"0614141", "0614141123"} {
		if _, err := loadConfig(envOf(withEnv(map[string]string{"RECEIVER_GS1_COMPANY_PREFIX": p}))); err != nil {
			t.Fatalf("prefix %q: %v", p, err)
		}
	}
}

func TestBuildServerWiresAssignSsccAndTheLabelSettings(t *testing.T) {
	ad := testDoubleAdapters(t)
	cfg, err := loadConfig(envOf(withEnv(map[string]string{
		"RECEIVING_SITE_GLNS": "4012345000009", "RECEIVER_GS1_COMPANY_PREFIX": "0614141",
	})))
	if err != nil {
		t.Fatal(err)
	}
	ad = ad.withLabelSettings(cfg)
	s := buildServer(ad, &inboundhttp.Readiness{}, nil, nil)
	if s.AssignSscc == nil || s.AssignSscc.Serials == nil || s.AssignSscc.Prefix.String() != "0614141" {
		t.Fatalf("AssignSscc = %+v", s.AssignSscc)
	}
	if !s.ReceiveScan.SiteGLNs.Enabled() {
		t.Fatal("the destination rule is not wired into the scan use case")
	}
	// Unset: wired and honest about it.
	s = buildServer(testDoubleAdapters(t), &inboundhttp.Readiness{}, nil, nil)
	if s.AssignSscc == nil || !s.AssignSscc.Prefix.IsZero() || s.ReceiveScan.SiteGLNs.Enabled() {
		t.Fatalf("unset settings: %+v", s.AssignSscc)
	}
}
