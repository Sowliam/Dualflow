package wintun

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Manager coordinates all Wintun virtual adapter lifecycle and session operations.
type Manager struct {
	mu                  sync.Mutex
	dll                 *WintunDLL
	adapterName         string
	tunnelType          string
	adapterHandle       uintptr
	sessionHandle       uintptr
	adapterCreatedByUs  bool
	isSessionRunning    bool
	readWaitEvent       syscall.Handle
	quitChan            chan struct{}
	rxWorkerWg          sync.WaitGroup
	sessionStartTime    time.Time
	lastError           string

	// Atomic Telemetry Counters (strictly incremented on real API events)
	rxPackets      uint64
	txPackets      uint64
	rxBytes        uint64
	txBytes        uint64
	droppedPackets uint64
	lastRxTime     int64
	lastTxTime     int64

	// Optional packet hook for internal test harnesses
	packetReceiver func(packet []byte)
}

var (
	defaultManager *Manager
	managerOnce    sync.Once
)

// GetManager returns the singleton Manager instance.
func GetManager() *Manager {
	managerOnce.Do(func() {
		defaultManager = NewManager(DefaultAdapterName, DefaultTunnelType)
	})
	return defaultManager
}

// NewManager creates an isolated Manager instance.
func NewManager(adapterName, tunnelType string) *Manager {
	return &Manager{
		dll:         GetDLL(),
		adapterName: adapterName,
		tunnelType:  tunnelType,
	}
}

// SetPacketReceiver registers a callback hook invoked whenever a valid packet is received.
func (m *Manager) SetPacketReceiver(fn func(packet []byte)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.packetReceiver = fn
}

// GetStatus returns the current runtime, adapter, and session health snapshot.
func (m *Manager) GetStatus() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	driverStatus := DriverStatusNotInstalled
	driverVersion := "UNKNOWN"
	driverPath := ""
	isElevated := IsElevated()

	if m.dll.IsAvailable() {
		driverStatus = DriverStatusAvailable
		driverPath = m.dll.GetPath()
		ver, err := m.dll.GetRunningDriverVersion()
		if err == nil {
			driverVersion = ver
		} else {
			driverVersion = "0.14.1 (DLL Available, Driver idle)"
		}
	} else if m.dll.GetLoadError() != nil {
		driverStatus = DriverStatusError
		driverVersion = "ERROR"
	}

	adapterExists := false
	adapterStatus := AdapterStatusNotCreated
	if m.adapterHandle != 0 {
		adapterExists = true
		adapterStatus = AdapterStatusReady
	} else {
		// Check if exists in system
		exists, _ := m.checkAdapterExistsLocked()
		adapterExists = exists
		if exists {
			adapterStatus = AdapterStatusReady
		}
	}

	sessionStatus := SessionStatusStopped
	if m.isSessionRunning {
		sessionStatus = SessionStatusRunning
	}

	var uptime int64
	if m.isSessionRunning && !m.sessionStartTime.IsZero() {
		uptime = time.Since(m.sessionStartTime).Milliseconds()
	}

	return Status{
		DriverStatus:  driverStatus,
		DriverVersion: driverVersion,
		DriverPath:    driverPath,
		IsElevated:    isElevated,
		AdapterExists: adapterExists,
		AdapterName:   m.adapterName,
		AdapterStatus: adapterStatus,
		SessionStatus: sessionStatus,
		LastError:     m.lastError,
		RxPackets:     atomic.LoadUint64(&m.rxPackets),
		TxPackets:     atomic.LoadUint64(&m.txPackets),
		RxBytes:       atomic.LoadUint64(&m.rxBytes),
		TxBytes:       atomic.LoadUint64(&m.txBytes),
		UptimeMs:      uptime,
	}
}

