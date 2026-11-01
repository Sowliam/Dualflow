package relay

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"
)

type SessionState string

const (
	SessionActive   SessionState = "ACTIVE"
	SessionDraining SessionState = "DRAINING"
	SessionClosed   SessionState = "CLOSED"
)

var (
	ErrSessionLimitReached  = errors.New("maximum concurrent sessions limit reached (256)")
	ErrSessionNotFound      = errors.New("session not found or has been closed")
	ErrSessionIDCollision   = errors.New("unable to allocate unique session id")
	ErrSessionDrainingWrite = errors.New("outbound write prohibited on draining session")
)

// SessionEndpoint tracks remote endpoint validation status for return traffic routing
type SessionEndpoint struct {
	RemoteAddr      *net.UDPAddr
	PathID          uint16
	IsValidated     bool
	PendingChallenge [8]byte
	ChallengeSentAt time.Time
	LastSeen        time.Time
}

// Session represents an authenticated cryptographic session between Client and Relay
type Session struct {
	mu              sync.RWMutex
	ID              uint32
	SendCipher      *TransportCipher
	RecvCipher      *TransportCipher
	AntiReplay      *AntiReplayWindow
	SeqGen          *MonotonicSequence
	State           SessionState
	CreatedAt       time.Time
	LastActive      time.Time
	DrainDeadline   time.Time
	Endpoints       map[string]*SessionEndpoint // key: RemoteAddr.String()
	drainTimer      *time.Timer
}

// NewSession creates an initialized active session
func NewSession(id uint32, sendCipher, recvCipher *TransportCipher) *Session {
	now := time.Now()
	return &Session{
		ID:         id,
		SendCipher: sendCipher,
		RecvCipher: recvCipher,
		AntiReplay: NewAntiReplayWindow(),
		SeqGen:     NewMonotonicSequence(0),
		State:      SessionActive,
		CreatedAt:  now,
		LastActive: now,
		Endpoints:  make(map[string]*SessionEndpoint),
	}
}

// Touch updates the last active timestamp
func (s *Session) Touch() {
	s.mu.Lock()
	s.LastActive = time.Now()
	s.mu.Unlock()
}

// EncryptOutbound encrypts an outbound packet payload.
// Returns ErrSessionDrainingWrite if the session is currently in draining mode.
func (s *Session) EncryptOutbound(aad, plaintext []byte) (seq uint64, ciphertext []byte, err error) {
	s.mu.RLock()
	if s.State == SessionDraining {
		s.mu.RUnlock()
		return 0, nil, ErrSessionDrainingWrite
	}
	if s.State == SessionClosed {
		s.mu.RUnlock()
		return 0, nil, ErrSessionNotFound
	}
	s.mu.RUnlock()

	seq, err = s.SeqGen.Next()
	if err != nil {
		return 0, nil, err
	}

	ciphertext = s.SendCipher.Encrypt(seq, aad, plaintext)
	s.Touch()
	return seq, ciphertext, nil
}

// DecryptInbound decrypts and replay-verifies an inbound packet payload.
// Allows inbound decryption on both SessionActive and SessionDraining sessions.
func (s *Session) DecryptInbound(seq uint64, aad, ciphertext []byte) ([]byte, error) {
	s.mu.RLock()
	if s.State == SessionClosed {
		s.mu.RUnlock()
		return nil, ErrSessionNotFound
	}
	s.mu.RUnlock()

	var plaintext []byte
	var decErr error

	err := s.AntiReplay.VerifyAndCommit(seq, func() error {
		pt, err := s.RecvCipher.Decrypt(seq, aad, ciphertext)
		if err != nil {
			decErr = err
			return err
		}
		plaintext = pt
		return nil
	})

	if err != nil {
		return nil, err
	}
	if decErr != nil {
		return nil, decErr
	}

	s.Touch()
	return plaintext, nil
}

