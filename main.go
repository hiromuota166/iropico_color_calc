package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"fmt"
)

type ScoreRequest struct {
	ImageBase64 string `json:"image_base64"`
	ThemeHex    string `json:"theme_hex"`
}

type ScoreResponse struct {
	Score       float64 `json:"score"`
	AvgColorHex string  `json:"avg_color_hex"`
	Method      string  `json:"method"`
}

type DebugReq struct {
	ImageBase64 string `json:"image_base64"`
}

type DebugResp struct {
	DecodedLen int    `json:"decoded_len"`
	First8Hex  string `json:"first8_hex"`
	MimeGuess  string `json:"mime_guess"`
	Note       string `json:"note"`
}

func main() {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	http.HandleFunc("/score", handleScore)
	http.HandleFunc("/debug", handleDebug)

	handler := withCORS(mux)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleScore(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

	var req ScoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}

	imgBytes, err := decodeBase64Image(req.ImageBase64)
	if err != nil {
		http.Error(w, "bad image: "+err.Error(), http.StatusBadRequest)
		return
	}

	img, _, err := image.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		http.Error(w, "decode fail: "+err.Error(), http.StatusBadRequest)
		return
	}

	tr, tg, tb, err := parseHexColor(req.ThemeHex)
	if err != nil {
		http.Error(w, "bad theme_hex: "+err.Error(), http.StatusBadRequest)
		return
	}

	lr, lg, lb := averageLinearRGB(img)

	ltR := srgbToLinear(float64(tr) / 255.0)
	ltG := srgbToLinear(float64(tg) / 255.0)
	ltB := srgbToLinear(float64(tb) / 255.0)

	dist := math.Sqrt((lr-ltR)*(lr-ltR) + (lg-ltG)*(lg-ltG) + (lb-ltB)*(lb-ltB))
	maxDist := math.Sqrt(3.0)
	score := 100.0 * (1.0 - dist/maxDist)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	sr := linearToSrgb(lr)
	sg := linearToSrgb(lg)
	sb := linearToSrgb(lb)
	avgHex := "#" + to2Hex(sr) + to2Hex(sg) + to2Hex(sb)

	resp := ScoreResponse{
		Score:       math.Round(score*10) / 10,
		AvgColorHex: avgHex,
		Method:      "linear-srgb-euclidean(sampled)",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleDebug(w http.ResponseWriter, r *http.Request) {
  var req DebugReq
  if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
    http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest); return
  }
  s := strings.TrimSpace(req.ImageBase64)
  if i := strings.Index(s, ","); i != -1 && strings.HasPrefix(strings.ToLower(s), "data:") {
    s = s[i+1:]
  }
  b, err := base64.StdEncoding.DecodeString(s)
  if err != nil {
    http.Error(w, "bad base64: "+err.Error(), http.StatusBadRequest); return
  }

  first := b
  if len(first) > 8 { first = first[:8] }
  mime := http.DetectContentType(b)

  var decOK bool
  var width, height int
  var decErr string
  if img, _, err := image.Decode(bytes.NewReader(b)); err == nil {
    decOK = true
    bounds := img.Bounds()
    width, height = bounds.Dx(), bounds.Dy()
  } else {
    decErr = err.Error()
  }

  json.NewEncoder(w).Encode(map[string]any{
    "decoded_len": len(b),
    "first8_hex":  hex.EncodeToString(first),
    "mime_guess":  mime,
    "decode_ok":   decOK,
    "decode_err":  decErr,
    "width":       width,
    "height":      height,
    "note":        "ブラウザの canvas.toDataURL('image/png') で作ったデータなら decode_ok=true になるはず",
  })
}

func decodeBase64Image(s string) ([]byte, error) {
  s = strings.TrimSpace(s)
  if i := strings.Index(s, ","); i != -1 && strings.HasPrefix(strings.ToLower(s), "data:") {
    s = s[i+1:]
  }
  s = strings.ReplaceAll(s, "\n", "")
  s = strings.ReplaceAll(s, "\r", "")
  s = strings.ReplaceAll(s, " ", "")

  if b, err := base64.StdEncoding.DecodeString(s); err == nil {
    return b, nil
  } else {
    s2 := strings.NewReplacer("-", "+", "_", "/").Replace(s)
    if b2, err2 := base64.StdEncoding.DecodeString(s2); err2 == nil {
      return b2, nil
    }
    if b3, err3 := base64.RawStdEncoding.DecodeString(s2); err3 == nil {
      return b3, nil
    }
    return nil, fmt.Errorf("base64 decode failed")
  }
}


func parseHexColor(h string) (r, g, b uint8, err error) {
	if strings.HasPrefix(h, "#") {
		h = h[1:]
	}
	if len(h) != 6 {
		return 0, 0, 0, errors.New("want #RRGGBB")
	}
	ri, err := strconv.ParseUint(h[0:2], 16, 8)
	if err != nil {
		return
	}
	gi, err := strconv.ParseUint(h[2:4], 16, 8)
	if err != nil {
		return
	}
	bi, err := strconv.ParseUint(h[4:6], 16, 8)
	if err != nil {
		return
	}
	return uint8(ri), uint8(gi), uint8(bi), nil
}

func averageLinearRGB(img image.Image) (float64, float64, float64) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	const maxSamples = 4096
	step := int(math.Max(1, math.Sqrt(float64(w*h/maxSamples))))
	var sumR, sumG, sumB, sumW float64

	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r16, g16, b16, a16 := img.At(x, y).RGBA()
			sr := float64(r16) / 65535.0
			sg := float64(g16) / 65535.0
			sb := float64(b16) / 65535.0
			wa := float64(a16) / 65535.0
			lr := srgbToLinear(sr)
			lg := srgbToLinear(sg)
			lb := srgbToLinear(sb)
			sumR += lr * wa
			sumG += lg * wa
			sumB += lb * wa
			sumW += wa
		}
	}
	if sumW == 0 {
		return 0, 0, 0
	}
	return sumR / sumW, sumG / sumW, sumB / sumW
}

func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearToSrgb(c float64) float64 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1.0/2.4) - 0.055
}

func to2Hex(c float64) string {
	v := int(math.Round(c * 255))
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	s := strconv.FormatInt(int64(v), 16)
	if len(s) == 1 {
		s = "0" + s
	}
	return strings.ToLower(s)
}
