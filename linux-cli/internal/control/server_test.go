package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
)

type fakeClock struct {
	now time.Time
}

func (f fakeClock) Now() time.Time {
	return f.now
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
