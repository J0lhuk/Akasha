package syncer

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

type requestMetric struct {
	at           time.Time
	actor, route string
	status       int
	ms           float64
	bytes        int64
}

func (h *Hub) startMetrics() {
	h.metricsQueue = make(chan requestMetric, 256)
	h.metricsStop = make(chan struct{})
	h.metricsDone = make(chan struct{})
	go func() {
		defer close(h.metricsDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		batch := make([]requestMetric, 0, 32)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			args := make([]any, 0, len(batch)*6)
			groups := make([]string, 0, len(batch))
			for _, v := range batch {
				groups = append(groups, "(?,?,?,?,?,?)")
				args = append(args, v.at, v.actor, v.route, v.status, v.ms, v.bytes)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := h.db.ExecContext(ctx, "INSERT INTO request_metrics(time,actor,route,status,duration_ms,response_bytes) VALUES "+strings.Join(groups, ","), args...)
			cancel()
			if err != nil {
				h.metricsDropped.Add(uint64(len(batch)))
			}
			batch = batch[:0]
		}
		for {
			select {
			case v := <-h.metricsQueue:
				batch = append(batch, v)
				if len(batch) == 32 {
					flush()
				}
			case <-ticker.C:
				flush()
			case <-h.metricsStop:
				for {
					select {
					case v := <-h.metricsQueue:
						batch = append(batch, v)
						if len(batch) == 32 {
							flush()
						}
					default:
						flush()
						return
					}
				}
			}
		}
	}()
}

type metricWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *metricWriter) WriteHeader(n int) { w.status = n; w.ResponseWriter.WriteHeader(n) }
func (w *metricWriter) Write(b []byte) (int, error) {
	n, e := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, e
}
func (h *Hub) metric(actor, route string, status int, elapsed time.Duration, bytes int64) {
	if strings.HasPrefix(route, "/v1/events/") {
		route = "/v1/events/:project"
	}
	switch route {
	case "/v1/catalog", "/v1/enroll", "/v1/users", "/v1/grants", "/v1/stats", "/v1/events/:project":
	default:
		route = "/other"
	}
	select {
	case h.metricsQueue <- requestMetric{time.Now().UTC(), actor, route, status, float64(elapsed.Microseconds()) / 1000, bytes}:
	default:
		h.metricsDropped.Add(1)
	}
}
func (h *Hub) stats(ctx context.Context) (map[string]any, error) {
	if !h.db.Postgres {
		return nil, errors.New("Timescale metrics require the PostgreSQL hub")
	}
	var count, users, bytes int64
	var mean, p95 float64
	err := h.db.QueryRowContext(ctx, "SELECT count(*),count(DISTINCT actor),coalesce(sum(response_bytes),0),coalesce(avg(duration_ms),0),coalesce(percentile_cont(0.95) WITHIN GROUP(ORDER BY duration_ms),0) FROM request_metrics WHERE time>now()-INTERVAL '24 hours'").Scan(&count, &users, &bytes, &mean, &p95)
	return map[string]any{"database": "PostgreSQL / TimescaleDB", "requests_24h": count, "active_users_24h": users, "response_bytes_24h": bytes, "mean_ms": mean, "p95_ms": p95, "metrics_dropped": h.metricsDropped.Load(), "metrics_pending": len(h.metricsQueue), "pool": h.db.Stats()}, err
}
