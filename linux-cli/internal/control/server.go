package control

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
	"github.com/kvm404/phonecam/linux-cli/internal/rtp"
	"github.com/kvm404/phonecam/linux-cli/internal/trust"
)

// Media lets the control server pin the RTP gate and read its counters.
type Media interface {
	SetAllow(src pairing.RTPSource)
	Stats() rtp.Stats
	RestartReceiver(video pairing.VideoProfile) error
	ReceiverRestarts() int
}

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now().UTC()
}

type Server struct {
	session     *pairing.Session
	clock       Clock
	media       Media
	trust       TrustStore
	mux         *http.ServeMux
	autoApprove bool
	limiter     *reconnectLimiter
	mu          sync.Mutex
	camera      string
}

// TrustStore is the persistent phone list. nil means --no-trust.
type TrustStore interface {
	LookupBySecret(phoneID, secret string) (trust.Phone, bool)
	Upsert(id, name string, now time.Time) (secret string, err error)
	Put(id, name, secret string, now time.Time) error
	List() []trust.PublicPhone
	Revoke(idOrName string) error
	Count() int
	Touch(phoneID string, now time.Time)
}

type Config struct {
	Session     *pairing.Session
	Clock       Clock
	Media       Media
	AutoApprove bool
	Trust       TrustStore
}

type pairRequest struct {
	SessionID string                `json:"session"`
	Token     string                `json:"token"`
	Phone     pairing.Phone         `json:"phone"`
	RTPPort   int                   `json:"rtp_port"`
	SSRC      uint32                `json:"ssrc"`
	Video     *pairing.VideoProfile `json:"video"`
	Camera    string                `json:"camera"`
}

type reconnectRequest struct {
	Phone         pairing.Phone         `json:"phone"`
	RTPPort       int                   `json:"rtp_port"`
	SSRC          uint32                `json:"ssrc"`
	Video         *pairing.VideoProfile `json:"video"`
	ResumeToken   string                `json:"resume_token"`
	PairingSecret string                `json:"pairing_secret"`
	Camera        string                `json:"camera"`
}

type pairResponse struct {
	OK            bool   `json:"ok"`
	Approved      bool   `json:"approved"`
	Session       string `json:"session,omitempty"`
	ResumeToken   string `json:"resume_token,omitempty"`
	PairingSecret string `json:"pairing_secret,omitempty"`
}

type reconnectResponse struct {
	OK          bool                 `json:"ok"`
	Approved    bool                 `json:"approved"`
	Session     string               `json:"session,omitempty"`
	ResumeToken string               `json:"resume_token,omitempty"`
	Control     string               `json:"control,omitempty"`
	RTP         string               `json:"rtp,omitempty"`
	Video       pairing.VideoProfile `json:"video"`
}

type approveRequest struct {
	SessionID string `json:"session"`
}

type leaveRequest struct {
	SessionID     string `json:"session"`
	Token         string `json:"token"`
	ResumeToken   string `json:"resume_token"`
	PairingSecret string `json:"pairing_secret"`
}

