package runs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"mini-code-runner/internal/config"
)

const (
	labelManagedBy      = "app.kubernetes.io/managed-by"
	labelRunID          = "runner.example.com/run-id"
	labelKueueQueueName = "kueue.x-k8s.io/queue-name"
	statusFailed        = "Failed"
)

var ErrNotFound = errors.New("run not found")
var ErrInvalidInput = errors.New("invalid run request")

type Service struct {
	clientset *kubernetes.Clientset
	store     *RedisStore
	cfg       config.Config
	logger    *slog.Logger
}

func NewService(clientset *kubernetes.Clientset, store *RedisStore, cfg config.Config, logger *slog.Logger) *Service {
	return &Service{clientset: clientset, store: store, cfg: cfg, logger: logger}
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (RunResponse, error) {
	if _, err := getLanguageSpec(req.Language); err != nil {
		return RunResponse{}, err
	}
	if strings.TrimSpace(req.Code) == "" {
		return RunResponse{}, fmt.Errorf("%w: code is required", ErrInvalidInput)
	}

	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = s.cfg.DefaultTimeoutSecs
	}

	runID, err := newRunID()
	if err != nil {
		return RunResponse{}, err
	}

	record := RunRecord{
		RunID:          runID,
		Language:       strings.TrimSpace(req.Language),
		Code:           req.Code,
		TimeoutSeconds: timeout,
		Status:         statusQueued,
	}
	if record.Language == "" {
		record.Language = "python"
	}
	if err := s.store.EnqueueRun(ctx, record); err != nil {
		return RunResponse{}, err
	}

	return RunResponse{RunID: runID, Status: statusQueued}, nil
}

func (s *Service) List(ctx context.Context) (RunListResponse, error) {
	runIDs, err := s.store.ListRecentRunIDs(ctx, 100)
	if err != nil {
		return RunListResponse{}, err
	}

	runs := make([]RunDetail, 0, len(runIDs))
	seen := map[string]struct{}{}
	for _, runID := range runIDs {
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}

		detail, err := s.Get(ctx, runID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return RunListResponse{}, err
		}
		runs = append(runs, detail)
	}

	jobs, err := s.clientset.BatchV1().Jobs(s.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{labelManagedBy: "mini-code-runner"}).String(),
	})
	if err != nil {
		return RunListResponse{}, err
	}

	for i := range jobs.Items {
		job := jobs.Items[i]
		runID := job.Labels[labelRunID]
		if _, ok := seen[runID]; ok {
			continue
		}
		detail, err := s.detailFromJob(ctx, runID, &job)
		if err != nil {
			return RunListResponse{}, err
		}
		runs = append(runs, detail)
	}

	return RunListResponse{Runs: runs}, nil
}

func (s *Service) Get(ctx context.Context, runID string) (RunDetail, error) {
	job, err := s.clientset.BatchV1().Jobs(s.cfg.Namespace).Get(ctx, resourceName(runID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		record, recordErr := s.store.GetRun(ctx, runID)
		if errors.Is(recordErr, ErrNotFound) {
			return RunDetail{}, ErrNotFound
		}
		if recordErr != nil {
			return RunDetail{}, recordErr
		}
		return detailFromRecord(record), nil
	}
	if err != nil {
		return RunDetail{}, err
	}
	return s.detailFromJob(ctx, runID, job)
}

func (s *Service) Logs(ctx context.Context, runID string) (LogsResponse, error) {
	pod, err := s.podForRun(ctx, runID)
	if apierrors.IsNotFound(err) || errors.Is(err, ErrNotFound) {
		if _, recordErr := s.store.GetRun(ctx, runID); recordErr == nil {
			return LogsResponse{RunID: runID, Logs: "Run is queued. Logs will be available after a worker creates the Kubernetes Job."}, nil
		}
		return LogsResponse{}, ErrNotFound
	}
	if err != nil {
		return LogsResponse{}, err
	}

	reader, err := s.clientset.CoreV1().Pods(s.cfg.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: "runner",
	}).Stream(ctx)
	if err != nil {
		return LogsResponse{}, err
	}
	defer reader.Close()

	body, err := io.ReadAll(reader)
	if err != nil {
		return LogsResponse{}, err
	}

	return LogsResponse{RunID: runID, Logs: string(body)}, nil
}

