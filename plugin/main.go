// main.go
//
// Tyk rich (gRPC) plugin in Go implementing the Phantom Token pattern with caching.
// (introspection with Accept: application/jwt, cache by SHA-256(opaque), inject upstream JWT)
//
// Env vars:
//
//	INTROSPECTION_URL, CLIENT_ID, CLIENT_SECRET
//	PORT (default "50051"), TIMEOUT_SECONDS (default 2.5)
//	CACHE_MAX_ENTRIES (default 10000), CACHE_JANITOR_SECONDS (default 60), CLOCK_SKEW_SECONDS (default 30)
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	// Local, generated from proto/*.proto
	coprocess "github.com/curityio/tyk-phantom-token-plugin/internal/coprocess"
)

var (
	introspectionURL = os.Getenv("INTROSPECTION_URL")
	clientID         = os.Getenv("CLIENT_ID")
	clientSecret     = os.Getenv("CLIENT_SECRET")
	bearerRe         = regexp.MustCompile(`(?i)^\s*Bearer\s+(.+)\s*$`)
	httpClient       *http.Client

	clockSkew    = durationFromEnvSeconds("CLOCK_SKEW_SECONDS", 30)
	janitorEvery = durationFromEnvSeconds("CACHE_JANITOR_SECONDS", 60)
	cacheMax     = intFromEnv("CACHE_MAX_ENTRIES", 10000)
	introspectTO = durationFromEnvFloatSeconds("TIMEOUT_SECONDS", 2.5)
)

func init() {
	httpClient = &http.Client{Timeout: introspectTO}
}

// -------- Cache --------
type cacheEntry struct {
	JWT     string
	Expires time.Time
}

type jwtCache struct {
	mu   sync.RWMutex
	data map[string]cacheEntry
}

func newJWTCache() *jwtCache {
	c := &jwtCache{data: make(map[string]cacheEntry, 1024)}
	go func() {
		t := time.NewTicker(janitorEvery)
		defer t.Stop()
		for range t.C {
			c.purgeExpired()
			c.enforceCap()
		}
	}()
	return c
}

func (c *jwtCache) get(key string) (string, bool) {
	now := time.Now()
	c.mu.RLock()
	ce, ok := c.data[key]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if now.After(ce.Expires) {
		c.mu.Lock()
		delete(c.data, key)
		c.mu.Unlock()
		return "", false
	}
	return ce.JWT, true
}

func (c *jwtCache) set(key, jwt string, exp time.Time) {
	c.mu.Lock()
	if time.Now().Add(clockSkew).Before(exp) {
		c.data[key] = cacheEntry{JWT: jwt, Expires: exp}
	}
	c.mu.Unlock()
}

func (c *jwtCache) purgeExpired() {
	now := time.Now()
	c.mu.Lock()
	for k, v := range c.data {
		if now.After(v.Expires) {
			delete(c.data, k)
		}
	}
	c.mu.Unlock()
}

func (c *jwtCache) enforceCap() {
	if cacheMax <= 0 {
		return
	}
	c.mu.Lock()
	n := len(c.data)
	if n <= cacheMax {
		c.mu.Unlock()
		return
	}
	toDrop := n - cacheMax
	type kv struct {
		key string
		exp time.Time
	}
	items := make([]kv, 0, n)
	for k, v := range c.data {
		items = append(items, kv{k, v.Expires})
	}
	now := time.Now()
	dropped := 0
	for _, it := range items {
		if dropped >= toDrop {
			break
		}
		if !it.exp.After(now) || it.exp.Sub(now) < 2*time.Minute {
			delete(c.data, it.key)
			dropped++
		}
	}
	for k := range c.data {
		if dropped >= toDrop {
			break
		}
		delete(c.data, k)
		dropped++
	}
	c.mu.Unlock()
}

var cStore = newJWTCache()

// -------- Utilities --------
func durationFromEnvSeconds(name string, def int) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return time.Duration(def) * time.Second
	}
	i, err := strconv.Atoi(v)
	if err != nil || i < 0 {
		return time.Duration(def) * time.Second
	}
	return time.Duration(i) * time.Second
}

func durationFromEnvFloatSeconds(name string, def float64) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return time.Duration(def * float64(time.Second))
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return time.Duration(def * float64(time.Second))
	}
	return time.Duration(f * float64(time.Second))
}

func intFromEnv(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil || i < 0 {
		return def
	}
	return i
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])
}

func extractBearer(h string) string {
	if h == "" {
		return ""
	}
	m := bearerRe.FindStringSubmatch(h)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func parseJWTExp(jwt string) (time.Time, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("not a compact JWS")
	}
	payloadB, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("payload b64 decode: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadB, &claims); err != nil {
		return time.Time{}, fmt.Errorf("payload json: %w", err)
	}
	expVal, ok := claims["exp"]
	if !ok {
		return time.Time{}, fmt.Errorf("no exp claim")
	}
	var expUnix int64
	switch t := expVal.(type) {
	case float64:
		expUnix = int64(t)
	case json.Number:
		if v, err := t.Int64(); err == nil {
			expUnix = v
		}
	default:
		return time.Time{}, fmt.Errorf("exp type unsupported")
	}
	return time.Unix(expUnix, 0), nil
}

