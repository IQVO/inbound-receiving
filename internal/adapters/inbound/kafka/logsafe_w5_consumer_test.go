package kafka

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

// TestProductConsumerLogsNoEventDerivedLineBreaks feeds the product consumer a
// payload that is not a CloudEvent and a ProductRegistered whose id and
// subject carry CR/LF and whose data does not decode: no log attribute may
// carry the line break.
func TestProductConsumerLogsNoEventDerivedLineBreaks(t *testing.T) {
	rec := newCRLFRecorder()
	c := &ProductConsumer{Logger: slog.New(rec)}
	ctx := context.Background()
	_ = c.HandleMessage(ctx, []byte("not a CloudEvent"+crlfForged))
	b, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": "id" + crlfForged, "type": TypeProductRegistered, "source": "urn:logsafe",
		"subject": "subject" + crlfForged, "datacontenttype": "application/json",
		"data": []string{"not an object" + crlfForged},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.HandleMessage(ctx, b)
	rec.assertNoLineBreaks(t, 2)
}
