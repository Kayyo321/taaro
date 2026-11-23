package coordination

import (
	"context"
	"errors"
	"strings"

	"github.com/Kayyo321/taaro/internal/store"
)

// MatchCheckRequest specifies the full execution identity required to reuse an in-flight or completed check.
type MatchCheckRequest struct {
	RepoFullName string `json:"repo"`
	JobName      string `json:"job_name"`
	ProducerRev  string `json:"producer_rev"`
	ConsumerRev  string `json:"consumer_rev"`
	InputsHash   string `json:"inputs_hash"`
	Toolchain    string `json:"toolchain"`
	Flags        string `json:"flags"`
	Environment  string `json:"environment"`
}

// MatchCheckResponse reports whether an existing check is eligible for reuse or a fresh run is required.
type MatchCheckResponse struct {
	Reusable bool                         `json:"reusable"`
	Reason   string                       `json:"reason"`
	Check    *store.ValidationCheckRecord `json:"check,omitempty"`
}

// MatchEligibleCheck enforces docs/PROJECT.md §2: reuse is allowed ONLY when inputs, tree/revisions,
// toolchain, flags, and environment all match explicitly and the job is not flaky.
func (e *Engine) MatchEligibleCheck(ctx context.Context, req MatchCheckRequest) (MatchCheckResponse, error) {
	if strings.TrimSpace(req.RepoFullName) == "" || strings.TrimSpace(req.JobName) == "" {
		return MatchCheckResponse{Reusable: false, Reason: "repo and job_name are required"}, nil
	}
	if unknownIdentity(req.ProducerRev) {
		return MatchCheckResponse{Reusable: false, Reason: "producer_rev is required; fresh run needed"}, nil
	}
	if unknownIdentity(req.InputsHash) {
		return MatchCheckResponse{Reusable: false, Reason: "unknown inputs_hash; fresh run required"}, nil
	}
	if unknownIdentity(req.Toolchain) {
		return MatchCheckResponse{Reusable: false, Reason: "unknown toolchain; fresh run required"}, nil
	}
	if unknownIdentity(req.Environment) {
		return MatchCheckResponse{Reusable: false, Reason: "unknown environment; fresh run required"}, nil
	}

	if strings.EqualFold(strings.TrimSpace(req.ConsumerRev), "unknown") || strings.EqualFold(strings.TrimSpace(req.Flags), "unknown") {
		return MatchCheckResponse{Reusable: false, Reason: "unknown consumer revision or flags; fresh run required"}, nil
	}

	rec, err := e.Store.FindEligibleCheck(
		ctx,
		req.RepoFullName,
		req.JobName,
		req.ProducerRev,
		req.ConsumerRev,
		req.InputsHash,
		req.Toolchain,
		req.Flags,
		req.Environment,
	)
	if errors.Is(err, store.ErrNotFound) {
		return MatchCheckResponse{
			Reusable: false,
			Reason:   "no matching non-flaky check with identical revisions, inputs, toolchain, flags, and environment",
		}, nil
	}
	if err != nil {
		return MatchCheckResponse{}, err
	}
	return MatchCheckResponse{
		Reusable: true,
		Reason:   "exact match on revisions, inputs_hash, toolchain, flags, and environment (non-flaky)",
		Check:    &rec,
	}, nil
}

// RecordValidationCheck persists a validation or combined compatibility check run and cancels
// any superseded speculative in-flight runs for the same (repo, job_name, pr_number).
func (e *Engine) RecordValidationCheck(ctx context.Context, rec store.ValidationCheckRecord) (store.ValidationCheckRecord, []int64, error) {
	if strings.TrimSpace(rec.RepoFullName) == "" || strings.TrimSpace(rec.JobName) == "" || strings.TrimSpace(rec.ProducerRev) == "" {
		return store.ValidationCheckRecord{}, nil, errors.New("repo, job_name, and producer_rev are required")
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = e.now()
	}
	if rec.Status == "" {
		rec.Status = "completed"
	}
	switch rec.Status {
	case "completed":
		if rec.Conclusion != "success" && rec.Conclusion != "failure" && rec.Conclusion != "cancelled" {
			return store.ValidationCheckRecord{}, nil, errors.New("completed checks require a success, failure, or cancelled conclusion")
		}
	case "queued", "in_progress":
		if rec.Conclusion != "" || !rec.CompletedAt.IsZero() {
			return store.ValidationCheckRecord{}, nil, errors.New("unfinished checks cannot specify conclusion or completion time")
		}
	default:
		return store.ValidationCheckRecord{}, nil, errors.New("unsupported check status")
	}
	if rec.Status == "completed" && rec.CompletedAt.IsZero() {
		rec.CompletedAt = e.now()
	}
	return e.Store.RecordValidationCheck(ctx, rec)
}

func unknownIdentity(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.EqualFold(value, "unknown")
}