// -------- gRPC server --------
type server struct {
	coprocess.UnimplementedDispatcherServer
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "50051"
	}
	if introspectionURL == "" || clientID == "" || clientSecret == "" {
		log.Fatal("INTROSPECTION_URL, CLIENT_ID, and CLIENT_SECRET must be set")
	}

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer()
	coprocess.RegisterDispatcherServer(s, &server{})
	log.Printf("Phantom token gRPC plugin listening on :%s", port)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func (s *server) Dispatch(ctx context.Context, obj *coprocess.Object) (*coprocess.Object, error) {
	switch obj.HookName {
	case "PhantomAuthCheck":
		return phantomAuthCheck(obj)
	case "InjectJwtPostKeyAuth":
		return injectJwtPostKeyAuth(obj)
	default:
		return obj, nil
	}
}

// ---- Hook 1: auth_check ----
func phantomAuthCheck(obj *coprocess.Object) (*coprocess.Object, error) {
	auth := obj.GetRequest().GetHeaders()["Authorization"]
	opaque := extractBearer(auth)
	if opaque == "" {
		return unauthorized(obj, "Missing bearer token"), nil
	}

	key := sha256Hex(opaque)
	if jwt, ok := cStore.get(key); ok {
		ensureMetadata(obj)
		obj.Metadata["phantom_jwt"] = jwt
		obj.Metadata["token"] = opaque
		ensureSession(obj)
		return obj, nil
	}

	jwt, exp, err := introspectForJWTAndExp(opaque)
	if err != nil {
		return unauthorized(obj, fmt.Sprintf("Introspection error: %v", err)), nil
	}
	if jwt == "" {
		return unauthorized(obj, "Token inactive or invalid"), nil
	}

	storeUntil := exp.Add(-clockSkew)
	cStore.set(key, jwt, storeUntil)

	ensureMetadata(obj)
	obj.Metadata["phantom_jwt"] = jwt
	obj.Metadata["token"] = opaque
	ensureSession(obj)
	return obj, nil
}

// ---- Hook 2: post_key_auth ----
func injectJwtPostKeyAuth(obj *coprocess.Object) (*coprocess.Object, error) {
	jwt := ""
	if obj.Metadata != nil {
		jwt = obj.Metadata["phantom_jwt"]
	}
	if jwt == "" {
		return unauthorized(obj, "JWT missing post-auth"), nil
	}
	if obj.Request.SetHeaders == nil {
		obj.Request.SetHeaders = map[string]string{}
	}
	obj.Request.SetHeaders["Authorization"] = "Bearer " + jwt
	return obj, nil
}

// -------- Introspection --------

func introspectForJWTAndExp(opaque string) (string, time.Time, error) {
	form := url.Values{}
	form.Set("token", opaque)

	req, err := http.NewRequest(http.MethodPost, introspectionURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/jwt")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", time.Time{}, fmt.Errorf("introspection status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, err
	}
	j := strings.TrimSpace(string(body))
	if countDots(j) != 2 {
		return "", time.Time{}, nil
	}
	exp, err := parseJWTExp(j)
	if err != nil {
		return j, time.Now().Add(30 * time.Second), nil
	}
	return j, exp, nil
}

// -------- Helpers --------
func ensureMetadata(obj *coprocess.Object) {
	if obj.Metadata == nil {
		obj.Metadata = map[string]string{}
	}
}

func ensureSession(obj *coprocess.Object) {
	if obj.Session == nil {
		obj.Session = &coprocess.SessionState{}
	}
	obj.Session.Rate = 0
	obj.Session.Per = 0
	obj.Session.QuotaMax = -1
	obj.Session.QuotaRemaining = -1
	obj.Session.QuotaRenewalRate = 0
	obj.Session.LastUpdated = ""
	obj.Session.IdExtractorDeadline = 0
}

func countDots(s string) int {
	n := 0
	for _, r := range s {
		if r == '.' {
			n++
		}
	}
	return n
}

func unauthorized(obj *coprocess.Object, msg string) *coprocess.Object {
	if obj.Request == nil {
		obj.Request = &coprocess.MiniRequestObject{}
	}
	// Prepare headers map
	if obj.Request.ReturnOverrides == nil {
		obj.Request.ReturnOverrides = &coprocess.ReturnOverrides{}
	}
	if obj.Request.ReturnOverrides.Headers == nil {
		obj.Request.ReturnOverrides.Headers = map[string]string{}
	}

	// Fill ReturnOverrides on the MiniRequestObject (this short-circuits the request)
	obj.Request.ReturnOverrides.ResponseCode = 401
	obj.Request.ReturnOverrides.ResponseError = msg
	obj.Request.ReturnOverrides.ResponseBody = msg // optional, mirrors error as body
	obj.Request.ReturnOverrides.Headers["WWW-Authenticate"] = `Bearer error="invalid_token"`
	obj.Request.ReturnOverrides.OverrideError = true // ensure body is used

	return obj
}
