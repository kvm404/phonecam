package control

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
	"github.com/kvm404/phonecam/linux-cli/internal/rtp"
	"github.com/kvm404/phonecam/linux-cli/internal/trust"
)

type stubMedia struct {
	stats    rtp.Stats
	restarts int
}

func (m stubMedia) SetAllow(pairing.RTPSource) {}

func (m stubMedia) Stats() rtp.Stats { return m.stats }

func (m stubMedia) RestartReceiver(pairing.VideoProfile) error { return nil }

func (m stubMedia) ReceiverRestarts() int { return m.restarts }

type recordingMedia struct {
	mu       sync.Mutex
	allows   []pairing.RTPSource
	restarts []pairing.VideoProfile
	stats    rtp.Stats
}

func (m *recordingMedia) SetAllow(src pairing.RTPSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allows = append(m.allows, src)
}

func (m *recordingMedia) Stats() rtp.Stats { return m.stats }

func (m *recordingMedia) RestartReceiver(video pairing.VideoProfile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restarts = append(m.restarts, video)
	return nil
}

func (m *recordingMedia) ReceiverRestarts() int { return 0 }

func (m *recordingMedia) restartCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.restarts)
}

func (m *recordingMedia) lastAllow() (pairing.RTPSource, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.allows) == 0 {
		return pairing.RTPSource{}, false
	}
	return m.allows[len(m.allows)-1], true
}

type fakeClock struct {
	now time.Time
}

func (f fakeClock) Now() time.Time {
	return f.now
}

type memTrust struct {
	mu     sync.Mutex
	phones []trust.Phone
	n      int
}

func (m *memTrust) LookupBySecret(phoneID, secret string) (trust.Phone, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.phones {
		if p.ID == phoneID && p.Secret == secret {
			return p, true
		}
	}
	return trust.Phone{}, false
}

func (m *memTrust) Upsert(id, name string, now time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n++
	secret := fmt.Sprintf("pairing-secret-%d", m.n)
	for i, p := range m.phones {
		if p.ID == id {
			p.Name = name
			p.Secret = secret
			p.LastSeen = now
			m.phones[i] = p
			return secret, nil
		}
	}
	m.phones = append(m.phones, trust.Phone{
		ID: id, Name: name, Secret: secret, CreatedAt: now, LastSeen: now,
	})
	return secret, nil
}

func (m *memTrust) List() []trust.PublicPhone {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]trust.PublicPhone, 0, len(m.phones))
	for _, p := range m.phones {
		out = append(out, trust.PublicPhone{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, LastSeen: p.LastSeen})
	}
	return out
}

func (m *memTrust) Revoke(idOrName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.phones[:0]
	n := 0
	for _, p := range m.phones {
		if p.ID == idOrName || p.Name == idOrName {
			n++
			continue
		}
		kept = append(kept, p)
	}
	if n == 0 {
		return trust.ErrNotFound
	}
	m.phones = kept
	return nil
}

func (m *memTrust) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.phones)
}

func (m *memTrust) Put(id, name, secret string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" || secret == "" {
		return trust.ErrEmptyID
	}
	for i, p := range m.phones {
		if p.ID == id {
			p.Name = name
			p.Secret = secret
			p.LastSeen = now
			m.phones[i] = p
			return nil
		}
	}
	m.phones = append(m.phones, trust.Phone{
		ID: id, Name: name, Secret: secret, CreatedAt: now, LastSeen: now,
	})
	return nil
}

func (m *memTrust) Touch(phoneID string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.phones {
		if p.ID == phoneID {
			m.phones[i].LastSeen = now
			return
		}
	}
}

func TestHealth(t *testing.T) {
	server := New(Config{}).Handler()
	recorder := httptest.NewRecorder()

	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
}

func TestPairingReturnsPayload(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now}}).Handler()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/pairing", nil)
	request.RemoteAddr = "127.0.0.1:40000"

	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var payload pairing.Payload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid payload json: %v", err)
	}
	if payload.SessionID != session.Payload().SessionID || payload.Token == "" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	raw := mustJSONMap(t, recorder.Body.Bytes())
	if _, ok := raw["resume_token"]; ok {
		t.Fatal("GET /pairing must not include resume_token")
	}
	if _, ok := raw["pairing_secret"]; ok {
		t.Fatal("GET /pairing must not include pairing_secret")
	}
}

