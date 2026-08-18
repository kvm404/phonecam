package pairing

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

func TestNewBuildsVersionedPayload(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})

	payload := session.Payload()
	if payload.Version != ProtocolVersion {
		t.Fatalf("expected protocol version %d, got %d", ProtocolVersion, payload.Version)
	}
	if payload.Name != "phonecam-linux" {
		t.Fatalf("expected default laptop name, got %q", payload.Name)
	}
	if payload.Control != "http://192.168.1.42:49321" {
		t.Fatalf("unexpected control URL: %q", payload.Control)
	}
	if payload.RTP != "192.168.1.42:49322" {
		t.Fatalf("unexpected RTP endpoint: %q", payload.RTP)
	}
	if payload.Transport != "rtp-h264" {
		t.Fatalf("unexpected transport: %q", payload.Transport)
	}
	if payload.Video != (VideoProfile{Width: 1280, Height: 720, FPS: 30}) {
		t.Fatalf("unexpected video profile: %#v", payload.Video)
	}
	if !payload.Expires.Equal(now.Add(DefaultTTL)) {
		t.Fatalf("unexpected expiry: %s", payload.Expires)
	}
}

func TestPayloadJSONIncludesLaptopIDNotPairingSecret(t *testing.T) {
	session := newTestSession(t, Config{LaptopID: "laptop-abc"})

	data, err := session.PayloadJSON()
	if err != nil {
		t.Fatalf("PayloadJSON failed: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if raw["laptop_id"] != "laptop-abc" {
		t.Fatalf("expected laptop_id in payload, got %#v", raw)
	}
	if raw["v"] != float64(ProtocolVersion) {
		t.Fatalf("protocol version must stay 1, got %#v", raw["v"])
	}
	if _, ok := raw["pairing_secret"]; ok {
		t.Fatal("PayloadJSON must not include pairing_secret")
	}
	if _, ok := raw["resume_token"]; ok {
		t.Fatal("PayloadJSON must not include resume_token")
	}
}

func TestPayloadJSONIsScannableShape(t *testing.T) {
	session := newTestSession(t, Config{})

	data, err := session.PayloadJSON()
	if err != nil {
		t.Fatalf("PayloadJSON failed: %v", err)
	}

	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("payload JSON did not unmarshal: %v", err)
	}
	if payload.SessionID == "" || payload.Token == "" {
		t.Fatalf("expected session id and token in payload: %s", string(data))
	}
}

func TestTokenHasAtLeast128BitsOfEntropy(t *testing.T) {
	session := newTestSession(t, Config{})
	token := session.Payload().Token

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not base64url: %v", err)
	}
	if len(raw) < 16 {
		t.Fatalf("expected at least 16 token bytes, got %d", len(raw))
	}
}

func TestConsumeTokenRejectsInvalidExpiredAndReplay(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})

	if err := session.ConsumeToken(tokenRequest(session, "wrong"), now); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected invalid token error, got %v", err)
	}
	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(DefaultTTL)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expected expired token error, got %v", err)
	}

	session = newTestSession(t, Config{Now: now})
	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(2*time.Second)); !errors.Is(err, ErrTokenConsumed) {
		t.Fatalf("expected consumed token error, got %v", err)
	}
}

func TestConsumeTokenRejectsWrongSessionID(t *testing.T) {
	session := newTestSession(t, Config{})
	request := tokenRequest(session, session.Payload().Token)
	request.SessionID = "wrong-session"

	if err := session.ConsumeToken(request, time.Date(2026, 7, 1, 10, 0, 1, 0, time.UTC)); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("expected invalid session id, got %v", err)
	}
}

func TestApprovalRequiresConsumedToken(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})

	if err := session.Approve(now.Add(time.Second)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected approval to require consumed token, got %v", err)
	}

	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	if err := session.Approve(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("expected approval to succeed: %v", err)
	}
	if !session.IsApproved() {
		t.Fatal("expected approved session")
	}
	if session.ApprovedPhone().Name != "Pixel" {
		t.Fatalf("expected approved phone to be stored, got %#v", session.ApprovedPhone())
	}
}

