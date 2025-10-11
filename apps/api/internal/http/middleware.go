package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/example/keepstack/apps/api/internal/observability"
)

// MetricsMiddleware records request metrics for the API.
func MetricsMiddleware(metrics *observability.Metrics) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()

			err := next(c)

			route := c.Path()
			if route == "" {
				route = c.Request().URL.Path
			}

			status := c.Response().Status
			if status == 0 {
				if err != nil {
					status = http.StatusInternalServerError
				} else {
					status = http.StatusOK
				}
			}

			code := strconv.Itoa(status)
			durationSeconds := time.Since(start).Seconds()

			metrics.HTTPRequestTotal.WithLabelValues(route, code).Inc()
			metrics.HTTPRequestDurationSeconds.WithLabelValues(route, code).Observe(durationSeconds)

			if status < 200 || status >= 300 {
				metrics.HTTPRequestNon2xxTotal.WithLabelValues(route, code).Inc()
			}

			return err
		}
	}
}
