package server

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

// NewLogRouter returns an isolated router that protects the controller log
// with the same JWT and token-blacklist checks as the REST API.
func (r *RestServer) NewLogRouter(logFilePath string) http.Handler {
	router := gin.New()
	router.Use(gin.Recovery(), r.AuthMiddlewareWithBlacklist())
	router.GET("/", func(c *gin.Context) {
		f, err := os.Open(logFilePath)
		if err != nil {
			c.String(http.StatusNotFound, "log file not found")
			return
		}
		defer f.Close()

		fi, err := f.Stat()
		if err != nil {
			c.String(http.StatusInternalServerError, "failed to stat log file")
			return
		}

		c.Header("Content-Type", "text/plain; charset=utf-8")
		http.ServeContent(c.Writer, c.Request, filepath.Base(logFilePath), fi.ModTime(), f)
	})
	return router
}