func TestApproveMintsResumeToken(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})
	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	if err := session.Approve(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("expected approval to succeed: %v", err)
	}
	token := session.ResumeToken()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("resume token is not base64url: %v", err)
	}
	if len(raw) != TokenBytes {
		t.Fatalf("expected %d resume token bytes, got %d", TokenBytes, len(raw))
	}
	if err := session.Approve(now.Add(3 * time.Second)); err != nil {
		t.Fatalf("expected re-approve to be idempotent: %v", err)
	}
	if session.ResumeToken() != token {
		t.Fatal("re-approve must not rotate resume_token")
	}
}

func TestTakeSecretsIsOneShotAndOmittedFromPayload(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})
	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	if err := session.Approve(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("expected approval to succeed: %v", err)
	}

	data, err := session.PayloadJSON()
	if err != nil {
		t.Fatalf("PayloadJSON failed: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if _, ok := raw["resume_token"]; ok {
		t.Fatal("PayloadJSON must not include resume_token")
	}
	if _, ok := raw["pairing_secret"]; ok {
		t.Fatal("PayloadJSON must not include pairing_secret")
	}

	resume, pairing, ok := session.TakeSecrets()
	if !ok || resume == "" {
		t.Fatalf("expected one-shot resume secret, got resume=%q ok=%v", resume, ok)
	}
	if pairing != "" {
		t.Fatalf("pairing secret must be empty until SetPairingSecret, got %q", pairing)
	}

	session.SetPairingSecret("stored-pairing-secret")
	// already taken; setting a secret later must not re-open the one-shot
	if _, _, ok := session.TakeSecrets(); ok {
		t.Fatal("TakeSecrets must stay one-shot after SetPairingSecret")
	}
	if _, _, ok := session.TakeSecrets(); ok {
		t.Fatal("TakeSecrets must be one-shot")
	}
}

func TestTakeSecretsReturnsPairingSecretWhenSet(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now, LaptopID: "lid"})
	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if err := session.Approve(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	session.SetPairingSecret("pairing-secret-value")
	resume, pairing, ok := session.TakeSecrets()
	if !ok || resume == "" || pairing != "pairing-secret-value" {
		t.Fatalf("expected both secrets, got resume=%q pairing=%q ok=%v", resume, pairing, ok)
	}
}

func TestApproveTrustedIgnoresTTLAndDoesNotInvalidate(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})
	expired := now.Add(DefaultTTL + time.Minute)
	source := RTPSource{IP: net.ParseIP("192.168.1.60"), Port: 51000, SSRC: 99}
	phone := Phone{ID: "phone-1", Name: "Pixel"}
	video := VideoProfile{Width: 640, Height: 360, FPS: 30}

	if err := session.ApproveTrusted(phone, source, &video); err != nil {
		t.Fatalf("ApproveTrusted after QR expiry must succeed, got %v", err)
	}
	if !session.IsApproved() {
		t.Fatal("expected approved session")
	}
	if _, ok := session.PendingPhone(); ok {
		t.Fatal("ApproveTrusted must not leave a pending phone")
	}
	if got := session.NegotiatedVideo(); got != video {
		t.Fatalf("expected negotiated video, got %#v", got)
	}
	got, ok := session.ApprovedSource()
	if !ok || !got.IP.Equal(source.IP) || got.Port != source.Port || got.SSRC != source.SSRC {
		t.Fatalf("expected pinned source, got %#v", got)
	}
	if err := session.ValidateRTPSource(source); err != nil {
		t.Fatalf("rtpSource should be bound: %v", err)
	}

	token := session.Payload().Token
	if err := session.ConsumeToken(tokenRequest(session, token), expired); !errors.Is(err, ErrTokenConsumed) {
		t.Fatalf("leftover QR must be consumed, got %v", err)
	}
	if !session.IsApproved() {
		t.Fatal("ApproveTrusted must not Invalidate the live session")
	}
	live := session.ResumeToken()
	if live == "" {
		t.Fatal("expected minted resume_token")
	}

	// Same id rebind echoes the token and does not mint.
	next := RTPSource{IP: net.ParseIP("192.168.1.61"), Port: 52000, SSRC: 100}
	if err := session.ApproveTrusted(phone, next, nil); err != nil {
		t.Fatalf("same-id ApproveTrusted: %v", err)
	}
	if session.ResumeToken() != live {
		t.Fatal("same-id rebind must echo the existing resume_token")
	}
	rebound, ok := session.ApprovedSource()
	if !ok || rebound.Port != 52000 || rebound.SSRC != 100 {
		t.Fatalf("expected rebound source, got %#v", rebound)
	}

	other := Phone{ID: "phone-2", Name: "Other"}
	if err := session.ApproveTrusted(other, next, nil); !errors.Is(err, ErrDifferentPhone) {
		t.Fatalf("expected ErrDifferentPhone, got %v", err)
	}
}