func (s *Service) SystemStatus(ctx context.Context) (SystemStatusResponse, error) {
	queueDepth, err := s.store.QueueDepth(ctx)
	if err != nil {
		return SystemStatusResponse{}, err
	}
	queuedRunIDs, err := s.store.QueuedRunIDs(ctx, 50)
	if err != nil {
		return SystemStatusResponse{}, err
	}

	deployment, err := s.clientset.AppsV1().Deployments(s.cfg.Namespace).Get(ctx, "code-runner-worker", metav1.GetOptions{})
	if err != nil {
		return SystemStatusResponse{}, err
	}

	workerPods, err := s.clientset.CoreV1().Pods(s.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"app": "code-runner-worker"}).String(),
	})
	if err != nil {
		return SystemStatusResponse{}, err
	}

	pods := make([]WorkerPod, 0, len(workerPods.Items))
	for i := range workerPods.Items {
		pod := workerPods.Items[i]
		workerPod := WorkerPod{
			Name:     pod.Name,
			NodeName: pod.Spec.NodeName,
			Phase:    string(pod.Status.Phase),
			Ready:    podReady(&pod),
		}
		if pod.Status.StartTime != nil {
			workerPod.StartedAt = pod.Status.StartTime.Time.Format(time.RFC3339)
		}
		pods = append(pods, workerPod)
	}

	runnerPods, err := s.clientset.CoreV1().Pods(s.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{labelManagedBy: "mini-code-runner"}).String(),
	})
	if err != nil {
		return SystemStatusResponse{}, err
	}

	nodeRuns := map[string]int{}
	runnerPodDetails := make([]RunnerPod, 0, len(runnerPods.Items))
	runnerPodByRunID := map[string]*corev1.Pod{}
	for i := range runnerPods.Items {
		pod := runnerPods.Items[i]
		runID := pod.Labels[labelRunID]
		nodeName := pod.Spec.NodeName
		if nodeName == "" {
			nodeName = "pending"
		}
		nodeRuns[nodeName]++
		runnerPodByRunID[runID] = &pod

		detail := RunnerPod{
			RunID:    runID,
			Name:     pod.Name,
			JobName:  pod.Labels["job-name"],
			NodeName: nodeName,
			Phase:    string(pod.Status.Phase),
			Reason:   podReason(&pod),
			Ready:    podReady(&pod),
		}
		if pod.Status.StartTime != nil {
			detail.StartedAt = pod.Status.StartTime.Time.Format(time.RFC3339)
		}
		runnerPodDetails = append(runnerPodDetails, detail)
	}

	jobs, err := s.clientset.BatchV1().Jobs(s.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{labelManagedBy: "mini-code-runner"}).String(),
	})
	if err != nil {
		return SystemStatusResponse{}, err
	}

	runnerJobs := make([]RunnerJob, 0, len(jobs.Items))
	for i := range jobs.Items {
		job := jobs.Items[i]
		runID := job.Labels[labelRunID]
		detail := RunnerJob{
			RunID:     runID,
			Name:      job.Name,
			Active:    job.Status.Active,
			Succeeded: job.Status.Succeeded,
			Failed:    job.Status.Failed,
			Status:    statusFromJob(&job, runnerPodByRunID[runID]),
		}
		if job.Status.StartTime != nil {
			detail.StartedAt = job.Status.StartTime.Time.Format(time.RFC3339)
		}
		if job.Status.CompletionTime != nil {
			detail.FinishedAt = job.Status.CompletionTime.Time.Format(time.RFC3339)
		}
		runnerJobs = append(runnerJobs, detail)
	}

	return SystemStatusResponse{
		QueueName:           s.store.QueueName(),
		QueueDepth:          queueDepth,
		QueuedRunIDs:        queuedRunIDs,
		WorkerReplicas:      deployment.Status.Replicas,
		WorkerReadyReplicas: deployment.Status.ReadyReplicas,
		WorkerPods:          pods,
		RunnerJobs:          runnerJobs,
		RunnerPods:          runnerPodDetails,
		NodeRuns:            nodeRuns,
	}, nil
}