// RegisterEndpoint adds or updates a remote endpoint associated with this session
func (s *Session) RegisterEndpoint(addr *net.UDPAddr, pathID uint16) *SessionEndpoint {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := addr.String()
	ep, exists := s.Endpoints[key]
	if !exists {
		ep = &SessionEndpoint{
			RemoteAddr: addr,
			PathID:     pathID,
			LastSeen:   time.Now(),
		}
		s.Endpoints[key] = ep
	} else {
		ep.LastSeen = time.Now()
		ep.PathID = pathID
	}
	return ep
}

// SetEndpointValidated marks an endpoint as verified by challenge/response
func (s *Session) SetEndpointValidated(addr *net.UDPAddr, validated bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := addr.String()
	if ep, exists := s.Endpoints[key]; exists {
		ep.IsValidated = validated
	}
}

// SetPendingChallenge records a challenge nonce sent to an endpoint
func (s *Session) SetPendingChallenge(addr *net.UDPAddr, nonce [8]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := addr.String()
	if ep, exists := s.Endpoints[key]; exists {
		ep.PendingChallenge = nonce
		ep.ChallengeSentAt = time.Now()
	}
}

// VerifyChallenge checks if the returned response nonce matches the pending challenge
func (s *Session) VerifyChallenge(addr *net.UDPAddr, nonce [8]byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := addr.String()
	ep, exists := s.Endpoints[key]
	if !exists {
		return false
	}

	if ep.PendingChallenge == nonce && !ep.PendingChallengeSentExpired() {
		ep.IsValidated = true
		ep.PendingChallenge = [8]byte{}
		return true
	}
	return false
}

// PendingChallengeSentExpired returns true if challenge sent more than 3 seconds ago
func (ep *SessionEndpoint) PendingChallengeSentExpired() bool {
	return time.Since(ep.ChallengeSentAt) > 3*time.Second
}

// GetValidatedEndpoints returns a snapshot list of all currently validated endpoints
func (s *Session) GetValidatedEndpoints() []*SessionEndpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*SessionEndpoint
	for _, ep := range s.Endpoints {
		if ep.IsValidated {
			// Shallow copy
			result = append(result, &SessionEndpoint{
				RemoteAddr:  ep.RemoteAddr,
				PathID:      ep.PathID,
				IsValidated: ep.IsValidated,
				LastSeen:    ep.LastSeen,
			})
		}
	}
	return result
}

// GetValidatedEndpointForPath returns the validated endpoint for a specific PathID, or nil
func (s *Session) GetValidatedEndpointForPath(pathID uint16) *SessionEndpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ep := range s.Endpoints {
		if ep.PathID == pathID && ep.IsValidated {
			return &SessionEndpoint{
				RemoteAddr:  ep.RemoteAddr,
				PathID:      ep.PathID,
				IsValidated: ep.IsValidated,
				LastSeen:    ep.LastSeen,
			}
		}
	}
	return nil
}

// Close gracefully terminates the session and performs best-effort key zeroization
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.State == SessionClosed {
		return
	}

	s.State = SessionClosed
	if s.drainTimer != nil {
		s.drainTimer.Stop()
	}

	// Memory hygiene: overwrite cipher references
	s.SendCipher = nil
	s.RecvCipher = nil
}

// SessionTable manages active and draining sessions with strict resource caps
const (
	// DefaultDrainDuration allows late in-flight multi-path packets to decrypt, exceeding MaxRetryDuration (15s)
	DefaultDrainDuration = 25 * time.Second
)

type SessionTable struct {
	mu          sync.RWMutex
	sessions    map[uint32]*Session
	maxSessions int
	onRekey     func(oldID uint32, newSession *Session)
}

// NewSessionTable instantiates a session table with a capacity cap
func NewSessionTable(maxSessions int) *SessionTable {
	if maxSessions <= 0 {
		maxSessions = 256
	}
	return &SessionTable{
		sessions:    make(map[uint32]*Session),
		maxSessions: maxSessions,
	}
}

// SetRekeyCallback registers a hook to notify forwarder and flow managers of session rekeying
func (st *SessionTable) SetRekeyCallback(cb func(oldID uint32, newSession *Session)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.onRekey = cb
}