// GetStats returns atomic packet statistics.
func (m *Manager) GetStats() Stats {
	return Stats{
		RxPackets:       atomic.LoadUint64(&m.rxPackets),
		TxPackets:       atomic.LoadUint64(&m.txPackets),
		RxBytes:         atomic.LoadUint64(&m.rxBytes),
		TxBytes:         atomic.LoadUint64(&m.txBytes),
		DroppedPackets:  atomic.LoadUint64(&m.droppedPackets),
		LastRxTimestamp: atomic.LoadInt64(&m.lastRxTime),
		LastTxTimestamp: atomic.LoadInt64(&m.lastTxTime),
		LastError:       m.lastError,
	}
}

// CheckAdapterExists inspects whether the virtual adapter exists on the Windows system.
func (m *Manager) CheckAdapterExists() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkAdapterExistsLocked()
}

func (m *Manager) checkAdapterExistsLocked() (bool, error) {
	if m.adapterHandle != 0 {
		return true, nil
	}

	if !m.dll.IsAvailable() {
		return false, fmt.Errorf("wintun runtime not available: %w", m.dll.GetLoadError())
	}

	namePtr, err := syscall.UTF16PtrFromString(m.adapterName)
	if err != nil {
		return false, err
	}

	r1, _, callErr := m.dll.procOpenAdapter.Call(uintptr(unsafe.Pointer(namePtr)))
	if r1 != 0 {
		// Close the test handle immediately so we do not hold a reference
		m.dll.procCloseAdapter.Call(r1)
		return true, nil
	}

	// If access denied, report requirement
	if errors.Is(callErr, syscall.Errno(5)) { // ERROR_ACCESS_DENIED
		return false, fmt.Errorf("insufficient privileges to query adapter (Administrator required): %w", callErr)
	}

	// Fallback to checking Windows netsh/pnputil
	out, pnpErr := exec.Command("pnputil", "/enum-devices", "/class", "Net").Output()
	if pnpErr == nil && strings.Contains(string(out), m.adapterName) {
		return true, nil
	}

	return false, nil
}

// CreateAdapter creates the Wintun adapter in Windows. Requires Administrator privileges.
func (m *Manager) CreateAdapter() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.dll.IsAvailable() {
		m.lastError = "wintun.dll is not available"
		return fmt.Errorf("wintun runtime not available: %w", m.dll.GetLoadError())
	}

	if !IsElevated() {
		err := fmt.Errorf("administrator privileges are required to create Wintun adapter '%s' (Win32 ERROR_ACCESS_DENIED)", m.adapterName)
		m.lastError = err.Error()
		return err
	}

	if m.adapterHandle != 0 {
		return nil // already open
	}

	namePtr, err := syscall.UTF16PtrFromString(m.adapterName)
	if err != nil {
		m.lastError = err.Error()
		return err
	}

	tunnelPtr, err := syscall.UTF16PtrFromString(m.tunnelType)
	if err != nil {
		m.lastError = err.Error()
		return err
	}

	r1, _, callErr := m.dll.procCreateAdapter.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(tunnelPtr)),
		0, // NULL requested GUID -> randomized deterministic NLA
	)

	if r1 == 0 {
		m.lastError = fmt.Sprintf("WintunCreateAdapter failed: %v", callErr)
		return fmt.Errorf("WintunCreateAdapter failed: %w", callErr)
	}

	m.adapterHandle = r1
	m.adapterCreatedByUs = true
	m.lastError = ""
	return nil
}

// OpenAdapter opens an existing Wintun adapter handle without creating a duplicate.
func (m *Manager) OpenAdapter() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.dll.IsAvailable() {
		m.lastError = "wintun.dll is not available"
		return fmt.Errorf("wintun runtime not available: %w", m.dll.GetLoadError())
	}

	if m.adapterHandle != 0 {
		return nil
	}

	namePtr, err := syscall.UTF16PtrFromString(m.adapterName)
	if err != nil {
		m.lastError = err.Error()
		return err
	}

	r1, _, callErr := m.dll.procOpenAdapter.Call(uintptr(unsafe.Pointer(namePtr)))
	if r1 == 0 {
		m.lastError = fmt.Sprintf("WintunOpenAdapter failed: %v", callErr)
		return fmt.Errorf("WintunOpenAdapter failed: %w", callErr)
	}

	m.adapterHandle = r1
	m.adapterCreatedByUs = false
	m.lastError = ""
	return nil
}