func (s *Service) Delete(ctx context.Context, runID string) error {
	name := resourceName(runID)
	propagation := metav1.DeletePropagationBackground
	jobErr := s.clientset.BatchV1().Jobs(s.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	cmErr := s.clientset.CoreV1().ConfigMaps(s.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	storeErr := s.store.DeleteRun(ctx, runID)

	if apierrors.IsNotFound(jobErr) && apierrors.IsNotFound(cmErr) && errors.Is(storeErr, ErrNotFound) {
		return ErrNotFound
	}
	if jobErr != nil && !apierrors.IsNotFound(jobErr) {
		return jobErr
	}
	if cmErr != nil && !apierrors.IsNotFound(cmErr) {
		return cmErr
	}
	if storeErr != nil && !errors.Is(storeErr, ErrNotFound) {
		return storeErr
	}
	return nil
}

func (s *Service) SubmitRun(ctx context.Context, record RunRecord) error {
	spec, err := getLanguageSpec(record.Language)
	if err != nil {
		_ = s.store.UpdateRunStatus(ctx, record.RunID, statusFailed)
		return err
	}
	if strings.TrimSpace(record.Code) == "" {
		_ = s.store.UpdateRunStatus(ctx, record.RunID, statusFailed)
		return fmt.Errorf("%w: code is required", ErrInvalidInput)
	}

	timeout := record.TimeoutSeconds
	if timeout <= 0 {
		timeout = s.cfg.DefaultTimeoutSecs
	}

	name := resourceName(record.RunID)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.cfg.Namespace,
			Labels:    runLabels(record.RunID),
		},
		Data: map[string]string{spec.File: record.Code},
	}

	if _, err := s.clientset.CoreV1().ConfigMaps(s.cfg.Namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		_ = s.store.UpdateRunStatus(ctx, record.RunID, statusFailed)
		return err
	}

	job := s.buildJob(name, record.RunID, spec, timeout)
	if _, err := s.clientset.BatchV1().Jobs(s.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return s.store.UpdateRunStatus(ctx, record.RunID, statusSubmitted)
		}

		cleanupErr := s.clientset.CoreV1().ConfigMaps(s.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if cleanupErr != nil && !apierrors.IsNotFound(cleanupErr) {
			s.logger.Warn("failed to clean configmap after job create error", "run_id", record.RunID, "err", cleanupErr)
		}
		_ = s.store.UpdateRunStatus(ctx, record.RunID, statusFailed)
		return err
	}

	return s.store.UpdateRunStatus(ctx, record.RunID, statusSubmitted)
}

func (s *Service) buildJob(name, runID string, spec languageSpec, timeout int64) *batchv1.Job {
	backoffLimit := int32(0)
	defaultMode := int32(0444)
	suspend := false
	jobLabels := runLabels(runID)
	if s.cfg.KueueEnabled && strings.TrimSpace(s.cfg.KueueQueueName) != "" {
		suspend = true
		jobLabels[labelKueueQueueName] = s.cfg.KueueQueueName
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.cfg.Namespace,
			Labels:    jobLabels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &timeout,
			Suspend:                 &suspend,
			TTLSecondsAfterFinished: &s.cfg.TTLSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: runLabels(runID)},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					NodeSelector:  s.cfg.NodeSelector,
					Containers: []corev1.Container{
						{
							Name:            "runner",
							Image:           spec.Image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         spec.Command,
							VolumeMounts: []corev1.VolumeMount{
								{Name: "code", MountPath: "/workspace", ReadOnly: true},
								{Name: "tmp", MountPath: "/tmp"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resourceQuantity(s.cfg.RunnerCPURequest, "100m"),
									corev1.ResourceMemory: resourceQuantity(s.cfg.RunnerMemoryRequest, "64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resourceQuantity(s.cfg.RunnerCPULimit, "500m"),
									corev1.ResourceMemory: resourceQuantity(s.cfg.RunnerMemoryLimit, "256Mi"),
								},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr(false),
								ReadOnlyRootFilesystem:   ptr(true),
								RunAsNonRoot:             ptr(true),
								RunAsUser:                ptr(int64(1000)),
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "code",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: name},
									DefaultMode:          &defaultMode,
								},
							},
						},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
}

