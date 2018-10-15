package main

import (
	"encoding/base32"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/context"
	"github.com/gorilla/schema"
	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	// key must be 16, 24 or 32 bytes long (AES-128, AES-192 or AES-256)
	key   = []byte("super-secret-key")
	store = sessions.NewCookieStore(key)
)

var decoder = schema.NewDecoder()

type ColorSetQuestion struct {
	Set1     []string
	Set2     []string
	Orders   []string
	DrawMode int
	Picks    int
}

type ColorSetResponse struct {
	Set1      string
	Set2      string
	Orders    string
	DrawMode  int
	SetPick   int8
	OrderPick int8
}

type QuestionResponse struct {
	Consent           string
	Question1         string
	Question2         string
	Question3         string
	WindowWidth       int
	WindowOrientation string
}

func read_colors_csv(filename string) [][]string {
	csvFile, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}

	r := csv.NewReader(csvFile)
	r.TrimLeadingSpace = true
	records, err := r.ReadAll()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(len(records), "color sets read")

	return records
}

var color_sets = read_colors_csv("colors_hsv_sorted.csv")
var len_color_sets = int32(len(color_sets))

func new_session(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "survey")
	session.Options.MaxAge = -1
	session.Save(r, w)
}

func colors(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "survey")

	// Find IP address of client
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	fip := r.Header.Get("X-Forwarded-For")
	ua := r.Header.Get("User-Agent")

	// Truncate to avoid logging too much data
	if len(fip) > 100 {
		fip = fip[:100]
	}
	if len(ua) > 100 {
		ua = ua[:100]
	}

	// Make sure user has answered questionnaire
	if init := session.Values["id"]; init == nil {
		if r.Method == "POST" {
			// Parse response
			if err := r.ParseMultipartForm(1024); err != nil {
				http.Error(w, "Error parsing response", http.StatusInternalServerError)
				log.Println(err)
				return
			}
			qr := new(QuestionResponse)
			if err := decoder.Decode(qr, r.Form); err != nil {
				http.Error(w, "Error decoding response", http.StatusInternalServerError)
				log.Println(err)
				return
			}

			// Check for data collection consent
			if qr.Consent != "yes" {
				http.Error(w, "No consent", http.StatusInternalServerError)
				return
			}

			// Validate answers
			q1 := qr.Question1
			if q1 != "y" && q1 != "n" && q1 != "dk" && q1 != "dta" {
				zap.L().Info("badanswer", zap.String("ip", ip), zap.String("fip", fip),
					zap.String("ua", ua), zap.Int("n", 1))
				http.Error(w, "Invalid answer", http.StatusInternalServerError)
				return
			}
			q2 := qr.Question2
			if q2 != "y" && q2 != "n" && q2 != "dk" && q2 != "dta" {
				zap.L().Info("badanswer", zap.String("ip", ip), zap.String("fip", fip),
					zap.String("ua", ua), zap.Int("n", 2))
				http.Error(w, "Invalid answer", http.StatusInternalServerError)
				return
			}
			q3 := qr.Question3
			if q3 != "na" && q3 != "dta" && q3 != "dk" && q3 != "dy" &&
				q3 != "py" && q3 != "da" && q3 != "pa" && q3 != "ty" &&
				q3 != "ta" && q3 != "m" && q3 != "o" {
				zap.L().Info("badanswer", zap.String("ip", ip), zap.String("fip", fip),
					zap.String("ua", ua), zap.Int("n", 3))
				http.Error(w, "Invalid answer", http.StatusInternalServerError)
				return
			}
			orientation := qr.WindowOrientation
			if orientation != "l" && orientation != "p" {
				zap.L().Info("badanswer", zap.String("ip", ip), zap.String("fip", fip),
					zap.String("ua", ua), zap.String("n", "wo"))
				http.Error(w, "Invalid answer", http.StatusInternalServerError)
				return
			}

			// Create a random session ID
			session.Values["id"] = strings.TrimRight(
				base32.StdEncoding.EncodeToString(
					securecookie.GenerateRandomKey(32)), "=")

			// Initialize response counter
			session.Values["picks"] = 0

			// Log response
			zap.L().Info("session", zap.String("id", session.Values["id"].(string)),
				zap.String("ip", ip), zap.String("fip", fip), zap.String("ua", ua),
				zap.String("consent", qr.Consent), zap.String("q1", q1),
				zap.String("q2", q2), zap.String("q3", q3),
				zap.Int("ww", qr.WindowWidth), zap.String("wo", orientation))
		} else {
			// Prompt for question answers
			w.Header().Set("Content-Type", "text/json; charset=utf-8")
			w.Write([]byte("{\"Question\": true}"))
			return
		}
	}

	// Randomly pick two color sets
	cycle1 := append([]string(nil), color_sets[rand.Int31n(len_color_sets)]...)
	cycle2 := append([]string(nil), color_sets[rand.Int31n(len_color_sets)]...)

	// Randomly generate four permutations
	orders := [][]int{rand.Perm(8), rand.Perm(8), rand.Perm(8), rand.Perm(8)}
	var ordersStr []string
	for _, x := range orders {
		ordersStr = append(ordersStr, strings.Trim(strings.Replace(fmt.Sprint(x), " ", "", -1), "[]"))
	}

	// Randomly pick a drawing mode
	drawMode := rand.Intn(4)

	// Number of picks the user has made
	picks := session.Values["picks"].(int)

	// Retrieve previous information from cryptographically-signed cookie
	flashes := session.Flashes()

	// Parse, verify, and record response
	if r.Method == "POST" && len(flashes) > 0 {
		if err := r.ParseMultipartForm(1024); err != nil {
			http.Error(w, "Error parsing response", http.StatusInternalServerError)
			log.Println(err)
			return
		}
		csr := new(ColorSetResponse)
		if err := decoder.Decode(csr, r.Form); err != nil {
			http.Error(w, "Error decoding response", http.StatusInternalServerError)
			log.Println(err)
			return
		}
		if flashes[0] != csr.Set1+";"+csr.Set2+";"+csr.Orders+";"+strconv.Itoa(csr.DrawMode) {
			log.Printf("Bad match %s %s\n", flashes[0], csr.Set1+";"+csr.Set2+";"+csr.Orders+";"+strconv.Itoa(csr.DrawMode))
			zap.L().Info("badmatch", zap.String("id", session.Values["id"].(string)),
				zap.String("ip", ip), zap.String("fip", fip))
		} else {
			sp := csr.SetPick
			cp := csr.OrderPick
			if sp > 0 && sp <= 2 && cp > 0 && cp <= 4 {
				//log.Printf("Good match %s %s\n", flashes[0], csr.Set1 + ";" + csr.Set2)
				//log.Println("Pick", csr.Pick)
				zap.L().Info("pick", zap.String("id", session.Values["id"].(string)),
					zap.String("ip", ip), zap.String("fip", fip), zap.String("ua", ua),
					zap.String("c1", csr.Set1), zap.String("c2", csr.Set2),
					zap.String("o", csr.Orders), zap.Int("dm", csr.DrawMode),
					zap.Int8("sp", sp), zap.Int8("cp", cp))
				picks += 1
				session.Values["picks"] = picks
			} else {
				zap.L().Info("badpick", zap.String("id", session.Values["id"].(string)),
					zap.String("ip", ip), zap.String("fip", fip))
			}
		}
	}

	// Save generated sets, permutations, and drawing mode in session
	session.AddFlash(strings.Join(cycle1, ",") + ";" + strings.Join(cycle2, ",") + ";" + strings.Join(ordersStr, ",") + ";" + strconv.Itoa(drawMode))
	session.Save(r, w)

	// Encode JSON response with color cycles
	csq := ColorSetQuestion{cycle1, cycle2, ordersStr, drawMode, session.Values["picks"].(int)}
	w.Header().Set("Content-Type", "text/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(csq); err != nil {
		http.Error(w, "Error encoding JSON", http.StatusInternalServerError)
		return
	}
}

func main() {
	// Seed PRNG with current time
	rand.Seed(time.Now().UnixNano())

	// Set up logger to record results
	logger := zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey: "type",
			TimeKey:    "ts",
			EncodeTime: zapcore.EpochTimeEncoder,
		}),
		zapcore.AddSync(&lumberjack.Logger{
			Filename: "results.log",
		}),
		zapcore.InfoLevel,
	))
	defer logger.Sync() // Flushes buffer, if any
	zap.ReplaceGlobals(logger)

	// Only send cookie to data endpoint, delete cookie when browser is closed,
	// and don't allow access from JavaScript
	store.Options = &sessions.Options{
		Path:     "/colors",
		MaxAge:   0,
		HttpOnly: true,
	}

	// Configure web server
	http.HandleFunc("/colors", colors)
	http.HandleFunc("/colors/new", new_session)
	http.Handle("/", http.FileServer(http.Dir("static")))
	http.ListenAndServe(":8080", context.ClearHandler(http.DefaultServeMux))
}
