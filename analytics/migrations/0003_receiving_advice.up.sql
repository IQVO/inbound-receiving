-- 0003_receiving_advice.up.sql: the receiving advice KPI (ADR 0023). Additive.
--
-- advice_facts: one row per receipt, folded from ReceivingAdviceIssued.
-- within_sla says whether the advice was issued inside the committed window
-- after the last unit received. The report counts a fact in the UTC day of its
-- issued_at.
CREATE TABLE advice_facts (
    receipt_id TEXT COLLATE "C" PRIMARY KEY,
    asn_number TEXT COLLATE "C" NOT NULL,
    issued_at  TIMESTAMPTZ NOT NULL,
    within_sla BOOLEAN     NOT NULL
);

CREATE INDEX idx_advice_facts_issued_at ON advice_facts (issued_at);