func TestPairingRejectsNonLocalRequest(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now}}).Handler()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/pairing", nil)
	request.RemoteAddr = "192.168.1.50:40000"

	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestPairConsumesTokenAndWaitsForApproval(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	recorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if session.IsApproved() {
		t.Fatal("pairing should not auto-approve")
	}
	body := mustJSONMap(t, recorder.Body.Bytes())
	if approved, _ := body["approved"].(bool); approved {
		t.Fatalf("require-approval 202 must have approved=false, got %#v", body)
	}
	if _, ok := body["resume_token"]; ok {
		t.Fatal("require-approval 202 must not include resume_token")
	}
	if _, ok := body["pairing_secret"]; ok {
		t.Fatal("require-approval 202 must not include pairing_secret")
	}
}

func TestPairStoresNegotiatedVideo(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	recorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
		Video:     &pairing.VideoProfile{Width: 720, Height: 1280, FPS: 24},
	})

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := session.NegotiatedVideo(); got != (pairing.VideoProfile{Width: 720, Height: 1280, FPS: 24}) {
		t.Fatalf("expected stored negotiated video, got %#v", got)
	}
}

func TestPairWithoutVideoUsesAdvertisedProfile(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	recorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := session.NegotiatedVideo(); got != (pairing.VideoProfile{Width: 1280, Height: 720, FPS: 30}) {
		t.Fatalf("expected advertised profile fallback, got %#v", got)
	}
}

func TestPairRejectsInvalidVideo(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	recorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
		Video:     &pairing.VideoProfile{Width: 720, Height: 1280, FPS: 999},
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestPairRejectsWrongToken(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	recorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     "wrong",
		Phone:     pairing.Phone{Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestPairRejectsExpiredToken(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(pairing.DefaultTTL)}}).Handler()

	recorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})

	if recorder.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestApproveRequiresConsumedToken(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	recorder := postLocalJSON(server, "/approve", approveRequest{SessionID: session.Payload().SessionID})

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestApproveAfterPairing(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	pairRecorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if pairRecorder.Code != http.StatusAccepted {
		t.Fatalf("expected pair 202, got %d: %s", pairRecorder.Code, pairRecorder.Body.String())
	}

	approveRecorder := postLocalJSON(server, "/approve", approveRequest{SessionID: session.Payload().SessionID})
	if approveRecorder.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d: %s", approveRecorder.Code, approveRecorder.Body.String())
	}
	if !session.IsApproved() {
		t.Fatal("expected session to be approved")
	}
}

func TestApproveRejectsWrongSession(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	recorder := postLocalJSON(server, "/approve", approveRequest{SessionID: "wrong"})

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestStatusReportsApproval(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	postLocalJSON(server, "/approve", approveRequest{SessionID: session.Payload().SessionID})

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var status statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid status json: %v", err)
	}
	if !status.Approved {
		t.Fatalf("expected approved status, got %#v", status)
	}
}

func TestStatusIncludesRTPCounters(t *testing.T) {
	session, now := newTestSession(t)
	last := now.Add(time.Second - 14*time.Millisecond)
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now.Add(time.Second)},
		Media: stubMedia{stats: rtp.Stats{
			Received:   10,
			Forwarded:  8,
			DroppedACL: 412,
			LastPacket: last,
		}},
	}).Handler()

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var status statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid status json: %v", err)
	}
	if status.LastRTPms == nil || *status.LastRTPms != 14 {
		t.Fatalf("expected last_rtp_ms=14, got %#v", status.LastRTPms)
	}
	if status.PacketsDropped != 412 {
		t.Fatalf("expected packets_dropped_acl=412, got %d", status.PacketsDropped)
	}
	if status.PacketsFwd != 8 || status.PacketsRecv != 10 {
		t.Fatalf("expected forwarded/received counters, got %#v", status)
	}
	if _, ok := mustJSONMap(t, recorder.Body.Bytes())["resume_token"]; ok {
		t.Fatal("status must not include secrets")
	}
}