func TestApproveTrustedDoesNotRotatePairingSecret(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})
	session.SetPairingSecret("keep-me")
	source := RTPSource{IP: net.ParseIP("192.168.1.60"), Port: 51000, SSRC: 99}
	if err := session.ApproveTrusted(Phone{ID: "p", Name: "N"}, source, nil); err != nil {
		t.Fatalf("ApproveTrusted: %v", err)
	}
	_, pairing, ok := session.TakeSecrets()
	if !ok || pairing != "keep-me" {
		t.Fatalf("pairing_secret must not rotate on ApproveTrusted, got %q ok=%v", pairing, ok)
	}
}

func TestRebindRTPSourceReplacesPin(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})
	original := RTPSource{IP: net.ParseIP("192.168.1.50"), Port: 50000, SSRC: 1234}
	if err := session.RebindRTPSource(original); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("expected rebind to require approval, got %v", err)
	}

	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	if err := session.Approve(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("expected approval to succeed: %v", err)
	}
	if err := session.BindRTPSource(original); err != nil {
		t.Fatalf("expected first bind to succeed: %v", err)
	}

	next := RTPSource{IP: net.ParseIP("192.168.1.60"), Port: 50001, SSRC: 99}
	if err := session.RebindRTPSource(next); err != nil {
		t.Fatalf("expected rebind to succeed: %v", err)
	}
	got, ok := session.ApprovedSource()
	if !ok || !got.IP.Equal(next.IP) || got.Port != next.Port || got.SSRC != next.SSRC {
		t.Fatalf("approved source after rebind %#v, want %#v", got, next)
	}
	if err := session.ValidateRTPSource(next); err != nil {
		t.Fatalf("expected rebound source to validate: %v", err)
	}
	if err := session.BindRTPSource(next); err != nil {
		t.Fatalf("expected same-source bind after rebind, got %v", err)
	}
	if err := session.BindRTPSource(original); !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("expected old source to be already bound, got %v", err)
	}

	live := session.ResumeToken()
	session.Invalidate()
	if err := session.RebindRTPSource(next); !errors.Is(err, ErrInvalidated) {
		t.Fatalf("expected invalidated rebind to fail, got %v", err)
	}
	if session.MatchResumeToken(live) {
		t.Fatal("invalidated session must not match a resume token")
	}
}

func TestPendingPhoneReflectsSessionLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})

	if _, ok := session.PendingPhone(); ok {
		t.Fatal("expected no pending phone before pairing")
	}

	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	phone, ok := session.PendingPhone()
	if !ok {
		t.Fatal("expected pending phone after token consumption")
	}
	if phone.Name != "Pixel" {
		t.Fatalf("expected pending phone Pixel, got %#v", phone)
	}

	if err := session.Approve(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("expected approval to succeed: %v", err)
	}
	if _, ok := session.PendingPhone(); ok {
		t.Fatal("expected no pending phone after approval")
	}
}

func TestPendingPhoneFalseAfterInvalidate(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})

	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	session.Invalidate()
	if _, ok := session.PendingPhone(); ok {
		t.Fatal("expected no pending phone after invalidate")
	}
}