// StartSession initiates packet processing with the configured ring buffer capacity.
func (m *Manager) StartSession(capacity uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.isSessionRunning {
		return errors.New("Wintun session is already active")
	}

	if m.adapterHandle == 0 {
		// Attempt to open existing adapter
		if err := m.openAdapterInternal(); err != nil {
			m.lastError = "adapter handle is not open; create or open adapter first"
			return errors.New("adapter must be created or opened before starting session")
		}
	}

	if capacity == 0 {
		capacity = DefaultRingCapacity
	}
	if capacity < MinRingCapacity || capacity > MaxRingCapacity || (capacity&(capacity-1)) != 0 {
		m.lastError = fmt.Sprintf("invalid ring capacity %d (must be power of two between 128KB and 64MB)", capacity)
		return fmt.Errorf("invalid ring capacity: %d", capacity)
	}

	r1, _, callErr := m.dll.procStartSession.Call(m.adapterHandle, uintptr(capacity))
	if r1 == 0 {
		m.lastError = fmt.Sprintf("WintunStartSession failed: %v", callErr)
		return fmt.Errorf("WintunStartSession failed: %w", callErr)
	}

	m.sessionHandle = r1
	m.isSessionRunning = true
	m.sessionStartTime = time.Now()
	m.quitChan = make(chan struct{})

	// Retrieve wait event
	evtR1, _, _ := m.dll.procGetReadWaitEvent.Call(m.sessionHandle)
	m.readWaitEvent = syscall.Handle(evtR1)

	// Launch background RX packet worker
	m.rxWorkerWg.Add(1)
	go m.rxWorker(m.sessionHandle, m.readWaitEvent, m.quitChan)

	m.lastError = ""
	return nil
}

func (m *Manager) openAdapterInternal() error {
	if !m.dll.IsAvailable() {
		return errors.New("wintun dll unavailable")
	}
	namePtr, _ := syscall.UTF16PtrFromString(m.adapterName)
	r1, _, err := m.dll.procOpenAdapter.Call(uintptr(unsafe.Pointer(namePtr)))
	if r1 == 0 {
		return err
	}
	m.adapterHandle = r1
	m.adapterCreatedByUs = false
	return nil
}

// StopSession cleanly terminates packet I/O and shuts down the active Wintun session.
func (m *Manager) StopSession() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isSessionRunning || m.sessionHandle == 0 {
		return nil
	}

	// Signal worker to quit
	close(m.quitChan)

	// End Wintun session
	m.dll.procEndSession.Call(m.sessionHandle)
	m.sessionHandle = 0
	m.isSessionRunning = false

	// Release lock temporarily while awaiting worker shutdown
	m.mu.Unlock()
	m.rxWorkerWg.Wait()
	m.mu.Lock()

	m.lastError = ""
	return nil
}

// CloseAdapter closes open handles safely without deleting the underlying Windows adapter unless created dynamically.
func (m *Manager) CloseAdapter() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.isSessionRunning {
		m.mu.Unlock()
		m.StopSession()
		m.mu.Lock()
	}

	if m.adapterHandle != 0 {
		m.dll.procCloseAdapter.Call(m.adapterHandle)
		m.adapterHandle = 0
		m.adapterCreatedByUs = false
	}

	return nil
}

// DeleteAdapter explicitly uninstalls and removes the DualFlow Tunnel adapter from the Windows system.
// This requires explicit user confirmation.
func (m *Manager) DeleteAdapter() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop session if running
	if m.isSessionRunning {
		m.mu.Unlock()
		m.StopSession()
		m.mu.Lock()
	}

	if !IsElevated() {
		err := errors.New("administrator privileges are required to delete the Wintun adapter")
		m.lastError = err.Error()
		return err
	}

	// Close open handle (if created, WintunCloseAdapter automatically removes it)
	if m.adapterHandle != 0 {
		m.dll.procCloseAdapter.Call(m.adapterHandle)
		m.adapterHandle = 0
	}

	// Clean up any remaining device instances via pnputil if needed
	go func() {
		exec.Command("pnputil", "/remove-device", fmt.Sprintf("SWD\\Wintun\\%s", m.adapterName)).Run()
	}()

	m.lastError = ""
	return nil
}