// AllocateSessionID generates a non-zero 32-bit random session identifier,
// guaranteeing collision avoidance against currently active/draining sessions.
func (st *SessionTable) AllocateSessionID() (uint32, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.sessions) >= st.maxSessions {
		return 0, ErrSessionLimitReached
	}

	var b [4]byte
	for attempts := 0; attempts < 1000; attempts++ {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		id := binary.BigEndian.Uint32(b[:])
		if id != 0 {
			if _, exists := st.sessions[id]; !exists {
				return id, nil
			}
		}
	}

	return 0, ErrSessionIDCollision
}

// Add registers an active session into the table
func (st *SessionTable) Add(s *Session) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	if len(st.sessions) >= st.maxSessions {
		return ErrSessionLimitReached
	}

	st.sessions[s.ID] = s
	return nil
}

// Get retrieves a session by ID
func (st *SessionTable) Get(id uint32) (*Session, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()

	s, ok := st.sessions[id]
	return s, ok
}

// Remove drops and closes a session
func (st *SessionTable) Remove(id uint32) {
	st.mu.Lock()
	s, ok := st.sessions[id]
	if ok {
		delete(st.sessions, id)
	}
	st.mu.Unlock()

	if ok {
		s.Close()
	}
}

// Rekey transitions oldSession into a draining state and establishes newSession.
// Old session remains active for drainDuration (default 25s) to service in-flight multi-path packets.
func (st *SessionTable) Rekey(oldID uint32, newSession *Session, drainDuration time.Duration) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	if drainDuration <= 0 {
		drainDuration = DefaultDrainDuration
	}

	oldSession, exists := st.sessions[oldID]
	if !exists {
		// Just register new session if old session is already gone
		st.sessions[newSession.ID] = newSession
		return nil
	}

	// Migrate validated endpoints from old session to new session so path validation is retained
	oldSession.mu.RLock()
	for k, ep := range oldSession.Endpoints {
		newSession.Endpoints[k] = &SessionEndpoint{
			RemoteAddr:  ep.RemoteAddr,
			PathID:      ep.PathID,
			IsValidated: ep.IsValidated,
			LastSeen:    time.Now(),
		}
	}
	oldSession.mu.RUnlock()

	// Register new session as active
	st.sessions[newSession.ID] = newSession

	// Put old session in draining state
	oldSession.mu.Lock()
	oldSession.State = SessionDraining
	oldSession.DrainDeadline = time.Now().Add(drainDuration)
	oldSession.drainTimer = time.AfterFunc(drainDuration, func() {
		st.Remove(oldID)
	})
	oldSession.mu.Unlock()

	// Notify forwarder to migrate flows to new session
	cb := st.onRekey
	if cb != nil {
		go cb(oldID, newSession)
	}

	return nil
}

// Count returns the number of active and draining sessions
func (st *SessionTable) Count() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.sessions)
}

// CleanupStale scans and removes inactive sessions
func (st *SessionTable) CleanupStale(idleTimeout time.Duration) int {
	st.mu.Lock()
	var toRemove []uint32
	now := time.Now()

	for id, s := range st.sessions {
		s.mu.RLock()
		if s.State == SessionDraining && now.After(s.DrainDeadline) {
			toRemove = append(toRemove, id)
		} else if s.State == SessionActive && now.Sub(s.LastActive) > idleTimeout {
			toRemove = append(toRemove, id)
		}
		s.mu.RUnlock()
	}

	for _, id := range toRemove {
		if s, ok := st.sessions[id]; ok {
			delete(st.sessions, id)
			s.Close()
		}
	}
	st.mu.Unlock()

	return len(toRemove)
}

// CloseAll shuts down and purges all active and draining sessions
func (st *SessionTable) CloseAll() {
	st.mu.Lock()
	defer st.mu.Unlock()

	for id, s := range st.sessions {
		delete(st.sessions, id)
		s.Close()
	}
}