func TestStatusIncludesReceiverRestarts(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now.Add(time.Second)},
		Media:   stubMedia{restarts: 3},
	}).Handler()

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var status statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid status json: %v", err)
	}
	if status.ReceiverRestarts != 3 {
		t.Fatalf("expected receiver_restarts=3, got %d", status.ReceiverRestarts)
	}
	body := mustJSONMap(t, recorder.Body.Bytes())
	if _, ok := body["resume_token"]; ok {
		t.Fatal("status must not include secrets")
	}
}

func TestStatusRequestKeyframeWhenLastPacketStale(t *testing.T) {
	session, now := newTestSession(t)
	last := now.Add(time.Second - 500*time.Millisecond)
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now.Add(time.Second)},
		Media:   stubMedia{stats: rtp.Stats{LastPacket: last}},
	}).Handler()

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var status statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid status json: %v", err)
	}
	if status.LastRTPms == nil || *status.LastRTPms != 500 {
		t.Fatalf("expected last_rtp_ms=500, got %#v", status.LastRTPms)
	}
	if !status.RequestKeyframe {
		t.Fatalf("expected request_keyframe true when last packet is older than 400ms, got %#v", status)
	}
}

func TestStatusRequestKeyframeClearedWhenPacketRecent(t *testing.T) {
	session, now := newTestSession(t)
	last := now.Add(time.Second - 100*time.Millisecond)
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now.Add(time.Second)},
		Media:   stubMedia{stats: rtp.Stats{LastPacket: last}},
	}).Handler()

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var status statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid status json: %v", err)
	}
	if status.RequestKeyframe {
		t.Fatal("expected request_keyframe false when last_rtp_ms <= 400")
	}
	if _, ok := mustJSONMap(t, recorder.Body.Bytes())["request_keyframe"]; ok {
		t.Fatal("request_keyframe should be omitted when a packet was just forwarded")
	}
}

func TestStatusRequestKeyframeFalseWhenNoPacket(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now.Add(time.Second)},
		Media:   stubMedia{stats: rtp.Stats{}},
	}).Handler()

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var status statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("invalid status json: %v", err)
	}
	if status.RequestKeyframe {
		t.Fatal("expected request_keyframe false when LastPacket is zero")
	}
}

func mustJSONMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("json map: %v", err)
	}
	return body
}

func TestApproveRejectsNonLocalRequest(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	recorder := postJSON(server, "/approve", approveRequest{SessionID: session.Payload().SessionID})

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func newTestSession(t *testing.T) (*pairing.Session, time.Time) {
	t.Helper()

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	session, err := pairing.New(pairing.Config{
		ControlURL: "http://192.168.1.42:49321",
		RTPHost:    "192.168.1.42",
		RTPPort:    49322,
		Now:        now,
	})
	if err != nil {
		t.Fatalf("pairing.New failed: %v", err)
	}
	return session, now
}

func postJSON(handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	return postJSONFrom(handler, path, value, "192.168.1.50:40000")
}

func postLocalJSON(handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	return postJSONFrom(handler, path, value, "127.0.0.1:40000")
}

func postJSONFrom(handler http.Handler, path string, value any, remoteAddr string) *httptest.ResponseRecorder {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}

	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	request.RemoteAddr = remoteAddr
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func getStatusFrom(handler http.Handler, remoteAddr string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.RemoteAddr = remoteAddr
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestPairAutoApproveReturnsSecretsOn202(t *testing.T) {
	session, now := newTestSession(t)
	media := &recordingMedia{}
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		Media:       media,
		AutoApprove: true,
	}).Handler()

	recorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
		Video:     &pairing.VideoProfile{Width: 640, Height: 360, FPS: 30},
		Camera:    "back",
	})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !session.IsApproved() {
		t.Fatal("expected auto-approve to approve the session")
	}

	body := mustJSONMap(t, recorder.Body.Bytes())
	if approved, _ := body["approved"].(bool); !approved {
		t.Fatalf("expected approved=true, got %#v", body)
	}
	resume, _ := body["resume_token"].(string)
	raw, err := base64.RawURLEncoding.DecodeString(resume)
	if err != nil || len(raw) != pairing.TokenBytes {
		t.Fatalf("expected 256-bit resume_token, got %q (%v)", resume, err)
	}
	if _, ok := body["pairing_secret"]; ok {
		t.Fatal("pairing_secret must be omitted without a trust store")
	}
	if media.restartCount() != 1 {
		t.Fatalf("auto-approve pair should stash via RestartReceiver once, got %d", media.restartCount())
	}
	allow, ok := media.lastAllow()
	if !ok || allow.Port != 50000 || allow.SSRC != 1234 {
		t.Fatalf("expected SetAllow of paired source, got %#v", allow)
	}

	status := getStatusFrom(server, "192.168.1.50:40000")
	if _, ok := mustJSONMap(t, status.Body.Bytes())["resume_token"]; ok {
		t.Fatal("auto-approve /status must not repeat secrets")
	}
}

