package architecture

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	archgo "github.com/arch-go/arch-go/api"
	"github.com/arch-go/arch-go/api/configuration"
)

// TestA2AAdapterDependencyRule encodes docs/adr/0016: the A2A inbound adapter
// is additive, never load-bearing. It may depend only on the application and
// domain layers (never on outbound adapters, never on cmd), and nothing else
// in this codebase may depend on it; cmd/a2a wires it up and nothing else
// touches it.
func TestA2AAdapterDependencyRule(t *testing.T) {
	moduleInfo := configuration.Load(modulePath)

	t.Run("a2a adapter depends only on application and domain", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.adapters.inbound.a2a.**",
					ShouldOnlyDependsOn: &configuration.Dependencies{
						Internal: []string{
							"**.internal.adapters.inbound.a2a.**",
							"**.internal.application.**",
							"**.internal.domain.**",
						},
					},
				},
			},
		})

		assertArchGoPasses(t, result)
	})

	t.Run("nothing else depends on the a2a adapter", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.**",
					ShouldNotDependsOn: &configuration.Dependencies{
						Internal: []string{"**.internal.adapters.inbound.a2a.**"},
					},
				},
			},
		})

		assertArchGoPasses(t, result)
	})
}

// a2aAllowedUseCases is the complete set of application use-case identifiers
// the A2A adapter may name: the three READ use cases its skills are built on
// and the query types and sentinel they take. An allowlist, not a denylist:
// this context's MCP charter is entirely read-only (ADR 0005, CLAUDE.md rule
// 8), so the A2A agent derived from it is read-only too, and a new write
// use case (RegisterAsn, OpenReceipt, CloseReceipt, ... or the Writer that
// carries the outbox) is refused by construction, however it is named.
var a2aAllowedUseCases = map[string]bool{
	"GetAsn":                true,
	"ListReceipts":          true,
	"ListReceiptsQuery":     true,
	"ListAppointments":      true,
	"ListAppointmentsQuery": true,
	"ErrInvalidListQuery":   true,
}

var (
	useCaseRef  = regexp.MustCompile(`\busecases\.([A-Za-z0-9_]+)`)
	lineComment = regexp.MustCompile(`(?m)//.*$`)
)

// code drops line comments, so the sensors judge what the package DOES, not
// what its documentation says it does not do.
func code(src string) string { return lineComment.ReplaceAllString(src, "") }

// a2aForbidden maps a construct that must never appear in the A2A adapter's
// non-test sources to the reason.
var a2aForbidden = map[string]string{
	"a2a-go/v2/a2aclient": "an outbound A2A client (CLAUDE.md rule 5: no call to a sibling context, ever; ADR 0016)",
	"a2aclient.":          "an outbound A2A client (CLAUDE.md rule 5: no call to a sibling context, ever; ADR 0016)",
	"ports.UnitOfWork":    "a transaction: a read-only agent never writes the outbox",
	"ports.Clock":         "a clock: a read-only agent stamps nothing",
	"outbox.":             "the outbox: a read-only agent publishes no fact",
	".Save(":              "a repository Save: a read-only agent persists nothing",
}

// a2aViolations returns one finding per use case outside the allowlist and per
// forbidden construct in src.
func a2aViolations(src string) []string {
	src = code(src)
	var out []string
	seen := map[string]bool{}
	for _, m := range useCaseRef.FindAllStringSubmatch(src, -1) {
		if name := m[1]; !a2aAllowedUseCases[name] && !seen[name] {
			seen[name] = true
			out = append(out, "usecases."+name+": not one of the three read use cases the A2A agent may use (the agent is read-only like the MCP charter, ADR 0005)")
		}
	}
	for needle, why := range a2aForbidden {
		if strings.Contains(src, needle) {
			out = append(out, needle+": "+why)
		}
	}
	sort.Strings(out)
	return out
}

// TestA2AAdapterIsReadOnlyAndHasNoOutboundClient is the sensor behind the
// ADR 0016 boundaries: no write use case, no outbox access and no outbound A2A
// client in internal/adapters/inbound/a2a (test files excluded: the wire tests
// legitimately seed data with the write use cases and drive the agent with the
// SDK client).
func TestA2AAdapterIsReadOnlyAndHasNoOutboundClient(t *testing.T) {
	files := goFilesUnder(t, "../adapters/inbound/a2a", false)
	if len(files) == 0 {
		t.Fatal("no A2A adapter sources found; the sensor is looking in the wrong place")
	}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, v := range a2aViolations(string(src)) {
			t.Errorf("%s uses %s", path, v)
		}
	}
}

// TestA2AReadOnlyBinaryWiresNoWriteUseCase applies the same allowlist to
// cmd/a2a: the composition root of the read-only agent constructs only the
// three read use cases, so it cannot hand the adapter a write path.
func TestA2AReadOnlyBinaryWiresNoWriteUseCase(t *testing.T) {
	files := goFilesUnder(t, "../../cmd/a2a", false)
	if len(files) == 0 {
		t.Fatal("no cmd/a2a sources found; the sensor is looking in the wrong place")
	}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, name := range useCaseNames(code(string(src))) {
			if !a2aAllowedUseCases[name] {
				t.Errorf("%s wires usecases.%s: the A2A binary is read-only (ADR 0016)", path, name)
			}
		}
		for _, needle := range []string{"outbox.", "ports.UnitOfWork", "ports.EventPublisher", "EVENT_PUBLISHER", "KAFKA_BROKERS", "a2aclient."} {
			if strings.Contains(code(string(src)), needle) {
				t.Errorf("%s mentions %q: the A2A binary has no outbox, publisher, Kafka or outbound client (ADR 0016)", path, needle)
			}
		}
	}
}

func useCaseNames(src string) []string {
	var out []string
	for _, m := range useCaseRef.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestA2AReadOnlySensorBites proves the detector flags each write use case and
// forbidden shape, so the sensors above cannot pass vacuously, and does not
// flag the three read use cases.
func TestA2AReadOnlySensorBites(t *testing.T) {
	for _, write := range []string{"RegisterAsn", "CancelAsn", "BookAppointment", "CheckInAppointment", "CancelAppointment", "OpenReceipt", "ReceiveLine", "ReceiveScan", "CloseReceipt", "Writer", "ListDocks"} {
		if got := a2aViolations("x := &usecases." + write + "{}"); len(got) == 0 {
			t.Errorf("the sensor does not flag usecases.%s", write)
		}
	}
	for needle := range a2aForbidden {
		if got := a2aViolations("package a2a\nvar _ = " + needle + "\n"); len(got) == 0 {
			t.Errorf("the sensor does not flag %q", needle)
		}
	}
	if got := a2aViolations("// the outbox. a2aclient. usecases.RegisterAsn\n"); len(got) != 0 {
		t.Errorf("a comment must not trip the sensor: %v", got)
	}
	read := "d := Deps{GetAsn: &usecases.GetAsn{}, ListReceipts: &usecases.ListReceipts{}, ListAppointments: &usecases.ListAppointments{}}\n" +
		"q := usecases.ListReceiptsQuery{}; r := usecases.ListAppointmentsQuery{}; e := usecases.ErrInvalidListQuery"
	if got := a2aViolations(read); len(got) != 0 {
		t.Errorf("false positive on the read use cases: %v", got)
	}
}
