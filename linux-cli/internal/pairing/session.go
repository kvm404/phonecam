package pairing

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ProtocolVersion = 1
	DefaultWidth    = 1280
	DefaultHeight   = 720
	DefaultFPS      = 30
	DefaultTTL      = 2 * time.Minute
	TokenBytes      = 32
	SessionIDBytes  = 16
)

var (
	ErrExpired          = errors.New("pairing token expired")
	ErrInvalidToken     = errors.New("invalid pairing token")
	ErrTokenConsumed    = errors.New("pairing token already consumed")
	ErrNotApproved      = errors.New("session is not approved")
	ErrAlreadyBound     = errors.New("rtp source is already bound")
	ErrSourceMismatch   = errors.New("rtp source does not match approved session")
	ErrInvalidSSRC      = errors.New("rtp ssrc must be non-zero")
	ErrInvalidEndpoint  = errors.New("endpoint must include host and port")
	ErrInvalidSessionID = errors.New("session id is empty")
	ErrInvalidated      = errors.New("session is invalidated")
	ErrNoPendingPhone   = errors.New("no pending phone approval")
	ErrInvalidVideo     = errors.New("invalid video profile")
	ErrDifferentPhone   = errors.New("a different phone is already approved")
)

const (
	MinVideoDimension = 16
	MaxVideoDimension = 4096
	MinVideoFPS       = 1
	MaxVideoFPS       = 120
)

type Config struct {
	LaptopName string
	LaptopID   string
	ControlURL string
	RTPHost    string
	RTPPort    int
	Width      int
	Height     int
	FPS        int
	TTL        time.Duration
	Now        time.Time
}

type VideoProfile struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	FPS    int `json:"fps"`
}

type Payload struct {
	Version   int          `json:"v"`
	Name      string       `json:"name"`
	LaptopID  string       `json:"laptop_id,omitempty"`
	Control   string       `json:"control"`
	RTP       string       `json:"rtp"`
	SessionID string       `json:"session"`
	Token     string       `json:"token"`
	Expires   time.Time    `json:"expires"`
	Transport string       `json:"transport"`
	Video     VideoProfile `json:"video"`
}

type Phone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TokenRequest struct {
	SessionID string
	Token     string
	Phone     Phone
	ControlIP net.IP
	RTPPort   int
	SSRC      uint32
	// Video is the phone's actual negotiated output profile. Nil means the
	// phone did not report one and the session's advertised profile applies.
	Video *VideoProfile
}

type RTPSource struct {
	IP   net.IP
	Port int
	SSRC uint32
}

type Session struct {
	mu            sync.RWMutex
	payload       Payload
	consumed      bool
	approved      bool
	invalidated   bool
	pendingPhone  Phone
	pendingSource RTPSource
	approvedPhone Phone
	rtpSource     *RTPSource
	negotiated    *VideoProfile
	resumeToken   string
	pairingSecret string
	secretsTaken  bool
}

func New(config Config) (*Session, error) {
	if !validControlURL(config.ControlURL) || !validHost(config.RTPHost) || !validPort(config.RTPPort) {
		return nil, ErrInvalidEndpoint
	}

	now := config.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	ttl := config.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	width := valueOrDefault(config.Width, DefaultWidth)
	height := valueOrDefault(config.Height, DefaultHeight)
	fps := valueOrDefault(config.FPS, DefaultFPS)
	laptopName := config.LaptopName
	if laptopName == "" {
		laptopName = "phonecam-linux"
	}

	sessionID, err := randomBase64URL(SessionIDBytes)
	if err != nil {
		return nil, err
	}
	token, err := randomBase64URL(TokenBytes)
	if err != nil {
		return nil, err
	}

	return &Session{
		payload: Payload{
			Version:   ProtocolVersion,
			Name:      laptopName,
			LaptopID:  config.LaptopID,
			Control:   config.ControlURL,
			RTP:       net.JoinHostPort(config.RTPHost, fmt.Sprintf("%d", config.RTPPort)),
			SessionID: sessionID,
			Token:     token,
			Expires:   now.Add(ttl).UTC(),
			Transport: "rtp-h264",
			Video: VideoProfile{
				Width:  width,
				Height: height,
				FPS:    fps,
			},
		},
	}, nil
}

func (s *Session) Payload() Payload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.payload
}

func (s *Session) PayloadJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return json.Marshal(s.payload)
}

func (s *Session) IsExpired(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.isExpiredLocked(now)
}

