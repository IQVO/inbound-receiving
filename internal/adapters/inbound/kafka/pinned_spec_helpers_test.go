package kafka

// Shared helpers of the pinned-contract tests (E1-E4). The pins under
// testdata/ are byte-copies of the producers' apis/asyncapi.yaml registered
// in contracts/pinned.json; these helpers read the pins, never the producers.
// Golden fixtures come from the specs' own message examples (provenance:
// pinned spec); facility-layout's LocationSlotDecommissioned has no spec
// example and is derived from its payload schema (see the facility-layout
// contract test).

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// pinnedSpecFile is a pinned asyncapi copy's path fragment (testdata dir of
// this package).
const pinnedSpecDir = "testdata/"

// loadPinnedSpec decodes a pinned asyncapi.yaml copy.
func loadPinnedSpec(t *testing.T, file string) map[string]any {
	t.Helper()
	raw, err := readFileForTest(pinnedSpecDir + file)
	if err != nil {
		t.Fatalf("read pinned spec %s: %v", file, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse pinned spec %s: %v", file, err)
	}
	return doc
}

// pinnedChannel returns the channel definitions map of the doc.
func pinnedChannels(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	chans, ok := doc["channels"].(map[string]any)
	if !ok {
		t.Fatal("pinned spec has no channels map")
	}
	return chans
}

// pinnedChannelFor finds the channel for topic: AsyncAPI 2.x files key the
// channel by the topic name; yard-management's 3.x file uses named channels
// with the topic in the channel `address`.
func pinnedChannelFor(t *testing.T, doc map[string]any, topic string) (string, map[string]any) {
	t.Helper()
	for name, raw := range pinnedChannels(t, doc) {
		def, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name == topic {
			return name, def
		}
		if addr, _ := def["address"].(string); addr == topic {
			return name, def
		}
	}
	t.Fatalf("pinned spec has no channel for topic %q", topic)
	return "", nil
}

// pinnedChannelMessages returns the message defs of a channel, $refs
// resolved into the doc's components.messages. Covers the 2.x
// subscribe/publish message/oneOf shapes and the 3.x channel-level messages
// map (yard-management).
func pinnedChannelMessages(t *testing.T, doc, channel map[string]any) map[string]any {
	t.Helper()
	out := map[string]any{}
	msgs := componentMap(t, doc, "messages")
	for _, opKey := range []string{"subscribe", "publish"} {
		if op, ok := channel[opKey].(map[string]any); ok {
			collectOpMessages(op, msgs, out)
		}
	}
	if channelMsgs, ok := channel["messages"].(map[string]any); ok { // 3.x
		for _, raw := range channelMsgs {
			collectMessage(raw, msgs, out)
		}
	}
	if len(out) == 0 {
		t.Fatal("channel documents no messages")
	}
	return out
}

func collectOpMessages(op, msgs, out map[string]any) {
	msg, ok := op["message"].(map[string]any)
	if !ok {
		return
	}
	if oneOf, ok := msg["oneOf"].([]any); ok {
		for _, raw := range oneOf {
			collectMessage(raw, msgs, out)
		}
		return
	}
	collectMessage(msg, msgs, out)
}

func collectMessage(raw any, msgs, out map[string]any) {
	m, ok := raw.(map[string]any)
	if !ok {
		return
	}
	if ref, _ := m["$ref"].(string); ref != "" {
		if target := refMessage(nil, msgs, ref); target != nil {
			out[refKey(ref)] = target
		}
		return
	}
	if name, _ := m["name"].(string); name != "" {
		out[name] = m
	}
}

// pinnedChannelTypes maps every CloudEvents `type` documented on the channel
// to its message key: from the messages' own examples (payload.type) and, as
// a fallback, from `const` values inside the payload schemas (facility-layout
// inlines the type const and has messages without examples).
func pinnedChannelTypes(t *testing.T, doc, channel map[string]any) map[string]string {
	t.Helper()
	schemas := componentMap(t, doc, "schemas")
	types := map[string]string{}
	for key, raw := range pinnedChannelMessages(t, doc, channel) {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		found := exampleType(t, m)
		if found == "" {
			found = constIn(t, m["payload"], schemas, "com.warehouse.")
		}
		if found != "" {
			types[found] = key
		}
	}
	if len(types) == 0 {
		t.Fatal("no CloudEvents types found on the channel")
	}
	return types
}

// pinnedGolden returns a deep copy of the spec's own example envelope for
// typ on the channel (provenance: the pinned producer spec).
func pinnedGolden(t *testing.T, doc, channel map[string]any, typ string) map[string]any {
	t.Helper()
	for _, raw := range pinnedChannelMessages(t, doc, channel) {
		m, _ := raw.(map[string]any)
		for _, exRaw := range messageExamples(t, m) {
			ex, _ := exRaw.(map[string]any)
			payload, _ := ex["payload"].(map[string]any)
			if got, _ := payload["type"].(string); got == typ {
				return cloneEnvelope(t, payload)
			}
		}
	}
	t.Fatalf("no spec example envelope for type %q", typ)
	return nil
}

// assertCEEnvelope checks the E3 CloudEvents envelope invariants of a golden
// envelope: specversion 1.0, id, source /warehouse/<producer>, the expected
// type, subject, RFC3339 time, application/json datacontenttype, and a
// dataschema urn:warehouse:<producer>:<stream>:<EventName>:vN whose
// EventName is the type's last segment (analytics twins reuse the
// integration event name by design).
func assertCEEnvelope(t *testing.T, env map[string]any, producer, wantType string) {
	t.Helper()
	if got, _ := env["specversion"].(string); got != "1.0" {
		t.Errorf("specversion = %q, want 1.0", got)
	}
	if id, _ := env["id"].(string); id == "" {
		t.Error("id is empty")
	}
	if got, _ := env["source"].(string); got != "/warehouse/"+producer {
		t.Errorf("source = %q, want /warehouse/%s", got, producer)
	}
	if got, _ := env["type"].(string); got != wantType {
		t.Errorf("type = %q, want %q", got, wantType)
	}
	if subj, _ := env["subject"].(string); subj == "" {
		t.Error("subject is empty (it is the Kafka key / aggregate id)")
	}
	if raw, _ := env["time"].(string); raw != "" {
		if _, err := time.Parse(time.RFC3339, raw); err != nil {
			t.Errorf("time %q is not RFC3339: %v", raw, err)
		}
	} else {
		t.Error("time is missing")
	}
	if got, _ := env["datacontenttype"].(string); got != "application/json" {
		t.Errorf("datacontenttype = %q, want application/json", got)
	}
	assertDataSchema(t, env, producer, wantType)
}

var dataschemaRe = regexp.MustCompile(`^urn:warehouse:([a-z-]+):(events|analytics):([A-Za-z0-9]+):v([0-9]+)$`)

func assertDataSchema(t *testing.T, env map[string]any, producer, wantType string) {
	t.Helper()
	urn, _ := env["dataschema"].(string)
	parts := dataschemaRe.FindStringSubmatch(urn)
	if parts == nil {
		t.Errorf("dataschema %q does not match urn:warehouse:<repo>:<stream>:<name>:vN", urn)
		return
	}
	if parts[1] != producer {
		t.Errorf("dataschema repo segment = %q, want %q", parts[1], producer)
	}
	eventName := wantType
	for i := len(wantType) - 1; i >= 0; i-- {
		if wantType[i] == '.' {
			eventName = wantType[i+1:]
			break
		}
	}
	if parts[3] != eventName {
		t.Errorf("dataschema event name = %q, want the type's own name %q", parts[3], eventName)
	}
}

// envelopeWithUnknowns deep-copies a golden envelope and perturbs it for E4:
// an unknown `data` key, an unknown extension attribute, and a FRESH
// CloudEvents id (the original id may already be claimed by an earlier
// assertion in the same world; the id is the idempotency key).
func envelopeWithUnknowns(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	clone := cloneEnvelope(t, env)
	clone["id"] = fmt.Sprintf("%s-unknown-probe", clone["id"])
	// CloudEvents extension names are lowercase alphanumerics only (no
	// underscores), or the SDK's Validate rejects the envelope.
	clone["xconsumercontractprobe"] = "probe"
	data, _ := clone["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	data["consumer_contract_probe"] = "unknown attribute"
	clone["data"] = data
	return clone
}

// cloneEnvelope deep-copies an envelope through JSON so mutations never
// touch the spec-derived original.
func cloneEnvelope(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// goldenBytes marshals an envelope to the Kafka message value form.
func goldenBytes(t *testing.T, env map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// envelopeDataString reads a string field of the envelope's data (yaml.v3
// decodes whole numbers as int; strings stay strings).
func envelopeDataString(t *testing.T, env map[string]any, key string) string {
	t.Helper()
	data, _ := env["data"].(map[string]any)
	got, _ := data[key].(string)
	return got
}

// assertRoutedAndClaimed proves E2 dispatch end to end: HandleMessage
// returned nil (deterministic), the CloudEvents id was claimed by the named
// consumer registry (claims happen inside the use case the switch routed
// to), and the replay of the same bytes stays nil without a second claim
// (the dedupe assertion: the claim set does not grow).
func assertRoutedAndClaimed(t *testing.T, handle func([]byte) error, claim func(id string) bool, id string, env map[string]any) {
	t.Helper()
	if err := handle(goldenBytes(t, env)); err != nil {
		t.Fatalf("first HandleMessage: %v", err)
	}
	if !claim(id) {
		t.Fatalf("CloudEvents id %q was not claimed by the routed use case", id)
	}
	if err := handle(goldenBytes(t, env)); err != nil {
		t.Fatalf("replay HandleMessage: %v", err)
	}
	if claim(id + "-never") {
		t.Fatal("claim probe leaked an unrelated id")
	}
}

// readFileForTest keeps the os import in one place.
func readFileForTest(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// --- small spec-walking helpers ---

func componentMap(t *testing.T, doc map[string]any, kind string) map[string]any {
	t.Helper()
	comps, _ := doc["components"].(map[string]any)
	if comps == nil {
		return map[string]any{}
	}
	m, _ := comps[kind].(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func refKey(ref string) string {
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '/' {
			return ref[i+1:]
		}
	}
	return ref
}

func refMessage(_ *testing.T, msgs map[string]any, ref string) map[string]any {
	m, _ := msgs[refKey(ref)].(map[string]any)
	return m
}

func messageExamples(t *testing.T, m map[string]any) []any {
	t.Helper()
	if exs, ok := m["examples"].([]any); ok {
		return exs
	}
	if ex, ok := m["example"]; ok {
		return []any{ex}
	}
	return nil
}

// exampleType returns the CloudEvents type of a message's first example
// envelope, or "".
func exampleType(t *testing.T, m map[string]any) string {
	t.Helper()
	for _, raw := range messageExamples(t, m) {
		ex, _ := raw.(map[string]any)
		payload, _ := ex["payload"].(map[string]any)
		if typ, _ := payload["type"].(string); typ != "" {
			return typ
		}
	}
	return ""
}

// constIn recursively walks a payload (resolving $refs into schemas) and
// returns the first `const` string with the given prefix (AsyncAPI 2.x
// facility-layout inlines `type` consts this way).
func constIn(t *testing.T, node any, schemas map[string]any, prefix string) string {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		return constInMap(t, v, schemas, prefix)
	case []any:
		for _, item := range v {
			if got := constIn(t, item, schemas, prefix); got != "" {
				return got
			}
		}
	}
	return ""
}

func constInMap(t *testing.T, v, schemas map[string]any, prefix string) string {
	t.Helper()
	if ref, _ := v["$ref"].(string); ref != "" {
		if got := constIn(t, schemas[refKey(ref)], schemas, prefix); got != "" {
			return got
		}
	}
	if s, ok := v["const"].(string); ok && strings.HasPrefix(s, prefix) {
		return s
	}
	for _, val := range v {
		if got := constIn(t, val, schemas, prefix); got != "" {
			return got
		}
	}
	return ""
}