func TestApprovalRejectsExpiredSessionAfterTokenConsumption(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})

	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	if err := session.Approve(now.Add(DefaultTTL)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expected expired approval to fail, got %v", err)
	}
}

func TestRTPBindingRequiresApprovalAndValidatesSource(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})
	source := RTPSource{IP: net.ParseIP("192.168.1.50"), Port: 50000, SSRC: 1234}
	if _, ok := session.ApprovedSource(); ok {
		t.Fatal("expected no approved source before approval")
	}

	if err := session.BindRTPSource(source); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("expected approval requirement, got %v", err)
	}

	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	if err := session.Approve(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("expected approval to succeed: %v", err)
	}
	got, ok := session.ApprovedSource()
	if !ok {
		t.Fatal("expected approved source after approval")
	}
	if !got.IP.Equal(source.IP) || got.Port != source.Port || got.SSRC != source.SSRC {
		t.Fatalf("approved source %#v, want %#v", got, source)
	}

	if err := session.BindRTPSource(source); err != nil {
		t.Fatalf("expected RTP binding to succeed: %v", err)
	}
	if err := session.ValidateRTPSource(source); err != nil {
		t.Fatalf("expected matching RTP source to validate: %v", err)
	}
	if err := session.ValidateRTPSource(RTPSource{IP: net.ParseIP("192.168.1.51"), Port: 50000, SSRC: 1234}); !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("expected source mismatch, got %v", err)
	}
	if err := session.BindRTPSource(source); err != nil {
		t.Fatalf("expected same-source re-bind to succeed, got %v", err)
	}
	other := RTPSource{IP: net.ParseIP("192.168.1.50"), Port: 50001, SSRC: 1234}
	if err := session.BindRTPSource(other); !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("expected different already-bound source to fail, got %v", err)
	}
}

func TestRTPBindingRejectsRaceFromUnapprovedSource(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})

	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	if err := session.Approve(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("expected approval to succeed: %v", err)
	}

	attacker := RTPSource{IP: net.ParseIP("192.168.1.51"), Port: 50000, SSRC: 1234}
	if err := session.BindRTPSource(attacker); !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("expected attacker source to be rejected, got %v", err)
	}
}

func TestInvalidateRejectsFutureSessionUse(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})
	request := tokenRequest(session, session.Payload().Token)

	session.Invalidate()

	if err := session.ConsumeToken(request, now.Add(time.Second)); !errors.Is(err, ErrInvalidated) {
		t.Fatalf("expected invalidated token consumption to fail, got %v", err)
	}
	if err := session.Approve(now.Add(time.Second)); !errors.Is(err, ErrInvalidated) {
		t.Fatalf("expected invalidated approval to fail, got %v", err)
	}
	if err := session.BindRTPSource(RTPSource{IP: net.ParseIP("192.168.1.50"), Port: 50000, SSRC: 1234}); !errors.Is(err, ErrInvalidated) {
		t.Fatalf("expected invalidated bind to fail, got %v", err)
	}
}

func TestConsumeTokenStoresNegotiatedVideo(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})

	request := tokenRequest(session, session.Payload().Token)
	request.Video = &VideoProfile{Width: 720, Height: 1280, FPS: 24}
	if err := session.ConsumeToken(request, now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}

	if got := session.NegotiatedVideo(); got != (VideoProfile{Width: 720, Height: 1280, FPS: 24}) {
		t.Fatalf("expected stored negotiated video, got %#v", got)
	}
}

func TestNegotiatedVideoFallsBackToAdvertisedProfile(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	session := newTestSession(t, Config{Now: now})

	if got := session.NegotiatedVideo(); got != (VideoProfile{Width: 1280, Height: 720, FPS: 30}) {
		t.Fatalf("expected advertised profile before consumption, got %#v", got)
	}

	if err := session.ConsumeToken(tokenRequest(session, session.Payload().Token), now.Add(time.Second)); err != nil {
		t.Fatalf("expected token consumption to succeed: %v", err)
	}
	if got := session.NegotiatedVideo(); got != (VideoProfile{Width: 1280, Height: 720, FPS: 30}) {
		t.Fatalf("expected advertised profile fallback, got %#v", got)
	}
}