func TestPairAlwaysAcceptedWhenAutoApprove(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		AutoApprove: true,
	}).Handler()

	recorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST /pair must stay 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestReconnectSamePhoneRebind(t *testing.T) {
	session, now := newTestSession(t)
	media := &recordingMedia{}
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		Media:       media,
		AutoApprove: true,
	}).Handler()

	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	resume := mustJSONMap(t, pairRec.Body.Bytes())["resume_token"].(string)

	recorder := postJSONFrom(server, "/reconnect", reconnectRequest{
		Phone:       pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:     51000,
		SSRC:        99,
		ResumeToken: resume,
		Video:       &pairing.VideoProfile{Width: 640, Height: 360, FPS: 30},
		Camera:      "front",
	}, "192.168.1.60:40000")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var got reconnectResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("reconnect json: %v", err)
	}
	if !got.OK || !got.Approved || got.ResumeToken != resume {
		t.Fatalf("expected echoed live resume_token, got %#v", got)
	}
	if got.Session != session.Payload().SessionID {
		t.Fatalf("unexpected session %q", got.Session)
	}
	if got.Control != session.Payload().Control || got.RTP != session.Payload().RTP {
		t.Fatalf("expected control/rtp echo, got %#v", got)
	}
	if got.Video != (pairing.VideoProfile{Width: 640, Height: 360, FPS: 30}) {
		t.Fatalf("expected rebound video, got %#v", got.Video)
	}

	src, ok := session.ApprovedSource()
	if !ok || src.Port != 51000 || src.SSRC != 99 || !src.IP.Equal(net.ParseIP("192.168.1.60")) {
		t.Fatalf("expected rebound source, got %#v", src)
	}
	allow, ok := media.lastAllow()
	if !ok || allow.Port != 51000 || allow.SSRC != 99 {
		t.Fatalf("expected SetAllow after rebind, got %#v", allow)
	}
	if media.restartCount() != 2 {
		t.Fatalf("expected RestartReceiver on pair and reconnect, got %d", media.restartCount())
	}

	status := getStatusFrom(server, "192.168.1.60:40000")
	statusBody := mustJSONMap(t, status.Body.Bytes())
	if statusBody["camera"] != "front" {
		t.Fatalf("expected camera echo, got %#v", statusBody)
	}
	if _, ok := statusBody["resume_token"]; ok {
		t.Fatal("GET /status must not include standing secrets")
	}
}

func TestReconnectRejectsBadResumeToken(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		AutoApprove: true,
	}).Handler()
	postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})

	recorder := postJSON(server, "/reconnect", reconnectRequest{
		Phone:       pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:     51000,
		SSRC:        99,
		ResumeToken: "not-the-token",
	})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if mustJSONMap(t, recorder.Body.Bytes())["error"] != "unauthorized" {
		t.Fatalf("expected generic unauthorized, got %s", recorder.Body.String())
	}
}