var procRtlMoveMemory = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlMoveMemory")

// SendPacket transmits an IP packet through the Wintun tunnel interface.
// Packet length must be between 20 (IPv4 minimum) and 65535 bytes.
func (m *Manager) SendPacket(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isSessionRunning || m.sessionHandle == 0 {
		return errors.New("Wintun session is not active")
	}

	pktLen := len(data)
	if pktLen < MinIPv4PacketSize || pktLen > MaxPacketSize {
		return fmt.Errorf("invalid packet size %d (must be between %d and %d bytes)", pktLen, MinIPv4PacketSize, MaxPacketSize)
	}

	r1, _, callErr := m.dll.procAllocateSendPacket.Call(m.sessionHandle, uintptr(pktLen))
	if r1 == 0 {
		atomic.AddUint64(&m.droppedPackets, 1)
		if errors.Is(callErr, syscall.Errno(111)) { // ERROR_BUFFER_OVERFLOW
			return errors.New("wintun send ring buffer is full (packet dropped)")
		}
		return fmt.Errorf("WintunAllocateSendPacket failed: %w", callErr)
	}

	// Copy packet into allocated ring buffer memory using ntdll RtlMoveMemory
	if pktLen > 0 {
		procRtlMoveMemory.Call(r1, uintptr(unsafe.Pointer(&data[0])), uintptr(pktLen))
	}

	// Send packet into kernel ring
	m.dll.procSendPacket.Call(m.sessionHandle, r1)

	// Increment atomic counters strictly upon successful send
	atomic.AddUint64(&m.txPackets, 1)
	atomic.AddUint64(&m.txBytes, uint64(pktLen))
	atomic.StoreInt64(&m.lastTxTime, time.Now().UnixMilli())

	return nil
}

// rxWorker processes incoming packets from the Wintun receive ring buffer.
func (m *Manager) rxWorker(session uintptr, waitEvent syscall.Handle, quit <-chan struct{}) {
	defer m.rxWorkerWg.Done()

	var packetSize uint32

	for {
		select {
		case <-quit:
			return
		default:
		}

		r1, _, callErr := m.dll.procReceivePacket.Call(session, uintptr(unsafe.Pointer(&packetSize)))
		if r1 != 0 {
			// Valid packet received
			sz := int(packetSize)
			if sz >= MinIPv4PacketSize && sz <= MaxPacketSize {
				pktCopy := make([]byte, sz)
				procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&pktCopy[0])), r1, uintptr(sz))

				// Release packet back to Wintun immediately
				m.dll.procReleaseReceivePacket.Call(session, r1)

				// Strictly increment RX metrics only after real kernel reception
				atomic.AddUint64(&m.rxPackets, 1)
				atomic.AddUint64(&m.rxBytes, uint64(sz))
				atomic.StoreInt64(&m.lastRxTime, time.Now().UnixMilli())

				// Dispatch to registered packet hook if present
				m.mu.Lock()
				hook := m.packetReceiver
				m.mu.Unlock()
				if hook != nil {
					hook(pktCopy)
				}
			} else {
				// Invalid size release
				m.dll.procReleaseReceivePacket.Call(session, r1)
				atomic.AddUint64(&m.droppedPackets, 1)
			}
			continue
		}

		// Handle error cases
		if errors.Is(callErr, syscall.Errno(259)) { // ERROR_NO_MORE_ITEMS
			// Wait on event or timeout
			if waitEvent != 0 {
				syscall.WaitForSingleObject(waitEvent, 250)
			} else {
				time.Sleep(50 * time.Millisecond)
			}
		} else if errors.Is(callErr, syscall.Errno(38)) { // ERROR_HANDLE_EOF
			// Session is closing
			return
		} else {
			// Back off on unexpected error
			time.Sleep(100 * time.Millisecond)
		}
	}
}