func TestConsumeTokenRejectsInvalidVideo(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	tests := []VideoProfile{
		{Width: 0, Height: 720, FPS: 30},
		{Width: 8, Height: 720, FPS: 30},
		{Width: 5000, Height: 720, FPS: 30},
		{Width: 1280, Height: 0, FPS: 30},
		{Width: 1280, Height: 5000, FPS: 30},
		{Width: 1280, Height: 720, FPS: 0},
		{Width: 1280, Height: 720, FPS: 121},
	}

	for _, video := range tests {
		session := newTestSession(t, Config{Now: now})
		request := tokenRequest(session, session.Payload().Token)
		v := video
		request.Video = &v
		if err := session.ConsumeToken(request, now.Add(time.Second)); !errors.Is(err, ErrInvalidVideo) {
			t.Fatalf("expected invalid video error for %#v, got %v", video, err)
		}
		if session.NegotiatedVideo() != (VideoProfile{Width: 1280, Height: 720, FPS: 30}) {
			t.Fatalf("expected no negotiated video stored on rejection for %#v", video)
		}
	}
}

func TestNewRejectsInvalidEndpoint(t *testing.T) {
	tests := []Config{
		{ControlURL: "not a url", RTPHost: "127.0.0.1", RTPPort: 49322},
		{ControlURL: "ftp://127.0.0.1:49321", RTPHost: "127.0.0.1", RTPPort: 49322},
		{ControlURL: "http://127.0.0.1", RTPHost: "127.0.0.1", RTPPort: 49322},
		{ControlURL: "http://127.0.0.1:49321", RTPHost: "", RTPPort: 49322},
		{ControlURL: "http://127.0.0.1:49321", RTPHost: "127.0.0.1:49322", RTPPort: 49322},
		{ControlURL: "http://127.0.0.1:49321", RTPHost: "127.0.0.1", RTPPort: 65536},
	}

	for _, test := range tests {
		_, err := New(test)
		if !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("expected invalid endpoint error for %#v, got %v", test, err)
		}
	}
}

func TestNewAcceptsIPv6RTPHost(t *testing.T) {
	session := newTestSession(t, Config{
		ControlURL: "http://[2001:db8::1]:49321",
		RTPHost:    "2001:db8::1",
	})

	if session.Payload().RTP != "[2001:db8::1]:49322" {
		t.Fatalf("expected bracketed IPv6 RTP endpoint, got %q", session.Payload().RTP)
	}
}

func newTestSession(t *testing.T, override Config) *Session {
	t.Helper()

	config := Config{
		ControlURL: "http://192.168.1.42:49321",
		RTPHost:    "192.168.1.42",
		RTPPort:    49322,
		Now:        time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
	}
	if override.LaptopName != "" {
		config.LaptopName = override.LaptopName
	}
	if override.LaptopID != "" {
		config.LaptopID = override.LaptopID
	}
	if override.ControlURL != "" {
		config.ControlURL = override.ControlURL
	}
	if override.RTPHost != "" {
		config.RTPHost = override.RTPHost
	}
	if override.RTPPort != 0 {
		config.RTPPort = override.RTPPort
	}
	if override.Width != 0 {
		config.Width = override.Width
	}
	if override.Height != 0 {
		config.Height = override.Height
	}
	if override.FPS != 0 {
		config.FPS = override.FPS
	}
	if override.TTL != 0 {
		config.TTL = override.TTL
	}
	if !override.Now.IsZero() {
		config.Now = override.Now
	}

	session, err := New(config)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	return session
}

func tokenRequest(session *Session, token string) TokenRequest {
	return TokenRequest{
		SessionID: session.Payload().SessionID,
		Token:     token,
		Phone:     Phone{ID: "phone-1", Name: "Pixel"},
		ControlIP: net.ParseIP("192.168.1.50"),
		RTPPort:   50000,
		SSRC:      1234,
	}
}