func TestReconnectPairingSecretOnlyUnauthorized(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		AutoApprove: true,
	}).Handler()
	postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})

	recorder := postJSON(server, "/reconnect", reconnectRequest{
		Phone:         pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:       51000,
		SSRC:          99,
		PairingSecret: "stored-secret",
	})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without resume_token, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestReconnectDifferentPhoneConflict(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		AutoApprove: true,
	}).Handler()
	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	resume := mustJSONMap(t, pairRec.Body.Bytes())["resume_token"].(string)

	recorder := postJSON(server, "/reconnect", reconnectRequest{
		Phone:       pairing.Phone{ID: "phone-2", Name: "Other"},
		RTPPort:     51000,
		SSRC:        99,
		ResumeToken: resume,
	})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestStatusOneShotSecretsToApprovedIPOnly(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{Session: session, Clock: fakeClock{now: now.Add(time.Second)}}).Handler()

	postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if rec := postLocalJSON(server, "/approve", approveRequest{SessionID: session.Payload().SessionID}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}

	loopback := getStatusFrom(server, "127.0.0.1:9")
	if _, ok := mustJSONMap(t, loopback.Body.Bytes())["resume_token"]; ok {
		t.Fatal("loopback /status must never include secrets")
	}

	other := getStatusFrom(server, "192.168.1.51:40000")
	if _, ok := mustJSONMap(t, other.Body.Bytes())["resume_token"]; ok {
		t.Fatal("non-approved IP must not receive secrets")
	}

	first := getStatusFrom(server, "192.168.1.50:40000")
	resume, _ := mustJSONMap(t, first.Body.Bytes())["resume_token"].(string)
	if resume == "" {
		t.Fatalf("expected one-shot resume_token for approved IP, got %s", first.Body.String())
	}
	if _, ok := mustJSONMap(t, first.Body.Bytes())["pairing_secret"]; ok {
		t.Fatal("pairing_secret must be omitted without a trust store")
	}

	second := getStatusFrom(server, "192.168.1.50:40000")
	resume2, _ := mustJSONMap(t, second.Body.Bytes())["resume_token"].(string)
	if resume2 != resume {
		t.Fatalf("expected retained resume_token on retry before media, got %v", resume2)
	}
}

func TestStatusOneShotIncludesPairingSecretWhenStoreConfigured(t *testing.T) {
	session, now := newTestSession(t)
	store := &memTrust{}
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now.Add(time.Second)},
		Trust:   store,
	}).Handler()

	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if pairRec.Code != http.StatusAccepted {
		t.Fatalf("pair: %d %s", pairRec.Code, pairRec.Body.String())
	}
	if _, ok := mustJSONMap(t, pairRec.Body.Bytes())["pairing_secret"]; ok {
		t.Fatal("require-approval 202 must not include pairing_secret")
	}

	if rec := postLocalJSON(server, "/approve", approveRequest{SessionID: session.Payload().SessionID}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}

	first := getStatusFrom(server, "192.168.1.50:40000")
	body := mustJSONMap(t, first.Body.Bytes())
	resume, _ := body["resume_token"].(string)
	secret, _ := body["pairing_secret"].(string)
	if resume == "" || secret == "" {
		t.Fatalf("expected one-shot resume_token and pairing_secret, got %s", first.Body.String())
	}
	if _, ok := store.LookupBySecret("phone-1", secret); !ok {
		t.Fatal("one-shot pairing_secret must match the store")
	}

	second := getStatusFrom(server, "192.168.1.50:40000")
	body2 := mustJSONMap(t, second.Body.Bytes())
	if body2["pairing_secret"] != secret {
		t.Fatalf("expected retained pairing_secret on retry before media, got %v", body2["pairing_secret"])
	}
}

