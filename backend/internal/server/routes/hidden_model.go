package routes

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// hiddenModelMiddleware is deliberately placed after API key authentication and
// before composite routing, so a public alias cannot be remapped around it.
func hiddenModelMiddleware(geminiProtocol bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || apiKey.Group == nil {
			c.Next()
			return
		}

		model := strings.TrimSpace(c.Param("model"))
		if model == "" {
			model = strings.TrimSpace(c.Param("modelAction"))
			if idx := strings.IndexByte(model, ':'); idx >= 0 {
				model = model[:idx]
			}
			model = strings.TrimPrefix(model, "/")
		}
		if model == "" && c.Request.Body != nil && c.Request.ContentLength != 0 {
			body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
			if err != nil {
				writeHiddenModelError(c, geminiProtocol, http.StatusBadRequest)
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			model = modelFromRequestBody(c.Request.Header.Get("Content-Type"), body)
		}
		if service.IsGroupModelHidden(apiKey.Group.ModelsListConfig, model) {
			writeHiddenModelError(c, geminiProtocol, http.StatusForbidden)
			return
		}
		c.Next()
	}
}

func modelFromRequestBody(contentType string, body []byte) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err == nil && mediaType == "multipart/form-data" && params["boundary"] != "" {
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, readErr := reader.NextPart()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				break
			}
			if part.FormName() == "model" {
				value, _ := io.ReadAll(part)
				return strings.TrimSpace(string(value))
			}
		}
		return ""
	}
	if !gjson.ValidBytes(body) {
		return ""
	}
	for _, path := range []string{"model", "session.model", "response.model"} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
			return value
		}
	}
	return ""
}

func writeHiddenModelError(c *gin.Context, geminiProtocol bool, status int) {
	if geminiProtocol {
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{
			"code": status, "message": "The requested model is not available for this group", "status": "PERMISSION_DENIED",
		}})
		return
	}
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{
		"type": "permission_error", "code": "model_not_available", "message": "The requested model is not available for this group",
	}})
}