type statusResponse struct {
	OK               bool                  `json:"ok"`
	Approved         bool                  `json:"approved"`
	Session          string                `json:"session,omitempty"`
	PhoneName        string                `json:"phone_name,omitempty"`
	PhoneID          string                `json:"phone_id,omitempty"`
	Video            *pairing.VideoProfile `json:"video,omitempty"`
	Camera           string                `json:"camera,omitempty"`
	LastRTPms        *int64                `json:"last_rtp_ms,omitempty"`
	PacketsFwd       uint64                `json:"packets_forwarded,omitempty"`
	PacketsDropped   uint64                `json:"packets_dropped_acl,omitempty"`
	PacketsRecv      uint64                `json:"packets_received,omitempty"`
	ReceiverRestarts int                   `json:"receiver_restarts,omitempty"`
	RequestKeyframe  bool                  `json:"request_keyframe,omitempty"`
	ReconnectReady   bool                  `json:"reconnect_ready,omitempty"`
	TrustedCount     int                   `json:"trusted_count,omitempty"`
	ResumeToken      string                `json:"resume_token,omitempty"`
	PairingSecret    string                `json:"pairing_secret,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func New(config Config) *Server {
	clock := config.Clock
	if clock == nil {
		clock = RealClock{}
	}

	server := &Server{
		session:     config.Session,
		clock:       clock,
		media:       config.Media,
		trust:       config.Trust,
		mux:         http.NewServeMux(),
		autoApprove: config.AutoApprove,
		limiter:     newReconnectLimiter(),
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /pairing", s.handlePairing)
	s.mux.HandleFunc("POST /pair", s.handlePair)
	s.mux.HandleFunc("POST /approve", s.handleApprove)
	s.mux.HandleFunc("POST /reconnect", s.handleReconnect)
	s.mux.HandleFunc("POST /leave", s.handleLeave)
	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("GET /trust", s.handleTrustList)
	s.mux.HandleFunc("DELETE /trust/{id}", s.handleTrustDelete)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePairing(w http.ResponseWriter, r *http.Request) {
	if s.session == nil {
		writeError(w, http.StatusServiceUnavailable, "no active pairing session")
		return
	}
	if !isLoopbackRequest(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "pairing payload is only available locally")
		return
	}
	writeJSON(w, http.StatusOK, s.session.Payload())
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if s.session == nil {
		writeError(w, http.StatusServiceUnavailable, "no active pairing session")
		return
	}

	var request pairRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	remoteIP, err := remoteIP(r.RemoteAddr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid remote address")
		return
	}

	err = s.session.ConsumeToken(pairing.TokenRequest{
		SessionID: request.SessionID,
		Token:     request.Token,
		Phone:     request.Phone,
		ControlIP: remoteIP,
		RTPPort:   request.RTPPort,
		SSRC:      request.SSRC,
		Video:     request.Video,
	}, s.clock.Now())
	if err != nil {
		writePairingError(w, err)
		return
	}
	s.storeCamera(request.Camera)

	resp := pairResponse{
		OK:      true,
		Session: s.session.Payload().SessionID,
	}
	// Attach the pairing_secret before Approve so a one-shot /status poll
	// cannot TakeSecrets with an empty pairing field.
	secret := s.attachTrustSecret(request.Phone)
	if !s.autoApprove {
		writeJSON(w, http.StatusAccepted, resp)
		return
	}

	if err := s.session.ApproveWithSecret(s.clock.Now(), secret); err != nil {
		writePairingError(w, err)
		return
	}
	s.flushTrustSecret(request.Phone, secret)
	s.pinMedia()
	resume, pairingSecret, _ := s.session.TakeSecrets()
	resp.Approved = true
	resp.ResumeToken = resume
	resp.PairingSecret = pairingSecret
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if s.session == nil {
		writeError(w, http.StatusServiceUnavailable, "no active pairing session")
		return
	}
	if !isLoopbackRequest(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "approval is only available locally")
		return
	}

	var request approveRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if request.SessionID != s.session.Payload().SessionID {
		writeError(w, http.StatusUnauthorized, "invalid session")
		return
	}

	already := s.session.IsApproved()
	secret := ""
	if !already {
		if phone, ok := s.session.PendingPhone(); ok {
			secret = s.attachTrustSecret(phone)
		}
	}
	if err := s.session.ApproveWithSecret(s.clock.Now(), secret); err != nil {
		writePairingError(w, err)
		return
	}
	if !already {
		s.flushTrustSecret(s.session.ApprovedPhone(), secret)
	}

	writeJSON(w, http.StatusOK, statusResponse{
		OK:       true,
		Approved: true,
		Session:  request.SessionID,
	})
}

func (s *Server) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if s.session == nil {
		writeError(w, http.StatusServiceUnavailable, "no active pairing session")
		return
	}

	var request reconnectRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	remoteIP, err := remoteIP(r.RemoteAddr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid remote address")
		return
	}

	if !s.limiter.allow(remoteIP.String(), request.Phone.ID, s.clock.Now()) {
		writeError(w, http.StatusTooManyRequests, "rate limited")
		return
	}

	source := pairing.RTPSource{
		IP:   remoteIP,
		Port: request.RTPPort,
		SSRC: request.SSRC,
	}

	if s.session.MatchResumeToken(request.ResumeToken) {
		approved := s.session.ApprovedPhone()
		if approved.ID != request.Phone.ID {
			writeError(w, http.StatusConflict, pairing.ErrDifferentPhone.Error())
			return
		}
		if request.Video != nil {
			if err := s.session.SetNegotiatedVideo(*request.Video); err != nil {
				writePairingError(w, err)
				return
			}
		}
		if err := s.session.RebindRTPSource(source); err != nil {
			writePairingError(w, err)
			return
		}
		s.storeCamera(request.Camera)
		s.pinMedia()
		s.writeReconnectOK(w)
		return
	}

	if s.trust != nil {
		if _, ok := s.trust.LookupBySecret(request.Phone.ID, request.PairingSecret); ok {
			if err := s.session.ApproveTrusted(request.Phone, source, request.Video); err != nil {
				if errors.Is(err, pairing.ErrDifferentPhone) {
					writeError(w, http.StatusConflict, pairing.ErrDifferentPhone.Error())
					return
				}
				writePairingError(w, err)
				return
			}
			s.trust.Touch(request.Phone.ID, s.clock.Now())
			s.storeCamera(request.Camera)
			s.pinMedia()
			s.writeReconnectOK(w)
			return
		}
	}

	writeError(w, http.StatusUnauthorized, "unauthorized")
}

func (s *Server) writeReconnectOK(w http.ResponseWriter) {
	payload := s.session.Payload()
	writeJSON(w, http.StatusOK, reconnectResponse{
		OK:          true,
		Approved:    true,
		Session:     payload.SessionID,
		ResumeToken: s.session.ResumeToken(),
		Control:     payload.Control,
		RTP:         payload.RTP,
		Video:       s.session.NegotiatedVideo(),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"ok":       true,
		"approved": false,
	}
	if s.session != nil {
		body["approved"] = s.session.IsApproved()
		if id := s.session.Payload().SessionID; id != "" {
			body["session"] = id
		}
		if s.session.IsApproved() {
			phone := s.session.ApprovedPhone()
			if phone.Name != "" {
				body["phone_name"] = phone.Name
			}
			if phone.ID != "" {
				body["phone_id"] = phone.ID
			}
			video := s.session.NegotiatedVideo()
			body["video"] = video
			if s.session.ReconnectReady() {
				body["reconnect_ready"] = true
			}
		}
	}
	if camera := s.currentCamera(); camera != "" {
		body["camera"] = camera
	}
	if s.media != nil {
		st := s.media.Stats()
		body["packets_forwarded"] = st.Forwarded
		body["packets_dropped_acl"] = st.DroppedACL
		body["packets_received"] = st.Received
		if n := s.media.ReceiverRestarts(); n != 0 {
			body["receiver_restarts"] = n
		}
		if !st.LastPacket.IsZero() {
			ms := s.clock.Now().Sub(st.LastPacket).Milliseconds()
			if ms < 0 {
				ms = 0
			}
			body["last_rtp_ms"] = ms
			if ms > 400 {
				body["request_keyframe"] = true
			}
		} else {
			body["last_rtp_ms"] = nil
		}
	}
	if resume, secret := s.statusSecrets(r); resume != "" {
		body["resume_token"] = resume
		if secret != "" {
			body["pairing_secret"] = secret
		}
	}
	if s.trust != nil {
		if n := s.trust.Count(); n > 0 {
			body["trusted_count"] = n
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// attachTrustSecret mints a pairing_secret onto the session before Approve.
// It does not write the store: a denied/failed approve must not rotate disk.
// Already-approved sessions keep an existing secret. A different approved
// phone (ApproveTrusted) is not overwritten.
func (s *Server) attachTrustSecret(phone pairing.Phone) string {
	if s.trust == nil || s.session == nil || phone.ID == "" {
		return ""
	}
	if existing := s.session.PairingSecret(); existing != "" {
		return existing
	}
	if s.session.IsApproved() && s.session.ApprovedPhone().ID != phone.ID {
		return ""
	}
	secret, err := trust.NewSecret()
	if err != nil || secret == "" {
		return ""
	}
	s.session.SetPairingSecret(secret)
	return secret
}

func (s *Server) flushTrustSecret(phone pairing.Phone, secret string) {
	if s.trust == nil || phone.ID == "" || secret == "" {
		return
	}
	_ = s.trust.Put(phone.ID, phone.Name, secret, s.clock.Now())
}

func (s *Server) handleTrustList(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "trust list is only available locally")
		return
	}
	phones := []trust.PublicPhone{}
	if s.trust != nil {
		phones = s.trust.List()
	}
	writeJSON(w, http.StatusOK, map[string]any{"phones": phones})
}

func (s *Server) handleTrustDelete(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "trust revoke is only available locally")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing phone id")
		return
	}
	if s.trust == nil {
		writeError(w, http.StatusNotFound, trust.ErrNotFound.Error())
		return
	}
	if err := s.trust.Revoke(id); err != nil {
		if errors.Is(err, trust.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not revoke")
		return
	}
	if s.session != nil {
		approved := s.session.ApprovedPhone()
		pending, hasPending := s.session.PendingPhone()
		if (approved.ID == id || approved.Name == id) || (hasPending && (pending.ID == id || pending.Name == id)) {
			s.session.Invalidate()
			if s.media != nil {
				s.media.SetAllow(pairing.RTPSource{})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	if s.session == nil {
		writeError(w, http.StatusServiceUnavailable, "no active pairing session")
		return
	}

	var request leaveRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if request.SessionID != "" && request.SessionID != s.session.Payload().SessionID {
		writeError(w, http.StatusUnauthorized, "invalid session")
		return
	}
	if !s.leaveAuthorized(r, request) {
		writeError(w, http.StatusForbidden, "leave is only available to the paired phone or locally")
		return
	}

	if s.media != nil {
		s.media.SetAllow(pairing.RTPSource{})
	}
	s.session.ResetPairing()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) leaveAuthorized(r *http.Request, request leaveRequest) bool {
	if isLoopbackRequest(r.RemoteAddr) {
		return true
	}
	if ip, err := remoteIP(r.RemoteAddr); err == nil {
		if want, ok := s.session.ControlIP(); ok && want.Equal(ip) {
			return true
		}
	}
	return s.session.MatchLeaveSecrets(request.SessionID, request.Token, request.ResumeToken, request.PairingSecret)
}

func (s *Server) pinMedia() {
	if s.media == nil || s.session == nil {
		return
	}
	src, ok := s.session.ApprovedSource()
	if !ok {
		return
	}
	s.media.SetAllow(src)
	_ = s.media.RestartReceiver(s.session.NegotiatedVideo())
}

func (s *Server) storeCamera(camera string) {
	if camera == "" {
		return
	}
	s.mu.Lock()
	s.camera = camera
	s.mu.Unlock()
}

func (s *Server) currentCamera() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.camera
}

// statusSecrets delivers credentials in require-approval mode without destroying
// them until streaming starts from the approved peer. Loopback and AutoApprove
// never receive standing credentials on GET /status.
func (s *Server) statusSecrets(r *http.Request) (resume, pairing string) {
	if s.autoApprove || s.session == nil || isLoopbackRequest(r.RemoteAddr) {
		return "", ""
	}
	ip, err := remoteIP(r.RemoteAddr)
	if err != nil {
		return "", ""
	}
	approvedIP, ok := s.session.ApprovedControlIP()
	if !ok || !approvedIP.Equal(ip) {
		return "", ""
	}
	if s.media != nil {
		st := s.media.Stats()
		if !st.LastPacket.IsZero() || st.Forwarded > 0 {
			return "", ""
		}
	}
	resume, pairing, ok = s.session.PeekSecrets()
	if !ok {
		return "", ""
	}
	return resume, pairing
}

const (
	reconnectIPLimit     = 5
	reconnectIPWindow    = time.Second
	reconnectPhoneLimit  = 20
	reconnectPhoneWindow = time.Minute
)

type reconnectLimiter struct {
	mu     sync.Mutex
	ips    map[string][]time.Time
	phones map[string][]time.Time
}

func newReconnectLimiter() *reconnectLimiter {
	return &reconnectLimiter{
		ips:    make(map[string][]time.Time),
		phones: make(map[string][]time.Time),
	}
}

func (l *reconnectLimiter) allow(ip, phoneID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	ipHits, ipOK := pruneAndLimit(l.ips[ip], now, reconnectIPWindow, reconnectIPLimit)
	l.ips[ip] = ipHits
	phoneHits, phoneOK := pruneAndLimit(l.phones[phoneID], now, reconnectPhoneWindow, reconnectPhoneLimit)
	l.phones[phoneID] = phoneHits
	return ipOK && phoneOK
}

func pruneAndLimit(prev []time.Time, now time.Time, window time.Duration, limit int) ([]time.Time, bool) {
	cutoff := now.Add(-window)
	kept := prev[:0]
	for _, ts := range prev {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= limit {
		return kept, false
	}
	return append(kept, now), true
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()

	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writePairingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pairing.ErrInvalidToken),
		errors.Is(err, pairing.ErrInvalidSessionID),
		errors.Is(err, pairing.ErrTokenConsumed):
		writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, pairing.ErrExpired),
		errors.Is(err, pairing.ErrInvalidated):
		writeError(w, http.StatusGone, err.Error())
	case errors.Is(err, pairing.ErrInvalidEndpoint),
		errors.Is(err, pairing.ErrInvalidSSRC),
		errors.Is(err, pairing.ErrInvalidVideo),
		errors.Is(err, pairing.ErrNoPendingPhone):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusConflict, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func remoteIP(remoteAddr string) (net.IP, error) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return nil, strconv.ErrSyntax
	}
	return ip, nil
}

func isLoopbackRequest(remoteAddr string) bool {
	ip, err := remoteIP(remoteAddr)
	return err == nil && ip.IsLoopback()
}