func TestReconnectRateLimit(t *testing.T) {
	session, now := newTestSession(t)
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		AutoApprove: true,
	}).Handler()
	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	resume := mustJSONMap(t, pairRec.Body.Bytes())["resume_token"].(string)
	req := reconnectRequest{
		Phone:       pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:     51000,
		SSRC:        99,
		ResumeToken: resume,
	}

	var last *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		last = postJSONFrom(server, "/reconnect", req, "192.168.1.50:40000")
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after 5 req/s, got %d: %s", last.Code, last.Body.String())
	}

	session2, now2 := newTestSession(t)
	server2 := New(Config{
		Session:     session2,
		Clock:       fakeClock{now: now2.Add(time.Second)},
		AutoApprove: true,
	}).Handler()
	pair2 := postJSON(server2, "/pair", pairRequest{
		SessionID: session2.Payload().SessionID,
		Token:     session2.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	resume2 := mustJSONMap(t, pair2.Body.Bytes())["resume_token"].(string)
	req2 := reconnectRequest{
		Phone:       pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:     51000,
		SSRC:        99,
		ResumeToken: resume2,
	}
	var lastPhone *httptest.ResponseRecorder
	for i := 0; i < 21; i++ {
		lastPhone = postJSONFrom(server2, "/reconnect", req2, fmt.Sprintf("10.0.0.%d:40000", i+1))
	}
	if lastPhone.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after 20/min/phone, got %d: %s", lastPhone.Code, lastPhone.Body.String())
	}
}

func TestPairAutoApproveIncludesPairingSecretWhenStoreConfigured(t *testing.T) {
	session, now := newTestSession(t)
	store := &memTrust{}
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		Media:       &recordingMedia{},
		AutoApprove: true,
		Trust:       store,
	}).Handler()

	recorder := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := mustJSONMap(t, recorder.Body.Bytes())
	secret, _ := body["pairing_secret"].(string)
	if secret == "" {
		t.Fatalf("pair 202 must include pairing_secret when store is configured: %s", recorder.Body.String())
	}
	if store.Count() != 1 {
		t.Fatalf("expected persisted phone, got %d", store.Count())
	}
	if _, ok := store.LookupBySecret("phone-1", secret); !ok {
		t.Fatal("stored secret should match the 202 body")
	}
}

