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
)

const (
	MinVideoDimension = 16
	MaxVideoDimension = 4096
	MinVideoFPS       = 1
	MaxVideoFPS       = 120
)

type Config struct {
	LaptopName string
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
	Control   string       `json:"control"`
	RTP       string       `json:"rtp"`
	SessionID string       `json:"session"`
	Token     string       `json:"token"`
	Expires   time.Time    `json:"expires"`
	Transport string       `json:"transport"`
	Video     VideoProfile `json:"video"`
}

type Phone struct {
	ID   string
	Name string
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
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.invalidated {
		return ErrInvalidated
	}
	if s.isExpiredLocked(now) {
		return ErrExpired
	}
	if !s.consumed {
		return ErrInvalidToken
	}
	if s.pendingSource.IP == nil {
		return ErrNoPendingPhone
	}
	s.approved = true
	s.approvedPhone = s.pendingPhone
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
