package runs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	statusQueued    = "Queued"
	statusSubmitted = "Submitted"
	runKeyPrefix    = "run:"
)

type RedisStore struct {
	client        *redis.Client
	queueName     string
	recentRunsKey string
}

func NewRedisStore(addr, queueName, recentRunsKey string) *RedisStore {
	return &RedisStore{
		client: redis.NewClient(&redis.Options{
			Addr: addr,
		}),
		queueName:     queueName,
		recentRunsKey: recentRunsKey,
	}
}

func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *RedisStore) Close() error {
	return s.client.Close()
}

func (s *RedisStore) EnqueueRun(ctx context.Context, record RunRecord) error {
	if record.Status == "" {
		record.Status = statusQueued
	}
	if record.CreatedAt == "" {
		record.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	key := runKey(record.RunID)
	pipe := s.client.TxPipeline()
	pipe.HSet(ctx, key, map[string]any{
		"run_id":          record.RunID,
		"language":        record.Language,
		"code":            record.Code,
		"timeout_seconds": strconv.FormatInt(record.TimeoutSeconds, 10),
		"status":          record.Status,
		"created_at":      record.CreatedAt,
	})
	pipe.LPush(ctx, s.recentRunsKey, record.RunID)
	pipe.LTrim(ctx, s.recentRunsKey, 0, 99)
	pipe.RPush(ctx, s.queueName, record.RunID)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *RedisStore) GetRun(ctx context.Context, runID string) (RunRecord, error) {
	values, err := s.client.HGetAll(ctx, runKey(runID)).Result()
	if err != nil {
		return RunRecord{}, err
	}
	if len(values) == 0 {
		return RunRecord{}, ErrNotFound
	}

	timeout, err := strconv.ParseInt(values["timeout_seconds"], 10, 64)
	if err != nil {
		return RunRecord{}, fmt.Errorf("parse timeout for %s: %w", runID, err)
	}

	return RunRecord{
		RunID:          values["run_id"],
		Language:       values["language"],
		Code:           values["code"],
		TimeoutSeconds: timeout,
		Status:         values["status"],
		CreatedAt:      values["created_at"],
	}, nil
}

func (s *RedisStore) DequeueRunID(ctx context.Context, timeout time.Duration) (string, error) {
	result, err := s.client.BLPop(ctx, timeout, s.queueName).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(result) < 2 {
		return "", nil
	}
	return result[1], nil
}

func (s *RedisStore) UpdateRunStatus(ctx context.Context, runID, status string) error {
	return s.client.HSet(ctx, runKey(runID), "status", status).Err()
}

func (s *RedisStore) ListRecentRunIDs(ctx context.Context, limit int64) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.client.LRange(ctx, s.recentRunsKey, 0, limit-1).Result()
}

func (s *RedisStore) QueueDepth(ctx context.Context) (int64, error) {
	return s.client.LLen(ctx, s.queueName).Result()
}

func (s *RedisStore) QueuedRunIDs(ctx context.Context, limit int64) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.client.LRange(ctx, s.queueName, 0, limit-1).Result()
}

func (s *RedisStore) DeleteRun(ctx context.Context, runID string) error {
	exists, err := s.client.Exists(ctx, runKey(runID)).Result()
	if err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}

	pipe := s.client.TxPipeline()
	pipe.Del(ctx, runKey(runID))
	pipe.LRem(ctx, s.recentRunsKey, 0, runID)
	pipe.LRem(ctx, s.queueName, 0, runID)
	_, err = pipe.Exec(ctx)
	if errors.Is(err, redis.Nil) {
		return ErrNotFound
	}
	return err
}

func (s *RedisStore) QueueName() string {
	return s.queueName
}

func runKey(runID string) string {
	return runKeyPrefix + runID
}
