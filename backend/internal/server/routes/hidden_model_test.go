package routes

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelFromRequestBody(t *testing.T) {
	require.Equal(t, "gpt-5.6", modelFromRequestBody("application/json", []byte(`{"model":"gpt-5.6"}`)))
	require.Equal(t, "gpt-5.6", modelFromRequestBody("application/json", []byte(`{"session":{"model":"gpt-5.6"}}`)))
	require.Equal(t, "", modelFromRequestBody("application/json", []byte(`{"model":""}`)))
}

func TestModelFromMultipartRequestBody(t *testing.T) {
	body := "--boundary\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ngpt-image\r\n--boundary--\r\n"
	require.Equal(t, "gpt-image", modelFromRequestBody("multipart/form-data; boundary=boundary", []byte(body)))
}
