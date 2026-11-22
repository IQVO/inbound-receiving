package http

import stdhttp "net/http"

// ReplayEntry is one problem type of the service, as the replay-class tests
// see it: its slug, status and declared class, and how to answer it through
// the production error writer.
type ReplayEntry struct {
	Slug   string
	Status int
	Class  string
	err    error
	p      problem
}

// Refuse answers the entry exactly as production does: through writeError
// (the catalogue match) when the entry has an error, else through
// writeProblem, so the class marking runs.
func (e ReplayEntry) Refuse(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if e.err != nil {
		writeError(w, r, e.err)
		return
	}
	writeProblem(w, r, e.p, "refused")
}

func entryOf(p problem, err error) ReplayEntry {
	return ReplayEntry{Slug: p.slug, Status: p.status, Class: string(p.class), err: err, p: p}
}

// ReplayCatalogue lists every problem type this adapter can answer: each
// catalogue entry plus the problems written outside the catalogue (the
// internal error, the readiness 503, the moved appointment writes, the
// unrouted path).
func ReplayCatalogue() []ReplayEntry {
	out := make([]ReplayEntry, 0, len(problemCatalogue)+4)
	for _, e := range problemCatalogue {
		out = append(out, entryOf(e.p, e.err))
	}
	for _, p := range []problem{internalProblem, dependencyUnavailable, appointmentMovedProblem, notFoundResource} {
		out = append(out, entryOf(p, nil))
	}
	return out
}

// SyntheticOutcomeEntry is a problem of the OUTCOME class that is NOT in the
// catalogue: no real problem type of this service is an OUTCOME (ADR 0027),
// so the mechanism (a stored refusal keeps the handler's writes) is proven
// with this stand-in only.
func SyntheticOutcomeEntry() ReplayEntry {
	return entryOf(problem{status: stdhttp.StatusConflict, slug: "synthetic-outcome-probe", title: "Synthetic outcome probe", class: replayOutcome}, nil)
}