func TestReconnectPairingSecretPath(t *testing.T) {
	session, now := newTestSession(t)
	store := &memTrust{}
	secret, err := store.Upsert("phone-1", "Pixel", now)
	if err != nil {
		t.Fatal(err)
	}
	media := &recordingMedia{}
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now.Add(pairing.DefaultTTL + time.Minute)},
		Media:   media,
		Trust:   store,
	}).Handler()

	// QR is unused and past TTL; pairing_secret must still work.
	recorder := postJSON(server, "/reconnect", reconnectRequest{
		Phone:         pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:       51000,
		SSRC:          99,
		PairingSecret: secret,
		Video:         &pairing.VideoProfile{Width: 640, Height: 360, FPS: 30},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var got reconnectResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !got.Approved || got.ResumeToken == "" {
		t.Fatalf("expected minted resume_token, got %#v", got)
	}
	firstToken := got.ResumeToken
	if !session.IsApproved() {
		t.Fatal("ApproveTrusted should approve")
	}
	if _, ok := session.PendingPhone(); ok {
		t.Fatal("trusted reconnect must not leave PendingPhone")
	}

	// leftover QR is consumed
	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if pairRec.Code != http.StatusUnauthorized {
		t.Fatalf("leftover QR should be consumed, got %d: %s", pairRec.Code, pairRec.Body.String())
	}

	// same-id rebind echoes the token
	again := postJSON(server, "/reconnect", reconnectRequest{
		Phone:         pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:       52000,
		SSRC:          100,
		PairingSecret: secret,
	})
	if again.Code != http.StatusOK {
		t.Fatalf("same-id rebind: %d %s", again.Code, again.Body.String())
	}
	if mustJSONMap(t, again.Body.Bytes())["resume_token"] != firstToken {
		t.Fatal("same-id rebind must echo the live resume_token")
	}
}

func TestReconnectResumeTokenPreferredOverPairingSecret(t *testing.T) {
	session, now := newTestSession(t)
	store := &memTrust{}
	secret, err := store.Upsert("phone-1", "Pixel", now)
	if err != nil {
		t.Fatal(err)
	}
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		Media:       &recordingMedia{},
		AutoApprove: true,
		Trust:       store,
	}).Handler()
	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	resume := mustJSONMap(t, pairRec.Body.Bytes())["resume_token"].(string)

	recorder := postJSON(server, "/reconnect", reconnectRequest{
		Phone:         pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:       51000,
		SSRC:          99,
		ResumeToken:   resume,
		PairingSecret: secret,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if mustJSONMap(t, recorder.Body.Bytes())["resume_token"] != resume {
		t.Fatal("resume_token path must be preferred and echo the live token")
	}
}

func TestReconnectPairingSecretDifferentPhoneConflict(t *testing.T) {
	session, now := newTestSession(t)
	store := &memTrust{}
	if _, err := store.Upsert("phone-1", "Pixel", now); err != nil {
		t.Fatal(err)
	}
	secret2, err := store.Upsert("phone-2", "Other", now)
	if err != nil {
		t.Fatal(err)
	}
	server := New(Config{
		Session:     session,
		Clock:       fakeClock{now: now.Add(time.Second)},
		Media:       &recordingMedia{},
		AutoApprove: true,
		Trust:       store,
	}).Handler()
	postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})

	recorder := postJSON(server, "/reconnect", reconnectRequest{
		Phone:         pairing.Phone{ID: "phone-2", Name: "Other"},
		RTPPort:       51000,
		SSRC:          99,
		PairingSecret: secret2,
	})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestTrustListOmitsSecretsAndRejectsNonLoopback(t *testing.T) {
	session, now := newTestSession(t)
	store := &memTrust{}
	if _, err := store.Upsert("phone-1", "Pixel", now); err != nil {
		t.Fatal(err)
	}
	server := New(Config{Session: session, Clock: fakeClock{now: now}, Trust: store}).Handler()

	remote := httptest.NewRequest(http.MethodGet, "/trust", nil)
	remote.RemoteAddr = "192.168.1.50:9"
	off := httptest.NewRecorder()
	server.ServeHTTP(off, remote)
	if off.Code != http.StatusForbidden {
		t.Fatalf("expected 403 off-loopback, got %d: %s", off.Code, off.Body.String())
	}

	local := httptest.NewRequest(http.MethodGet, "/trust", nil)
	local.RemoteAddr = "127.0.0.1:9"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, local)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := mustJSONMap(t, rec.Body.Bytes())
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "pairing") {
		t.Fatalf("GET /trust must not include secrets: %s", raw)
	}
	phones, _ := body["phones"].([]any)
	if len(phones) != 1 {
		t.Fatalf("expected 1 phone, got %#v", body)
	}
	entry := phones[0].(map[string]any)
	if entry["id"] != "phone-1" || entry["name"] != "Pixel" {
		t.Fatalf("unexpected entry %#v", entry)
	}
	if _, ok := entry["secret"]; ok {
		t.Fatal("secret field must be absent")
	}
}

func TestTrustDeleteLoopbackOnly(t *testing.T) {
	session, now := newTestSession(t)
	store := &memTrust{}
	if _, err := store.Upsert("phone-1", "Pixel", now); err != nil {
		t.Fatal(err)
	}
	server := New(Config{Session: session, Clock: fakeClock{now: now}, Trust: store}).Handler()

	off := httptest.NewRequest(http.MethodDelete, "/trust/phone-1", nil)
	off.RemoteAddr = "192.168.1.50:9"
	offRec := httptest.NewRecorder()
	server.ServeHTTP(offRec, off)
	if offRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", offRec.Code)
	}
	if store.Count() != 1 {
		t.Fatal("off-loopback delete must not revoke")
	}

	local := httptest.NewRequest(http.MethodDelete, "/trust/phone-1", nil)
	local.RemoteAddr = "127.0.0.1:9"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, local)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.Count() != 0 {
		t.Fatal("loopback delete should revoke")
	}
}

