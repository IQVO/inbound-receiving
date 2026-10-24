package http_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

var eighteenDigits = regexp.MustCompile(`"([0-9]{18})"`)

// Every 18-digit string in the published contracts is an SSCC example; a
// generated client or a contract test replays them, so each must carry a valid
// GS1 mod-10 check digit (340123450000000013 once did not).
func TestContractExamplesCarryValidSSCCs(t *testing.T) {
	for _, name := range []string{"openapi.yaml", "asyncapi.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "apis", name))
		if err != nil {
			t.Fatal(err)
		}
		found := eighteenDigits.FindAllSubmatch(raw, -1)
		if len(found) == 0 {
			t.Fatalf("%s: no SSCC example found: the pattern is stale", name)
		}
		for _, m := range found {
			if _, err := gs1.ParseSSCC(string(m[1])); err != nil {
				t.Errorf("%s: example SSCC %s is invalid: %v", name, m[1], err)
			}
		}
	}
}
