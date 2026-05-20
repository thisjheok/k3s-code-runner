package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port               string
	Namespace          string
	KubeconfigPath     string
	DefaultTimeoutSecs int64
	TTLSeconds         int32
	NodeSelector       map[string]string
	RedisAddr          string
	RedisQueueName     string
	RedisRecentRunsKey string
}

func Load() Config {
	return Config{
		Port:               getEnv("PORT", "8080"),
		Namespace:          getEnv("RUNNER_NAMESPACE", "code-runner-system"),
		KubeconfigPath:     getEnv("KUBECONFIG", ""),
		DefaultTimeoutSecs: getEnvInt64("RUNNER_TIMEOUT_SECONDS", 10),
		TTLSeconds:         int32(getEnvInt64("RUNNER_TTL_SECONDS", 300)),
		NodeSelector:       parseNodeSelector(getEnv("RUNNER_NODE_SELECTOR", "")),
		RedisAddr:          getEnv("REDIS_ADDR", "code-runner-redis:6379"),
		RedisQueueName:     getEnv("REDIS_QUEUE_NAME", "runs:queue"),
		RedisRecentRunsKey: getEnv("REDIS_RECENT_RUNS_KEY", "runs:recent"),
	}
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getEnvInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseNodeSelector(value string) map[string]string {
	selector := map[string]string{}
	for _, pair := range strings.Split(value, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key != "" && val != "" {
			selector[key] = val
		}
	}
	return selector
}
