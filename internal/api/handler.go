package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"mini-code-runner/internal/runs"
)

type Handler struct {
	runs   *runs.Service
	logger *slog.Logger
}

func NewHandler(runsService *runs.Service, logger *slog.Logger) *Handler {
	return &Handler{runs: runsService, logger: logger}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.index)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web"))))
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("GET /system/status", h.systemStatus)
	mux.HandleFunc("POST /runs", h.createRun)
	mux.HandleFunc("GET /runs", h.listRuns)
	mux.HandleFunc("GET /runs/{run_id}", h.getRun)
	mux.HandleFunc("GET /runs/{run_id}/logs", h.getLogs)
	mux.HandleFunc("DELETE /runs/{run_id}", h.deleteRun)
	return requestLogger(h.logger, mux)
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/index.html")
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) systemStatus(w http.ResponseWriter, r *http.Request) {
	resp, err := h.runs.SystemStatus(r.Context())
	if err != nil {
		h.logger.Error("system status failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get system status")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) createRun(w http.ResponseWriter, r *http.Request) {
	var req runs.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	resp, err := h.runs.Create(r.Context(), req)
	if err != nil {
		if errors.Is(err, runs.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.logger.Error("create run failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create run")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	resp, err := h.runs.List(r.Context())
	if err != nil {
		h.logger.Error("list runs failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	resp, err := h.runs.Get(r.Context(), r.PathValue("run_id"))
	if errors.Is(err, runs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		h.logger.Error("get run failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get run")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) getLogs(w http.ResponseWriter, r *http.Request) {
	resp, err := h.runs.Logs(r.Context(), r.PathValue("run_id"))
	if errors.Is(err, runs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		h.logger.Error("get logs failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get logs")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) deleteRun(w http.ResponseWriter, r *http.Request) {
	if err := h.runs.Delete(r.Context(), r.PathValue("run_id")); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		h.logger.Error("delete run failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to delete run")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/health") {
			logger.Info("request", "method", r.Method, "path", r.URL.Path)
		}
	})
}
