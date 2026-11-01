package kafka

// Wire payloads (CloudEvents `data`, snake_case JSON). Optional fields are
// omitted when unset.

type asnLineData struct {
	LineNo      int    `json:"line_no"`
	SKU         string `json:"sku"`
	ExpectedQty int64  `json:"expected_qty"`
}

type asnCartonContentData struct {
	LineNo int    `json:"line_no"`
	SKU    string `json:"sku"`
	GTIN   string `json:"gtin"`
	Qty    int64  `json:"qty"`
	Lot    string `json:"lot,omitempty"`
	Expiry string `json:"expiry,omitempty"`
}

type cartonDimsData struct {
	LengthMm int64 `json:"length_mm"`
	WidthMm  int64 `json:"width_mm"`
	HeightMm int64 `json:"height_mm"`
}

type asnCartonData struct {
	SSCC         string                 `json:"sscc"`
	ParentSSCC   string                 `json:"parent_sscc,omitempty"`
	Kind         string                 `json:"kind"`
	Contents     []asnCartonContentData `json:"contents"`
	GrossWeightG int64                  `json:"gross_weight_g,omitempty"`
	Dims         *cartonDimsData        `json:"dims,omitempty"`
}

type asnRegisteredData struct {
	AsnNumber       string          `json:"asn_number"`
	SupplierRef     string          `json:"supplier_ref"`
	OwnerRef        string          `json:"owner_ref,omitempty"`
	ExpectedArrival string          `json:"expected_arrival,omitempty"`
	Lines           []asnLineData   `json:"lines"`
	Cartons         []asnCartonData `json:"cartons,omitempty"`
}

type asnCancelledData struct {
	AsnNumber string `json:"asn_number"`
	Reason    string `json:"reason,omitempty"`
}

type bookedData struct {
	AppointmentID string   `json:"appointment_id"`
	DoorCode      string   `json:"door_code"`
	Carrier       string   `json:"carrier"`
	WindowStart   string   `json:"window_start"`
	WindowEnd     string   `json:"window_end"`
	AsnNumbers    []string `json:"asn_numbers"`
}

type checkedInData struct {
	AppointmentID string `json:"appointment_id"`
	DoorCode      string `json:"door_code"`
	CheckedInAt   string `json:"checked_in_at"`
}

type appointmentCancelledData struct {
	AppointmentID string `json:"appointment_id"`
	DoorCode      string `json:"door_code"`
	Reason        string `json:"reason,omitempty"`
}

type completedData struct {
	AppointmentID string `json:"appointment_id"`
	DoorCode      string `json:"door_code"`
	CompletedAt   string `json:"completed_at"`
}

type receiptOpenedData struct {
	ReceiptID     string `json:"receipt_id"`
	AsnNumber     string `json:"asn_number"`
	AppointmentID string `json:"appointment_id,omitempty"`
	DoorCode      string `json:"door_code,omitempty"`
	OpenedAt      string `json:"opened_at"`
}

type lineReceivedData struct {
	ReceiptID  string `json:"receipt_id"`
	AsnNumber  string `json:"asn_number"`
	LineNo     int    `json:"line_no"`
	SKU        string `json:"sku"`
	Quantity   int64  `json:"quantity"`
	Condition  string `json:"condition"`
	ReceivedAt string `json:"received_at"`
	OwnerRef   string `json:"owner_ref,omitempty"`
	PackGTIN   string `json:"pack_gtin,omitempty"`
	Cases      int64  `json:"cases,omitempty"`
	SSCC       string `json:"sscc,omitempty"`
}

type cartonContentData struct {
	LineNo    int    `json:"line_no"`
	SKU       string `json:"sku"`
	GTIN      string `json:"gtin"`
	Qty       int64  `json:"qty"`
	Lot       string `json:"lot,omitempty"`
	Expiry    string `json:"expiry,omitempty"`
	Condition string `json:"condition"`
}

// cartonReceivedData is IDENTITY ONLY: it never books quantity, and it has no
// site (contracts R6).
type cartonReceivedData struct {
	ReceiptID  string              `json:"receipt_id"`
	AsnNumber  string              `json:"asn_number"`
	OwnerRef   string              `json:"owner_ref,omitempty"`
	SSCC       string              `json:"sscc,omitempty"`
	ParentSSCC string              `json:"parent_sscc,omitempty"`
	Sealed     bool                `json:"sealed"`
	LPN        string              `json:"lpn"`
	Contents   []cartonContentData `json:"contents"`
	ReceivedAt string              `json:"received_at"`
}

type cartonDiscrepancyData struct {
	ReceiptID string `json:"receipt_id"`
	SSCC      string `json:"sscc,omitempty"`
	Kind      string `json:"kind"`
	Expected  string `json:"expected,omitempty"`
	Actual    string `json:"actual,omitempty"`
	At        string `json:"at"`
}

type discrepancyData struct {
	LineNo      int    `json:"line_no"`
	SKU         string `json:"sku"`
	Kind        string `json:"kind"`
	ExpectedQty int64  `json:"expected_qty"`
	ReceivedQty int64  `json:"received_qty"`
	DamagedQty  int64  `json:"damaged_qty"`
}

type receiptClosedData struct {
	ReceiptID     string            `json:"receipt_id"`
	AsnNumber     string            `json:"asn_number"`
	ClosedAt      string            `json:"closed_at"`
	Discrepancies []discrepancyData `json:"discrepancies"`
}
