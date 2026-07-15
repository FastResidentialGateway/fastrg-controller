package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestBoundedRequestCtx(t *testing.T) {
	t.Run("sets operation deadline", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = httptest.NewRequest("GET", "/", nil)

		ctx, cancel := boundedRequestCtx(ginCtx)
		defer cancel()

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("bounded request context has no deadline")
		}
		if remaining := time.Until(deadline); remaining > requestOpTimeout {
			t.Fatalf("deadline is %s from now, want at most %s", remaining, requestOpTimeout)
		}
	})

	t.Run("propagates request cancellation", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(context.Background())
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = httptest.NewRequest("GET", "/", nil).WithContext(parent)

		ctx, cancel := boundedRequestCtx(ginCtx)
		defer cancel()
		cancelParent()

		select {
		case <-ctx.Done():
			if ctx.Err() != context.Canceled {
				t.Fatalf("context error = %v, want %v", ctx.Err(), context.Canceled)
			}
		case <-time.After(time.Second):
			t.Fatal("bounded request context did not observe parent cancellation")
		}
	})
}
