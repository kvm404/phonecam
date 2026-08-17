package control

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
	"github.com/kvm404/phonecam/linux-cli/internal/rtp"
)

// Media lets the control server pin the RTP gate and read its counters.
type Media interface {
	SetAllow(src pairing.RTPSource)
	Stats() rtp.Stats
	RestartReceiver(video pairing.VideoProfile) error
}

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now().UTC()
}

type Server struct {
	session *pairing.Session
	clock   Clock
	media   Media
	mux     *http.ServeMux
}

type Config struct {
	Session *pairing.Session
	Clock   Clock
	Media   Media
}

type pairRequest struct {
	SessionID string                `json:"session"`
	Token     string                `json:"token"`
	Phone     pairing.Phone         `json:"phone"`
	RTPPort   int                   `json:"rtp_port"`
	SSRC      uint32                `json:"ssrc"`
	Video     *pairing.VideoProfile `json:"video"`
}

type approveRequest struct {
	SessionID string `json:"session"`
}

type statusResponse struct {
	OK             bool   `json:"ok"`
	Approved       bool   `json:"approved"`
	Session        string `json:"session,omitempty"`
	LastRTPms      *int64 `json:"last_rtp_ms,omitempty"`
	PacketsFwd     uint64 `json:"packets_forwarded,omitempty"`
	PacketsDropped uint64 `json:"packets_dropped_acl,omitempty"`
	PacketsRecv    uint64 `json:"packets_received,omitempty"`
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
		session: config.Session,
		clock:   clock,
		media:   config.Media,
		mux:     http.NewServeMux(),
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
	s.mux.HandleFunc("GET /status", s.handleStatus)
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

	writeJSON(w, http.StatusAccepted, statusResponse{
		OK:       true,
		Approved: s.session.IsApproved(),
		Session:  s.session.Payload().SessionID,
	})
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

	if err := s.session.Approve(s.clock.Now()); err != nil {
		writePairingError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, statusResponse{
		OK:       true,
		Approved: true,
		Session:  request.SessionID,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := statusResponse{OK: true}
	if s.session != nil {
		resp.Approved = s.session.IsApproved()
		resp.Session = s.session.Payload().SessionID
	}
	if s.media == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	st := s.media.Stats()
	body := map[string]any{
		"ok":                  resp.OK,
		"approved":            resp.Approved,
		"packets_forwarded":   st.Forwarded,
		"packets_dropped_acl": st.DroppedACL,
		"packets_received":    st.Received,
	}
	if resp.Session != "" {
		body["session"] = resp.Session
	}
	if !st.LastPacket.IsZero() {
		ms := s.clock.Now().Sub(st.LastPacket).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		body["last_rtp_ms"] = ms
	} else {
		body["last_rtp_ms"] = nil
	}
	writeJSON(w, http.StatusOK, body)
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
