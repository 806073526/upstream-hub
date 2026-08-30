package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/web"
)

func TestEmbeddedFrontendServesAssetWithoutSPAFallback(t *testing.T) {
	if !web.HasFrontend() {
		t.Skip("frontend dist is not present in this development checkout")
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerFrontend(router, web.DistFS())

	page := httptest.NewRecorder()
	router.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "/assets/index-") {
		t.Fatalf("index response = status %d, body prefix %q", page.Code, page.Body.String()[:min(120, len(page.Body.String()))])
	}

	assetPath := regexp.MustCompile(`(?:src|href)="(/assets/index-[^"]+\.(?:js|css))"`).FindStringSubmatch(page.Body.String())
	if len(assetPath) != 2 {
		t.Fatalf("could not find an asset path in index response: %q", page.Body.String()[:min(120, len(page.Body.String()))])
	}
	if _, err := fs.Stat(web.DistFS(), strings.TrimPrefix(assetPath[1], "/")); err != nil {
		t.Fatalf("embedded asset stat failed: %v", err)
	}

	asset := httptest.NewRecorder()
	router.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, assetPath[1], nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("asset status = %d, body = %q", asset.Code, asset.Body.String()[:min(120, len(asset.Body.String()))])
	}
	if contentType := asset.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("asset content type = %q, want javascript", contentType)
	}
	if strings.HasPrefix(asset.Body.String(), "<!doctype html>") {
		t.Fatal("asset request returned index.html via SPA fallback")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