func (s *Session) ConsumeToken(request TokenRequest, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.invalidated {
		return ErrInvalidated
	}
	if s.payload.SessionID == "" {
		return ErrInvalidSessionID
	}
	if request.SessionID != s.payload.SessionID {
		return ErrInvalidSessionID
	}
	if s.consumed {
		return ErrTokenConsumed
	}
	if s.isExpiredLocked(now) {
		return ErrExpired
	}
	if subtle.ConstantTimeCompare([]byte(request.Token), []byte(s.payload.Token)) != 1 {
		return ErrInvalidToken
	}
	if request.ControlIP == nil || !validPort(request.RTPPort) {
		return ErrInvalidEndpoint
	}
	if request.SSRC == 0 {
		return ErrInvalidSSRC
	}
	if request.Video != nil {
		if err := validateVideoProfile(*request.Video); err != nil {
			return err
		}
	}
	s.consumed = true
	s.approved = false
	s.secretsTaken = false
	s.pendingPhone = request.Phone
	s.pendingSource = RTPSource{
		IP:   append(net.IP(nil), request.ControlIP...),
		Port: request.RTPPort,
		SSRC: request.SSRC,
	}
	if request.Video != nil {
		video := *request.Video
		s.negotiated = &video
	}
	return nil
}

func (s *Session) Approve(now time.Time) error {
	return s.approve(now, "")
}

// ApproveWithSecret is Approve plus pairing_secret in the same lock so a
// concurrent TakeSecrets cannot observe an approved session with an empty
// pairing field. already-approved is still a no-op (secret is not rotated).
func (s *Session) ApproveWithSecret(now time.Time, secret string) error {
	return s.approve(now, secret)
}

func (s *Session) approve(now time.Time, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.invalidated {
		return ErrInvalidated
	}
	if s.approved {
		return nil
	}
	if !s.consumed {
		return ErrInvalidToken
	}
	if s.pendingSource.IP == nil {
		return ErrNoPendingPhone
	}
	token, err := randomBase64URL(TokenBytes)
	if err != nil {
		return err
	}
	s.approved = true
	s.approvedPhone = s.pendingPhone
	s.resumeToken = token
	if secret != "" {
		s.pairingSecret = secret
	}
	s.secretsTaken = false
	return nil
}

// SetPairingSecret attaches the long-lived pairing_secret. Call it before
// Approve so one-shot TakeSecrets cannot race an empty pairing field.
func (s *Session) SetPairingSecret(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairingSecret = secret
}

// PairingSecret returns the attached pairing_secret, or empty.
func (s *Session) PairingSecret() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pairingSecret
}

// ApproveTrusted installs a trusted phone without consulting QR TTL and
// without Invalidate. leftover QR is marked consumed. pairing_secret is
// not rotated. A same-id already-approved session rebinds and echoes the
// live resume_token.
func (s *Session) ApproveTrusted(phone Phone, source RTPSource, video *VideoProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.invalidated {
		return ErrInvalidated
	}
	if source.SSRC == 0 {
		return ErrInvalidSSRC
	}
	if source.IP == nil || !validPort(source.Port) {
		return ErrInvalidEndpoint
	}
	if video != nil {
		if err := validateVideoProfile(*video); err != nil {
			return err
		}
	}

	if s.approved {
		if s.approvedPhone.ID != phone.ID {
			return ErrDifferentPhone
		}
		pending := copyRTPSource(source)
		bound := copyRTPSource(source)
		s.pendingSource = pending
		s.rtpSource = &bound
		if video != nil {
			copied := *video
			s.negotiated = &copied
		}
		return nil
	}

	token, err := randomBase64URL(TokenBytes)
	if err != nil {
		return err
	}
	s.consumed = true
	s.approved = true
	s.approvedPhone = phone
	pending := copyRTPSource(source)
	bound := copyRTPSource(source)
	s.pendingSource = pending
	s.rtpSource = &bound
	s.resumeToken = token
	s.secretsTaken = false
	if video != nil {
		copied := *video
		s.negotiated = &copied
	}
	return nil
}

// PendingPhone returns the phone that consumed the pairing token and true when
// the session is awaiting approval: the token has been consumed but the session
// is neither approved nor invalidated. Otherwise it returns a zero Phone and
// false.
func (s *Session) PendingPhone() (Phone, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.invalidated || s.approved || !s.consumed {
		return Phone{}, false
	}
	return s.pendingPhone, true
}

func (s *Session) IsApproved() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.approved
}

func (s *Session) ApprovedPhone() Phone {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.approvedPhone
}

// ApprovedSource returns the phone-announced RTP pin after approval.
func (s *Session) ApprovedSource() (RTPSource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.invalidated || !s.approved || s.pendingSource.IP == nil {
		return RTPSource{}, false
	}
	return copyRTPSource(s.pendingSource), true
}

// NegotiatedVideo returns the phone-reported video profile if one was stored
// during token consumption, otherwise the session's advertised profile.
func (s *Session) NegotiatedVideo() VideoProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.negotiated != nil {
		return *s.negotiated
	}
	return s.payload.Video
}

func (s *Session) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.invalidated = true
	s.consumed = true
	s.approved = false
	s.rtpSource = nil
	s.resumeToken = ""
	s.pairingSecret = ""
	s.secretsTaken = true
}

