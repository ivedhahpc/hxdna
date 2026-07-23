package hxdna

// HealthStatus is the verified state of one external dependency a worker checks
// against the live system it integrates with — distinct from ping, which only
// confirms the worker process is alive and connected to NATS.
type HealthStatus string

const (
	HealthOK    HealthStatus = "ok"
	HealthError HealthStatus = "error"
)

// HealthCheckItem is the result of verifying one external dependency (e.g. the
// "target_system_api" check a worker runs against its own configured
// credentials).
type HealthCheckItem struct {
	Name   string       `json:"name"`
	Status HealthStatus `json:"status"`
	Detail string       `json:"detail,omitempty"`
}

// HealthCheckResult is the Data payload a worker's health_check command returns.
// There is no built-in default handler for health_check (unlike ping/describe_capabilities)
// because verifying "is my API reachable" requires knowledge only the worker has — each
// worker registers its own health_check in its Registry, using NewHealthCheckResult to
// compute the overall Status from its individual checks.
type HealthCheckResult struct {
	Status HealthStatus      `json:"status"`
	Checks []HealthCheckItem `json:"checks"`
}

// NewHealthCheckResult computes the overall Status from the given checks — HealthError if
// any check failed, HealthOK otherwise.
func NewHealthCheckResult(checks ...HealthCheckItem) HealthCheckResult {
	status := HealthOK
	for _, c := range checks {
		if c.Status == HealthError {
			status = HealthError
			break
		}
	}
	return HealthCheckResult{Status: status, Checks: checks}
}
