package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	defaultPort     = "3000"
	defaultEndpoint = "/image"
	maxSPAYDLen     = 512
	defaultSize     = 512
	minQRSize       = 120
	maxQRSize       = 2048
	imageTitle      = "QR platba"
)

//go:embed assets/fonts/Arimo-Bold.ttf
var arimoBoldTTF []byte

var (
	titleFont     *opentype.Font
	titleFontErr  error
	titleFontOnce sync.Once
)

type paymentPreset struct {
	AccountNumber string
	BankCode      string
	Currency      string
}

type app struct {
	logger   *slog.Logger
	presets  map[string]paymentPreset
	endpoint string
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	presets, err := parsePresets(os.Getenv("PRESET"))
	if err != nil {
		logger.Error("invalid PRESET", "error", err)
		os.Exit(1)
	}
	endpoint, err := imageEndpoint(os.Getenv("ENDPOINT"))
	if err != nil {
		logger.Error("invalid ENDPOINT", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              ":" + env("HTTP_PORT", defaultPort),
		Handler:           newApp(logger, presets, endpoint).routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("server starting", "addr", server.Addr, "endpoint", endpoint)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func newApp(logger *slog.Logger, presets map[string]paymentPreset, endpoint string) *app {
	return &app{logger: logger, presets: presets, endpoint: endpoint}
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.handleHealth)
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET "+a.endpoint, a.handleImage)
	return logRequests(a.logger, mux)
}

func (a *app) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (a *app) handleImage(w http.ResponseWriter, r *http.Request) {
	spayd, size, err := a.spaydFromImageQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeQR(w, a.logger, spayd, size)
}

func writeQR(w http.ResponseWriter, logger *slog.Logger, spayd string, size int) {
	pngData, err := framedQRCodePNG(spayd, size)
	if err != nil {
		logger.Error("encode qr", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to encode QR code")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `inline; filename="qr-platba.png"`)
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(pngData)
}

func framedQRCodePNG(spayd string, size int) ([]byte, error) {
	code, err := qrcode.New(spayd, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	bitmap := code.Bitmap()
	moduleCount := len(bitmap)
	if moduleCount == 0 {
		return nil, errors.New("empty QR code bitmap")
	}

	renderSize := maxInt(size, minimumFramedQRSize(moduleCount))
	moduleSize := renderSize / (moduleCount + 4)
	var outerMargin, frameSize, labelHeight int
	for moduleSize > 0 {
		outerMargin = (renderSize - moduleCount*moduleSize) / 2
		frameSize = moduleCount * moduleSize
		labelHeight = 4 * moduleSize
		if frameSize+labelHeight+outerMargin <= renderSize {
			break
		}
		moduleSize--
	}
	if moduleSize < 1 {
		return nil, errors.New("size is too small for framed QR code")
	}
	borderWidth := maxInt(1, moduleSize/2)

	canvas := image.NewRGBA(image.Rect(0, 0, renderSize, renderSize))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	frameRect := image.Rect(outerMargin, outerMargin, outerMargin+frameSize, outerMargin+frameSize)
	drawRectangleBorder(canvas, frameRect, borderWidth, color.RGBA{R: 32, G: 32, B: 32, A: 255})

	drawQRBitmap(canvas, bitmap, frameRect.Min, moduleSize)

	labelRect := image.Rect(frameRect.Min.X, frameRect.Max.Y, frameRect.Max.X, frameRect.Max.Y+labelHeight)
	if err := drawLabel(canvas, labelRect, imageTitle, color.RGBA{R: 32, G: 32, B: 32, A: 255}); err != nil {
		return nil, err
	}

	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func minimumFramedQRSize(moduleCount int) int {
	return moduleCount + 8
}

func drawQRBitmap(canvas *image.RGBA, bitmap [][]bool, origin image.Point, moduleSize int) {
	black := image.NewUniform(color.Black)
	for y, row := range bitmap {
		for x, enabled := range row {
			if !enabled {
				continue
			}
			moduleRect := image.Rect(
				origin.X+x*moduleSize,
				origin.Y+y*moduleSize,
				origin.X+(x+1)*moduleSize,
				origin.Y+(y+1)*moduleSize,
			)
			draw.Draw(canvas, moduleRect, black, image.Point{}, draw.Src)
		}
	}
}

func drawRectangleBorder(canvas *image.RGBA, rect image.Rectangle, width int, borderColor color.Color) {
	for offset := range width {
		top := image.Rect(rect.Min.X+offset, rect.Min.Y+offset, rect.Max.X-offset, rect.Min.Y+offset+1)
		bottom := image.Rect(rect.Min.X+offset, rect.Max.Y-offset-1, rect.Max.X-offset, rect.Max.Y-offset)
		left := image.Rect(rect.Min.X+offset, rect.Min.Y+offset, rect.Min.X+offset+1, rect.Max.Y-offset)
		right := image.Rect(rect.Max.X-offset-1, rect.Min.Y+offset, rect.Max.X-offset, rect.Max.Y-offset)
		draw.Draw(canvas, top, image.NewUniform(borderColor), image.Point{}, draw.Src)
		draw.Draw(canvas, bottom, image.NewUniform(borderColor), image.Point{}, draw.Src)
		draw.Draw(canvas, left, image.NewUniform(borderColor), image.Point{}, draw.Src)
		draw.Draw(canvas, right, image.NewUniform(borderColor), image.Point{}, draw.Src)
	}
}

func drawLabel(canvas *image.RGBA, rect image.Rectangle, title string, textColor color.Color) error {
	face, err := titleFontFace(rect.Dy())
	if err != nil {
		face = basicfont.Face7x13
	}
	metrics := face.Metrics()
	textWidth := font.MeasureString(face, title).Round()
	textHeight := metrics.Ascent.Round() + metrics.Descent.Round()
	dot := fixed.Point26_6{
		X: fixed.I(rect.Min.X + maxInt(0, (rect.Dx()-textWidth)/2)),
		Y: fixed.I(rect.Min.Y + maxInt(0, (rect.Dy()-textHeight)/2-1) + metrics.Ascent.Round()),
	}
	drawer := &font.Drawer{
		Dst:  canvas,
		Src:  image.NewUniform(textColor),
		Face: face,
		Dot:  dot,
	}
	drawer.DrawString(title)
	return nil
}

func titleFontFace(labelHeight int) (font.Face, error) {
	titleFontOnce.Do(func() {
		titleFont, titleFontErr = opentype.Parse(arimoBoldTTF)
	})
	if titleFontErr != nil {
		return nil, titleFontErr
	}
	return opentype.NewFace(titleFont, &opentype.FaceOptions{
		Size:    maxFloat(9, float64(labelHeight)*0.76),
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func parsePresets(value string) (map[string]paymentPreset, error) {
	presets := make(map[string]paymentPreset)
	if strings.TrimSpace(value) == "" {
		return presets, nil
	}

	for _, rawPreset := range strings.Split(value, ",") {
		rawPreset = strings.TrimSpace(rawPreset)
		if rawPreset == "" {
			continue
		}
		parts := strings.Split(rawPreset, "|")
		if len(parts) != 4 {
			return nil, fmt.Errorf("preset %q must use format name|accountNumber|bankCode|currency", rawPreset)
		}
		name := strings.TrimSpace(parts[0])
		preset := paymentPreset{
			AccountNumber: strings.TrimSpace(parts[1]),
			BankCode:      strings.TrimSpace(parts[2]),
			Currency:      strings.ToUpper(strings.TrimSpace(parts[3])),
		}
		if name == "" {
			return nil, errors.New("preset name is required")
		}
		normalizedName := normalizePresetName(name)
		if _, exists := presets[normalizedName]; exists {
			return nil, fmt.Errorf("duplicate preset %q", name)
		}
		if err := validateCurrency(preset.Currency); err != nil {
			return nil, fmt.Errorf("invalid preset %q: %w", name, err)
		}
		if _, err := czechIBAN(preset.AccountNumber, preset.BankCode, ""); err != nil {
			return nil, fmt.Errorf("invalid preset %q: %w", name, err)
		}
		presets[normalizedName] = preset
	}
	return presets, nil
}

func (a *app) spaydFromImageQuery(r *http.Request) (string, int, error) {
	query := r.URL.Query()
	accountNumber := strings.TrimSpace(query.Get("accountNumber"))
	bankCode := strings.TrimSpace(query.Get("bankCode"))
	accountPrefix := strings.TrimSpace(query.Get("accountPrefix"))
	currency := strings.TrimSpace(query.Get("currency"))
	if presetName := strings.TrimSpace(query.Get("preset")); presetName != "" {
		preset, ok := a.presets[normalizePresetName(presetName)]
		if !ok {
			return "", 0, fmt.Errorf("unknown preset %q", presetName)
		}
		accountNumber = preset.AccountNumber
		bankCode = preset.BankCode
		accountPrefix = ""
		currency = preset.Currency
	}
	if accountNumber == "" {
		return "", 0, errors.New("accountNumber is required")
	}
	if bankCode == "" {
		return "", 0, errors.New("bankCode is required")
	}

	size, err := parseQRSize(query.Get("size"))
	if err != nil {
		return "", 0, err
	}

	fields := []string{
		"SPD",
		"1.0",
	}
	iban, err := czechIBAN(accountNumber, bankCode, accountPrefix)
	if err != nil {
		return "", 0, err
	}
	fields = append(fields, "ACC:"+iban)

	if amount := strings.TrimSpace(query.Get("amount")); amount != "" {
		normalizedAmount, err := normalizeAmount(amount)
		if err != nil {
			return "", 0, err
		}
		fields = append(fields, "AM:"+normalizedAmount)
	}
	if currency != "" {
		currency = strings.ToUpper(currency)
		if err := validateCurrency(currency); err != nil {
			return "", 0, err
		}
		fields = append(fields, "CC:"+currency)
	}
	if vs := strings.TrimSpace(query.Get("vs")); vs != "" {
		if err := validateNumericField("vs", vs, 10); err != nil {
			return "", 0, err
		}
		fields = append(fields, "X-VS:"+vs)
	}
	if ss := strings.TrimSpace(query.Get("ss")); ss != "" {
		if err := validateNumericField("ss", ss, 10); err != nil {
			return "", 0, err
		}
		fields = append(fields, "X-SS:"+ss)
	}
	if ks := strings.TrimSpace(query.Get("ks")); ks != "" {
		if err := validateNumericField("ks", ks, 10); err != nil {
			return "", 0, err
		}
		fields = append(fields, "X-KS:"+ks)
	}
	if rf := strings.TrimSpace(query.Get("rf")); rf != "" {
		if err := validateNumericField("rf", rf, 16); err != nil {
			return "", 0, err
		}
		fields = append(fields, "RF:"+rf)
	}
	if rn := strings.TrimSpace(query.Get("rn")); rn != "" {
		if err := validateTextField("rn", rn, 35); err != nil {
			return "", 0, err
		}
		fields = append(fields, "RN:"+escapeSPAYDValue(rn))
	}
	if pt := strings.ToUpper(strings.TrimSpace(query.Get("pt"))); pt != "" {
		if err := validateTextField("pt", pt, 3); err != nil {
			return "", 0, err
		}
		fields = append(fields, "PT:"+escapeSPAYDValue(pt))
	}
	if message := strings.TrimSpace(query.Get("message")); message != "" {
		if err := validateTextField("message", message, 60); err != nil {
			return "", 0, err
		}
		fields = append(fields, "MSG:"+escapeSPAYDValue(message))
	}

	spayd := strings.Join(fields, "*")
	if _, err := validateSPAYD(spayd); err != nil {
		return "", 0, err
	}
	return spayd, size, nil
}

func normalizePresetName(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func imageEndpoint(value string) (string, error) {
	endpoint := strings.TrimSpace(value)
	if endpoint == "" {
		return defaultEndpoint, nil
	}
	if !strings.HasPrefix(endpoint, "/") {
		return "", errors.New("ENDPOINT must start with /")
	}
	if endpoint == "/" || endpoint == "/healthz" {
		return "", errors.New("ENDPOINT must not be / or /healthz")
	}
	return endpoint, nil
}

func parseQRSize(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return defaultSize, nil
	}
	size, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, errors.New("size must be an integer")
	}
	if size < minQRSize || size > maxQRSize {
		return 0, fmt.Errorf("size must be between %d and %d", minQRSize, maxQRSize)
	}
	return size, nil
}

func normalizeAmount(value string) (string, error) {
	amount := strings.NewReplacer(" ", "", "\u00a0", "").Replace(strings.TrimSpace(value))
	amount = strings.ReplaceAll(amount, ",", ".")
	if amount == "" {
		return "", errors.New("amount is required")
	}
	if len(amount) > 10 {
		return "", errors.New("amount must contain at most 10 characters")
	}
	parts := strings.Split(amount, ".")
	if len(parts) > 2 {
		return "", errors.New("amount must be a decimal number")
	}
	if !isDigits(parts[0]) {
		return "", errors.New("amount must be a decimal number")
	}
	if len(parts) == 2 {
		if parts[1] == "" || len(parts[1]) > 2 || !isDigits(parts[1]) {
			return "", errors.New("amount must have at most 2 decimal places")
		}
	}
	return amount, nil
}

func validateNumericField(name, value string, maxLength int) error {
	if utf8.RuneCountInString(value) > maxLength || !isDigits(value) {
		return fmt.Errorf("%s must contain at most %d digits", name, maxLength)
	}
	return nil
}

func validateTextField(name, value string, maxLength int) error {
	if utf8.RuneCountInString(value) > maxLength {
		return fmt.Errorf("%s must contain at most %d characters", name, maxLength)
	}
	return nil
}

func validateCurrency(value string) error {
	if len(value) != 3 {
		return errors.New("currency must contain exactly 3 letters")
	}
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return errors.New("currency must contain exactly 3 letters")
		}
	}
	return nil
}

func escapeSPAYDValue(value string) string {
	var builder strings.Builder
	for _, char := range value {
		switch char {
		case '*':
			builder.WriteString("%2A")
		case ':':
			builder.WriteString("%3A")
		case '%':
			builder.WriteString("%25")
		case '\n':
			builder.WriteString("%0A")
		case '\r':
			builder.WriteString("%0D")
		case '\t':
			builder.WriteString("%09")
		default:
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func czechIBAN(accountNumber, bankCode, accountPrefix string) (string, error) {
	prefix := accountPrefix
	number := accountNumber
	if strings.Contains(accountNumber, "-") {
		parts := strings.Split(accountNumber, "-")
		if len(parts) != 2 {
			return "", errors.New("accountNumber must contain at most one dash")
		}
		prefix = strings.TrimSpace(parts[0])
		number = strings.TrimSpace(parts[1])
	}

	if prefix == "" {
		prefix = "0"
	}
	if !isDigits(prefix) || len(prefix) > 6 {
		return "", errors.New("accountPrefix must contain at most 6 digits")
	}
	if !isDigits(number) || len(number) > 10 {
		return "", errors.New("accountNumber must contain at most 10 digits")
	}
	if !isDigits(bankCode) || len(bankCode) != 4 {
		return "", errors.New("bankCode must contain exactly 4 digits")
	}

	bban := bankCode + leftPad(prefix, 6) + leftPad(number, 10)
	checkDigits := 98 - mod97(numericIBANValue(bban+"CZ00"))
	return fmt.Sprintf("CZ%02d%s", checkDigits, bban), nil
}

func numericIBANValue(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= 'A' && char <= 'Z' {
			builder.WriteString(strconv.Itoa(int(char - 'A' + 10)))
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

func mod97(value string) int {
	remainder := 0
	for _, char := range value {
		remainder = (remainder*10 + int(char-'0')) % 97
	}
	return remainder
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func leftPad(value string, length int) string {
	if len(value) >= length {
		return value
	}
	return strings.Repeat("0", length-len(value)) + value
}

func validateSPAYD(value string) (string, error) {
	spayd := strings.TrimSpace(value)
	if spayd == "" {
		return "", errors.New("SPAYD is required")
	}
	if len(spayd) > maxSPAYDLen {
		return "", fmt.Errorf("SPAYD must be at most %d characters", maxSPAYDLen)
	}
	if !strings.HasPrefix(strings.ToUpper(spayd), "SPD*") {
		return "", errors.New(`SPAYD must start with "SPD*"`)
	}
	if !strings.Contains(spayd, "*ACC:") {
		return "", errors.New(`SPAYD must contain an "ACC:" field`)
	}
	return spayd, nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path == "/healthz" {
			return
		}
		logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

func env(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
