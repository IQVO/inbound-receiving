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
	RegisteredAt    string          `json:"registered_at,omitempty"`
	Lines           []asnLineData   `json:"lines"`
	Cartons         []asnCartonData `json:"cartons,omitempty"`
}

type asnCancelledData struct {
	AsnNumber string `json:"asn_number"`
	Reason    string `json:"reason,omitempty"`
}

// asnLateData is the payload of AsnLateForArrival (ADR 0025).
type asnLateData struct {
	AsnNumber       string `json:"asn_number"`
	SupplierRef     string `json:"supplier_ref"`
	AppointmentID   string `json:"appointment_id"`
	VisitID         string `json:"visit_id"`
	AsnRegisteredAt string `json:"asn_registered_at"`
	ArrivedAt       string `json:"arrived_at"`
	LatenessSeconds int64  `json:"lateness_seconds"`
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
	// The fields below are additive and optional (ADR 0024): who gave the carton
	// its SSCC and what the scanned label said.
	SSCCSource     string `json:"sscc_source,omitempty"`
	BestBefore     string `json:"best_before,omitempty"`
	Expiry         string `json:"expiry,omitempty"`
	ProductionDate string `json:"production_date,omitempty"`
	PackagingDate  string `json:"packaging_date,omitempty"`
	Serial         string `json:"serial,omitempty"`
	PurchaseOrder  string `json:"purchase_order,omitempty"`
	ShipToGLN      string `json:"ship_to_gln,omitempty"`
	ShipForGLN     string `json:"ship_for_gln,omitempty"`
}

type cartonDiscrepancyData struct {
	ReceiptID string `json:"receipt_id"`
	SSCC      string `json:"sscc,omitempty"`
	Kind      string `json:"kind"`
	Expected  string `json:"expected,omitempty"`
	Actual    string `json:"actual,omitempty"`
	At        string `json:"at"`
	// W13Code is absent for a kind with no W13 code (unmapped, ADR 0023).
	W13Code        string `json:"w13_code,omitempty"`
	CBVDisposition string `json:"cbv_disposition,omitempty"`
	// SupplierRef is set on the label findings of ADR 0024 (additive, optional).
	SupplierRef string `json:"supplier_ref,omitempty"`
}

type discrepancyData struct {
	LineNo         int    `json:"line_no"`
	SKU            string `json:"sku"`
	Kind           string `json:"kind"`
	ExpectedQty    int64  `json:"expected_qty"`
	ReceivedQty    int64  `json:"received_qty"`
	DamagedQty     int64  `json:"damaged_qty"`
	W13Code        string `json:"w13_code,omitempty"`
	CBVDisposition string `json:"cbv_disposition,omitempty"`
}

type receiptClosedData struct {
	ReceiptID     string            `json:"receipt_id"`
	AsnNumber     string            `json:"asn_number"`
	ClosedAt      string            `json:"closed_at"`
	Discrepancies []discrepancyData `json:"discrepancies"`
}

// stowCompletedData is the payload of ReceiptStowCompleted (ADR 0021).
type stowCompletedData struct {
	ReceiptID          string `json:"receipt_id"`
	AsnNumber          string `json:"asn_number"`
	AppointmentID      string `json:"appointment_id"`
	ArrivedAt          string `json:"arrived_at"`
	ClosedAt           string `json:"closed_at"`
	StowedAt           string `json:"stowed_at"`
	DockToStockSeconds int64  `json:"dock_to_stock_seconds"`
	CartonCount        int    `json:"carton_count"`
}

// adviceDiscrepancyData is one exception of an advice line (ADR 0023).
type adviceDiscrepancyData struct {
	Kind           string `json:"kind"`
	Quantity       int64  `json:"quantity"`
	W13Code        string `json:"w13_code,omitempty"`
	CBVDisposition string `json:"cbv_disposition"`
}

type adviceLineData struct {
	LineNo         int                     `json:"line_no"`
	SKU            string                  `json:"sku"`
	GTIN           string                  `json:"gtin,omitempty"`
	ExpectedQty    int64                   `json:"expected_qty"`
	ReceivedQty    int64                   `json:"received_qty"`
	AcceptedQty    int64                   `json:"accepted_qty"`
	RejectedQty    int64                   `json:"rejected_qty"`
	HeldQty        int64                   `json:"held_qty"`
	SSCCCount      int                     `json:"sscc_count"`
	SSCCs          []string                `json:"ssccs,omitempty"`
	SSCCsTruncated bool                    `json:"ssccs_truncated,omitempty"`
	Discrepancies  []adviceDiscrepancyData `json:"discrepancies"`
	// The earliest dates of the accepted cartons of the line (ADR 0024).
	EarliestBestBefore string `json:"earliest_best_before,omitempty"`
	EarliestExpiry     string `json:"earliest_expiry,omitempty"`
}

// adviceFindingData is a carton finding as the advice lists it (evidence, never
// a quantity: ADR 0023 rule 2).
type adviceFindingData struct {
	Kind           string `json:"kind"`
	SSCC           string `json:"sscc,omitempty"`
	Expected       string `json:"expected,omitempty"`
	Actual         string `json:"actual,omitempty"`
	At             string `json:"at"`
	W13Code        string `json:"w13_code,omitempty"`
	CBVDisposition string `json:"cbv_disposition,omitempty"`
}

// receivingAdviceIssuedData is the payload of ReceivingAdviceIssued (ADR 0023).
type receivingAdviceIssuedData struct {
	ReceiptID           string              `json:"receipt_id"`
	AsnNumber           string              `json:"asn_number"`
	DespatchAdviceRef   string              `json:"despatch_advice_ref"`
	SupplierRef         string              `json:"supplier_ref"`
	OwnerRef            string              `json:"owner_ref,omitempty"`
	IssuedAt            string              `json:"issued_at"`
	LastCartonAt        string              `json:"last_carton_at"`
	SLASeconds          int64               `json:"sla_seconds"`
	WithinSLA           bool                `json:"within_sla"`
	Lines               []adviceLineData    `json:"lines"`
	LinesTotal          int                 `json:"lines_total"`
	LinesTruncated      bool                `json:"lines_truncated,omitempty"`
	CartonFindings      []adviceFindingData `json:"carton_findings"`
	CartonFindingsTotal int                 `json:"carton_findings_total"`
}

// receivingAdviceOverdueData is the payload of ReceivingAdviceOverdue (ADR 0023).
type receivingAdviceOverdueData struct {
	ReceiptID    string `json:"receipt_id"`
	AsnNumber    string `json:"asn_number"`
	LastCartonAt string `json:"last_carton_at"`
	DueAt        string `json:"due_at"`
	SLASeconds   int64  `json:"sla_seconds"`
	OverdueAt    string `json:"overdue_at"`
}
