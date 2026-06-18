package main

import (
	"bytes"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImageEndpointReturnsPNG(t *testing.T) {
	app := newTestApp(t, "")
	request := httptest.NewRequest(http.MethodGet, "/image?accountNumber=2003486113&bankCode=2010&amount=123.45&currency=EUR&vs=2026001&message=2026001%20ACME&size=200", nil)
	response := httptest.NewRecorder()

	app.routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("expected image/png content type, got %q", contentType)
	}
	body := response.Body.Bytes()
	if len(body) < 8 || string(body[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("expected PNG response body")
	}
	image, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if image.Bounds().Dx() != 200 || image.Bounds().Dy() != 200 {
		t.Fatalf("expected 200x200 image, got %dx%d", image.Bounds().Dx(), image.Bounds().Dy())
	}
}

func TestImageEndpointReturnsPNGForExtendedSPAYDAt300px(t *testing.T) {
	app := newTestApp(t, "")
	request := httptest.NewRequest(http.MethodGet, "/image?accountNumber=1234567890&bankCode=0800&amount=123.45&currency=CZK&vs=2026001&ks=0308&rn=ACME%20s.r.o.&message=Invoice%202026001&size=300", nil)
	response := httptest.NewRecorder()

	app.routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("expected image/png content type, got %q", contentType)
	}
	image, err := png.Decode(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if image.Bounds().Dx() != 300 || image.Bounds().Dy() != 300 {
		t.Fatalf("expected 300x300 image, got %dx%d", image.Bounds().Dx(), image.Bounds().Dy())
	}
}

func TestImageEndpointReturnsPNGWhenRequestedSizeIsTooSmallForPayload(t *testing.T) {
	app := newTestApp(t, "")
	request := httptest.NewRequest(http.MethodGet, "/image?accountNumber=1234567890&bankCode=0800&amount=123.45&currency=CZK&vs=2026001&ks=0308&rn=ACME%20s.r.o.&message=Invoice%202026001&size=120", nil)
	response := httptest.NewRecorder()

	app.routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}
	image, err := png.Decode(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if image.Bounds().Dx() < 120 || image.Bounds().Dy() < 120 {
		t.Fatalf("expected image to be at least requested size, got %dx%d", image.Bounds().Dx(), image.Bounds().Dy())
	}
}

func TestHealthEndpoint(t *testing.T) {
	app := newTestApp(t, "")
	for _, path := range []string{"/", "/healthz"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()

		app.routes().ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("expected status 200 for %s, got %d", path, response.Code)
		}
		if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
			t.Fatalf("expected JSON content type for %s, got %q", path, contentType)
		}
	}
}

func TestImageEndpointCanBeConfigured(t *testing.T) {
	app := newTestAppWithEndpoint(t, "", "/qr")
	validRequest := httptest.NewRequest(http.MethodGet, "/qr?accountNumber=2003486113&bankCode=2010&amount=123.45&currency=EUR&size=200", nil)
	validResponse := httptest.NewRecorder()

	app.routes().ServeHTTP(validResponse, validRequest)

	if validResponse.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", validResponse.Code, validResponse.Body.String())
	}

	oldRequest := httptest.NewRequest(http.MethodGet, "/image?accountNumber=2003486113&bankCode=2010&amount=123.45&currency=EUR&size=200", nil)
	oldResponse := httptest.NewRecorder()

	app.routes().ServeHTTP(oldResponse, oldRequest)

	if oldResponse.Code != http.StatusNotFound {
		t.Fatalf("expected old endpoint status 404, got %d", oldResponse.Code)
	}
}

func TestImageEndpointConfigValidation(t *testing.T) {
	for _, endpoint := range []string{"image", "/", "/healthz"} {
		if _, err := imageEndpoint(endpoint); err == nil {
			t.Fatalf("expected invalid endpoint %q to fail", endpoint)
		}
	}

	endpoint, err := imageEndpoint("")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != defaultEndpoint {
		t.Fatalf("expected default endpoint %q, got %q", defaultEndpoint, endpoint)
	}
}

func TestImageEndpointValidatesRequiredParams(t *testing.T) {
	app := newTestApp(t, "")
	request := httptest.NewRequest(http.MethodGet, "/image?accountNumber=2003486113", nil)
	response := httptest.NewRecorder()

	app.routes().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", response.Code)
	}
}

func TestImageEndpointUsesPreset(t *testing.T) {
	app := newTestApp(t, "ABCDE|2003486113|2010|EUR")
	for _, preset := range []string{"ABCDE", "abcde"} {
		request := httptest.NewRequest(http.MethodGet, "/image?preset="+preset+"&accountNumber=1&bankCode=0100&currency=CZK&amount=123.45&vs=2026001&message=2026001%20ACME&size=200", nil)
		response := httptest.NewRecorder()

		app.routes().ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("expected status 200 for preset %s, got %d: %s", preset, response.Code, response.Body.String())
		}
		if contentType := response.Header().Get("Content-Type"); contentType != "image/png" {
			t.Fatalf("expected image/png content type for preset %s, got %q", preset, contentType)
		}
	}
}

func TestImageEndpointRejectsUnknownPreset(t *testing.T) {
	app := newTestApp(t, "ABCDE|2003486113|2010|EUR")
	for _, path := range []string{
		"/image?preset=MISSING&amount=123.45",
		"/image?preset=MISSING&accountNumber=1234567890&bankCode=0800&amount=123.45&currency=CZK",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()

		app.routes().ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400 for %s, got %d", path, response.Code)
		}
	}
}

