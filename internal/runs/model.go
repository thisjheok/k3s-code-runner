package runs

type CreateRequest struct {
	Language       string `json:"language"`
	Code           string `json:"code"`
	TimeoutSeconds int64  `json:"timeout_seconds,omitempty"`
}

type RunResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

type RunDetail struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	Language   string `json:"language,omitempty"`
	JobName    string `json:"job_name"`
	PodName    string `json:"pod_name,omitempty"`
	NodeName   string `json:"node_name,omitempty"`
	Reason     string `json:"reason,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type RunListResponse struct {
	Runs []RunDetail `json:"runs"`
}

type LogsResponse struct {
	RunID string `json:"run_id"`
	Logs  string `json:"logs"`
}

type SystemStatusResponse struct {
	QueueName           string         `json:"queue_name"`
	QueueDepth          int64          `json:"queue_depth"`
	QueuedRunIDs        []string       `json:"queued_run_ids"`
	WorkerReplicas      int32          `json:"worker_replicas"`
	WorkerReadyReplicas int32          `json:"worker_ready_replicas"`
	WorkerPods          []WorkerPod    `json:"worker_pods"`
	RunnerJobs          []RunnerJob    `json:"runner_jobs"`
	RunnerPods          []RunnerPod    `json:"runner_pods"`
	NodeRuns            map[string]int `json:"node_runs"`
}

type WorkerPod struct {
	Name      string `json:"name"`
	NodeName  string `json:"node_name,omitempty"`
	Phase     string `json:"phase"`
	Ready     bool   `json:"ready"`
	StartedAt string `json:"started_at,omitempty"`
}

type RunnerJob struct {
	RunID      string `json:"run_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Active     int32  `json:"active"`
	Succeeded  int32  `json:"succeeded"`
	Failed     int32  `json:"failed"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type RunnerPod struct {
	RunID     string `json:"run_id"`
	Name      string `json:"name"`
	JobName   string `json:"job_name,omitempty"`
	NodeName  string `json:"node_name,omitempty"`
	Phase     string `json:"phase"`
	Reason    string `json:"reason,omitempty"`
	Ready     bool   `json:"ready"`
	StartedAt string `json:"started_at,omitempty"`
}

type languageSpec struct {
	Image   string
	File    string
	Command []string
}

type RunRecord struct {
	RunID          string
	Language       string
	Code           string
	TimeoutSeconds int64
	Status         string
	CreatedAt      string
}