func resourceQuantity(value, fallback string) resource.Quantity {
	quantity, err := resource.ParseQuantity(strings.TrimSpace(value))
	if err == nil {
		return quantity
	}
	return resource.MustParse(fallback)
}

func (s *Service) detailFromJob(ctx context.Context, runID string, job *batchv1.Job) (RunDetail, error) {
	pod, err := s.podForRun(ctx, runID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return RunDetail{}, err
	}

	detail := RunDetail{
		RunID:    runID,
		Status:   statusFromJob(job, pod),
		JobName:  job.Name,
		Language: "",
	}
	if job.Status.StartTime != nil {
		detail.StartedAt = job.Status.StartTime.Time.Format("2006-01-02T15:04:05Z07:00")
	}
	if job.Status.CompletionTime != nil {
		detail.FinishedAt = job.Status.CompletionTime.Time.Format("2006-01-02T15:04:05Z07:00")
	}
	if pod != nil {
		detail.PodName = pod.Name
		detail.NodeName = pod.Spec.NodeName
		detail.Reason = podReason(pod)
	}
	return detail, nil
}

func detailFromRecord(record RunRecord) RunDetail {
	return RunDetail{
		RunID:    record.RunID,
		Status:   record.Status,
		Language: record.Language,
	}
}

func (s *Service) podForRun(ctx context.Context, runID string) (*corev1.Pod, error) {
	pods, err := s.clientset.CoreV1().Pods(s.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{labelRunID: runID}).String(),
	})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) == 0 {
		return nil, ErrNotFound
	}
	return &pods.Items[0], nil
}

func statusFromJob(job *batchv1.Job, pod *corev1.Pod) string {
	if pod != nil {
		reason := podReason(pod)
		if reason == "OOMKilled" {
			return "OOMKilled"
		}
		if reason == "DeadlineExceeded" {
			return "Timeout"
		}
		if pod.Status.Phase == corev1.PodRunning {
			return "Running"
		}
	}

	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return "Succeeded"
		}
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			if condition.Reason == "DeadlineExceeded" {
				return "Timeout"
			}
			return statusFailed
		}
	}
	return "Pending"
}

func podReason(pod *corev1.Pod) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Terminated != nil {
			return status.State.Terminated.Reason
		}
		if status.State.Waiting != nil {
			return status.State.Waiting.Reason
		}
	}
	return string(pod.Status.Phase)
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func getLanguageSpec(language string) (languageSpec, error) {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "", "python", "py":
		return languageSpec{
			Image:   "python:3.11-slim",
			File:    "main.py",
			Command: []string{"python", "/workspace/main.py"},
		}, nil
	case "node", "javascript", "js":
		return languageSpec{
			Image:   "node:20-slim",
			File:    "main.js",
			Command: []string{"node", "/workspace/main.js"},
		}, nil
	default:
		return languageSpec{}, fmt.Errorf("%w: unsupported language: %s", ErrInvalidInput, language)
	}
}

func newRunID() (string, error) {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "run-" + hex.EncodeToString(bytes[:]), nil
}

func resourceName(runID string) string {
	return "code-" + runID
}

func runLabels(runID string) map[string]string {
	return map[string]string{
		labelManagedBy: "mini-code-runner",
		labelRunID:     runID,
	}
}

func ptr[T any](value T) *T {
	return &value
}