// RebindRTPSource replaces the approved RTP pin. Same phone session only;
// callers must reject a different phone.id before invoking this.
func (s *Session) RebindRTPSource(source RTPSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.invalidated {
		return ErrInvalidated
	}
	if !s.approved {
		return ErrNotApproved
	}
	if source.SSRC == 0 {
		return ErrInvalidSSRC
	}
	if source.IP == nil || !validPort(source.Port) {
		return ErrInvalidEndpoint
	}
	pending := copyRTPSource(source)
	bound := copyRTPSource(source)
	s.pendingSource = pending
	s.rtpSource = &bound
	return nil
}

// SetNegotiatedVideo stores a reconnect-announced profile after approval.
func (s *Session) SetNegotiatedVideo(video VideoProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.invalidated {
		return ErrInvalidated
	}
	if !s.approved {
		return ErrNotApproved
	}
	if err := validateVideoProfile(video); err != nil {
		return err
	}
	copied := video
	s.negotiated = &copied
	return nil
}

// MatchResumeToken is a constant-time compare against the live resume token.
func (s *Session) MatchResumeToken(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.approved || s.invalidated || s.resumeToken == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.resumeToken), []byte(token)) == 1
}

// ResumeToken returns the live in-session credential. It is not a QR field.
func (s *Session) ResumeToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.approved || s.invalidated {
		return ""
	}
	return s.resumeToken
}

// TakeSecrets returns resume and pairing secrets once. pairing is empty until
// SetPairingSecret is called. Subsequent calls return ok=false.
func (s *Session) TakeSecrets() (resume, pairing string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.secretsTaken || !s.approved || s.invalidated || s.resumeToken == "" {
		return "", "", false
	}
	s.secretsTaken = true
	return s.resumeToken, s.pairingSecret, true
}

// ResetPairing resets the pairing session so that a new pairing handshake can take place.
func (s *Session) ResetPairing() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.consumed = false
	s.approved = false
	s.secretsTaken = false
	s.pendingPhone = Phone{}
	s.pendingSource = RTPSource{}
	s.approvedPhone = Phone{}
	s.rtpSource = nil
	s.negotiated = nil
	s.resumeToken = ""
	s.pairingSecret = ""
}

// PeekSecrets returns resume and pairing secrets without marking them as taken.
func (s *Session) PeekSecrets() (resume, pairing string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.approved || s.invalidated || s.resumeToken == "" {
		return "", "", false
	}
	return s.resumeToken, s.pairingSecret, true
}

// ApprovedControlIP is the HTTP peer that consumed the QR token (or the last
// rebind). Used for the require-approval one-shot /status delivery.
func (s *Session) ApprovedControlIP() (net.IP, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.invalidated || !s.approved || s.pendingSource.IP == nil {
		return nil, false
	}
	return append(net.IP(nil), s.pendingSource.IP...), true
}

// ReconnectReady reports that an in-session resume_token exists.
func (s *Session) ReconnectReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.approved && !s.invalidated && s.resumeToken != ""
}

func (s *Session) BindRTPSource(source RTPSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.invalidated {
		return ErrInvalidated
	}
	if !s.approved {
		return ErrNotApproved
	}
	if source.SSRC == 0 {
		return ErrInvalidSSRC
	}
	if source.IP == nil || !validPort(source.Port) {
		return ErrInvalidEndpoint
	}
	if s.rtpSource != nil {
		if sameRTPSource(*s.rtpSource, source) {
			return nil
		}
		return ErrAlreadyBound
	}
	if !sameRTPSource(s.pendingSource, source) {
		return ErrSourceMismatch
	}

	copied := copyRTPSource(source)
	s.rtpSource = &copied
	return nil
}

func (s *Session) ValidateRTPSource(source RTPSource) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.invalidated {
		return ErrInvalidated
	}
	if s.rtpSource == nil {
		return ErrNotApproved
	}
	if !sameRTPSource(*s.rtpSource, source) {
		return ErrSourceMismatch
	}
	return nil
}

func (s *Session) isExpiredLocked(now time.Time) bool {
	return !now.Before(s.payload.Expires)
}

func validateVideoProfile(video VideoProfile) error {
	if video.Width < MinVideoDimension || video.Width > MaxVideoDimension {
		return ErrInvalidVideo
	}
	if video.Height < MinVideoDimension || video.Height > MaxVideoDimension {
		return ErrInvalidVideo
	}
	if video.FPS < MinVideoFPS || video.FPS > MaxVideoFPS {
		return ErrInvalidVideo
	}
	return nil
}

func valueOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func randomBase64URL(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func validControlURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.Port() == "" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && validPort(port)
}

func validHost(host string) bool {
	if strings.TrimSpace(host) == "" {
		return false
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return false
	}
	return true
}

func validPort(port int) bool {
	return port > 0 && port <= 65535
}

func sameRTPSource(left, right RTPSource) bool {
	return left.IP.Equal(right.IP) && left.Port == right.Port && left.SSRC == right.SSRC
}

func copyRTPSource(source RTPSource) RTPSource {
	return RTPSource{
		IP:   append(net.IP(nil), source.IP...),
		Port: source.Port,
		SSRC: source.SSRC,
	}
}