func TestTrustDeleteInvalidatesActiveSession(t *testing.T) {
	session, now := newTestSession(t)
	store := &memTrust{}
	if _, err := store.Upsert("phone-1", "Pixel", now); err != nil {
		t.Fatal(err)
	}
	media := &recordingMedia{}
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now},
		Trust:   store,
		Media:   media,
	}).Handler()

	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if pairRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", pairRec.Code)
	}
	appRec := postLocalJSON(server, "/approve", approveRequest{SessionID: session.Payload().SessionID})
	if appRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", appRec.Code)
	}

	local := httptest.NewRequest(http.MethodDelete, "/trust/phone-1", nil)
	local.RemoteAddr = "127.0.0.1:9"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, local)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if session.IsApproved() {
		t.Fatal("expected session not approved when active trusted phone was deleted")
	}
	allow, ok := media.lastAllow()
	if !ok || len(allow.IP) != 0 || allow.Port != 0 {
		t.Fatalf("expected media allow reset to empty source, got %#v", allow)
	}
}

func TestTrustDeleteInvalidatesPendingSession(t *testing.T) {
	session, now := newTestSession(t)
	media := &recordingMedia{}
	store := &memTrust{}
	if _, err := store.Upsert("phone-1", "Pixel", now); err != nil {
		t.Fatal(err)
	}

	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now},
		Media:   media,
		Trust:   store,
	}).Handler()

	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if pairRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", pairRec.Code)
	}
	if _, ok := session.PendingPhone(); !ok {
		t.Fatal("expected pending phone before approval")
	}

	local := httptest.NewRequest(http.MethodDelete, "/trust/phone-1", nil)
	local.RemoteAddr = "127.0.0.1:9"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, local)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := session.PendingPhone(); ok {
		t.Fatal("expected pending phone cleared when trust deleted")
	}
}

func TestHandleLeaveResetsPairing(t *testing.T) {
	session, now := newTestSession(t)
	media := &recordingMedia{}
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now},
		Media:   media,
	}).Handler()

	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if pairRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", pairRec.Code)
	}
	appRec := postLocalJSON(server, "/approve", approveRequest{SessionID: session.Payload().SessionID})
	if appRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", appRec.Code)
	}

	leaveReq := httptest.NewRequest(http.MethodPost, "/leave", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, leaveReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if session.IsApproved() {
		t.Fatal("expected session not approved after leave")
	}
}

func TestStatusSecretsRetainedUntilMediaPackets(t *testing.T) {
	session, now := newTestSession(t)
	media := &recordingMedia{}
	server := New(Config{
		Session: session,
		Clock:   fakeClock{now: now},
		Media:   media,
	}).Handler()

	pairRec := postJSON(server, "/pair", pairRequest{
		SessionID: session.Payload().SessionID,
		Token:     session.Payload().Token,
		Phone:     pairing.Phone{ID: "phone-1", Name: "Pixel"},
		RTPPort:   50000,
		SSRC:      1234,
	})
	if pairRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", pairRec.Code)
	}
	appRec := postLocalJSON(server, "/approve", approveRequest{SessionID: session.Payload().SessionID})
	if appRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", appRec.Code)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/status", nil)
	statusReq.RemoteAddr = "192.168.1.50:9999"

	rec1 := httptest.NewRecorder()
	server.ServeHTTP(rec1, statusReq)
	var body1 map[string]any
	if err := json.Unmarshal(rec1.Body.Bytes(), &body1); err != nil {
		t.Fatal(err)
	}
	if body1["resume_token"] == nil || body1["resume_token"] == "" {
		t.Fatal("expected resume_token in first status")
	}

	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, statusReq)
	var body2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &body2); err != nil {
		t.Fatal(err)
	}
	if body2["resume_token"] != body1["resume_token"] {
		t.Fatalf("expected same resume_token in second status, got %v", body2["resume_token"])
	}

	media.mu.Lock()
	media.stats.Forwarded = 1
	media.mu.Unlock()

	rec3 := httptest.NewRecorder()
	server.ServeHTTP(rec3, statusReq)
	var body3 map[string]any
	if err := json.Unmarshal(rec3.Body.Bytes(), &body3); err != nil {
		t.Fatal(err)
	}
	if body3["resume_token"] != nil && body3["resume_token"] != "" {
		t.Fatalf("expected resume_token to be cleared after RTP packets received, got %v", body3["resume_token"])
	}
}