func TestSPAYDIncludesOptionalPaymentFields(t *testing.T) {
	app := newTestApp(t, "")
	request := httptest.NewRequest(http.MethodGet, "/image?accountNumber=1234567890&bankCode=0800&amount=123.45&currency=CZK&vs=2026001&ss=123&ks=0308&rf=1234567890123456&rn=ACME%20s.r.o.&pt=ip&message=Invoice%202026001", nil)

	spayd, _, err := app.spaydFromImageQuery(request)
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{
		"*AM:123.45",
		"*CC:CZK",
		"*X-VS:2026001",
		"*X-SS:123",
		"*X-KS:0308",
		"*RF:1234567890123456",
		"*RN:ACME s.r.o.",
		"*PT:IP",
		"*MSG:Invoice 2026001",
	} {
		if !strings.Contains(spayd, field) {
			t.Fatalf("expected SPAYD %q to contain %q", spayd, field)
		}
	}
}

func TestSPAYDNormalizesAmount(t *testing.T) {
	app := newTestApp(t, "")
	for _, rawAmount := range []string{"0", "144,44", "10%20234,44", "10%C2%A0234,44"} {
		request := httptest.NewRequest(http.MethodGet, "/image?accountNumber=1234567890&bankCode=0800&amount="+rawAmount+"&currency=CZK", nil)

		spayd, _, err := app.spaydFromImageQuery(request)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(spayd, "*AM:") {
			t.Fatalf("expected SPAYD %q to contain amount", spayd)
		}
		if strings.Contains(spayd, ",") || strings.Contains(spayd, " ") {
			t.Fatalf("expected normalized SPAYD amount, got %q", spayd)
		}
	}
}

func TestSPAYDNormalizesAmountWithThousandsSeparator(t *testing.T) {
	app := newTestApp(t, "")
	request := httptest.NewRequest(http.MethodGet, "/image?accountNumber=1234567890&bankCode=0800&amount=10%20234,44&currency=CZK", nil)

	spayd, _, err := app.spaydFromImageQuery(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spayd, "*AM:10234.44") {
		t.Fatalf("expected normalized amount in SPAYD, got %q", spayd)
	}
}

func TestParsePresetsRejectsDuplicateNames(t *testing.T) {
	_, err := parsePresets("DEMO|1234567890|0800|EUR,demo|987654321|0300|CZK")
	if err == nil {
		t.Fatal("expected duplicate preset error")
	}
}

func TestParsePresetsValidatesCurrency(t *testing.T) {
	_, err := parsePresets("DEMO|1234567890|0800|EURO")
	if err == nil {
		t.Fatal("expected invalid currency error")
	}
}

func TestImageEndpointValidatesPaymentFields(t *testing.T) {
	app := newTestApp(t, "")
	for _, path := range []string{
		"/image?accountNumber=1234567890&bankCode=0800&amount=abc",
		"/image?accountNumber=1234567890&bankCode=0800&amount=123.456",
		"/image?accountNumber=1234567890&bankCode=0800&amount=12345678901",
		"/image?accountNumber=1234567890&bankCode=0800&amount=1,234,56",
		"/image?accountNumber=1234567890&bankCode=0800&currency=EURO",
		"/image?accountNumber=1234567890&bankCode=0800&currency=E1R",
		"/image?accountNumber=1234567890&bankCode=0800&vs=2026A",
		"/image?accountNumber=1234567890&bankCode=0800&vs=12345678901",
		"/image?accountNumber=1234567890&bankCode=0800&ss=2026A",
		"/image?accountNumber=1234567890&bankCode=0800&ss=12345678901",
		"/image?accountNumber=1234567890&bankCode=0800&ks=2026A",
		"/image?accountNumber=1234567890&bankCode=0800&ks=12345678901",
		"/image?accountNumber=1234567890&bankCode=0800&rf=ABC",
		"/image?accountNumber=1234567890&bankCode=0800&rf=12345678901234567",
		"/image?accountNumber=1234567890&bankCode=0800&rn=" + strings.Repeat("A", 36),
		"/image?accountNumber=1234567890&bankCode=0800&pt=ABCD",
		"/image?accountNumber=1234567890&bankCode=0800&message=" + strings.Repeat("A", 61),
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()

		app.routes().ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400 for %s, got %d", path, response.Code)
		}
	}
}

func TestLowLevelEndpointsAreNotRegistered(t *testing.T) {
	app := newTestApp(t, "")
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/qr?spayd=SPD*1.0*ACC:CZ5855000000001265098001", nil),
		httptest.NewRequest(http.MethodPost, "/qr", nil),
		httptest.NewRequest(http.MethodPost, "/spayd", nil),
	} {
		response := httptest.NewRecorder()
		app.routes().ServeHTTP(response, request)

		if response.Code != http.StatusNotFound {
			t.Fatalf("expected status 404 for %s %s, got %d", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestCzechIBAN(t *testing.T) {
	iban, err := czechIBAN("2003486113", "2010", "")
	if err != nil {
		t.Fatal(err)
	}
	if remainder := mod97(numericIBANValue(iban[4:] + iban[:4])); remainder != 1 {
		t.Fatalf("expected valid IBAN checksum, got remainder %d for %s", remainder, iban)
	}
}

func newTestApp(t *testing.T, rawPresets string) *app {
	t.Helper()
	return newTestAppWithEndpoint(t, rawPresets, defaultEndpoint)
}

func newTestAppWithEndpoint(t *testing.T, rawPresets, endpoint string) *app {
	t.Helper()
	presets, err := parsePresets(rawPresets)
	if err != nil {
		t.Fatal(err)
	}
	return newApp(slog.New(slog.NewTextHandler(io.Discard, nil)), presets, endpoint)
}
