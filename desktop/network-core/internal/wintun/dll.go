package wintun

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

// WintunDLL manages the low-level dynamic binding to the official WireGuard wintun.dll.
type WintunDLL struct {
	mu           sync.RWMutex
	dllPath      string
	dll          *syscall.DLL
	isLoaded     bool
	loadErr      error

	// Procedures
	procCreateAdapter           *syscall.Proc
	procOpenAdapter             *syscall.Proc
	procCloseAdapter            *syscall.Proc
	procDeleteDriver            *syscall.Proc
	procGetAdapterLUID          *syscall.Proc
	procGetRunningDriverVersion *syscall.Proc
	procStartSession            *syscall.Proc
	procEndSession              *syscall.Proc
	procGetReadWaitEvent        *syscall.Proc
	procReceivePacket           *syscall.Proc
	procReleaseReceivePacket    *syscall.Proc
	procAllocateSendPacket      *syscall.Proc
	procSendPacket              *syscall.Proc
	procSetLogger               *syscall.Proc
}

var (
	defaultDLL *WintunDLL
	dllOnce    sync.Once
)

// GetDLL returns the singleton WintunDLL loader.
func GetDLL() *WintunDLL {
	dllOnce.Do(func() {
		defaultDLL = &WintunDLL{}
		defaultDLL.init()
	})
	return defaultDLL
}

func (w *WintunDLL) init() {
	path, err := findWintunDLL()
	if err != nil {
		w.loadErr = err
		return
	}
	w.dllPath = path

	dll, err := syscall.LoadDLL(path)
	if err != nil {
		w.loadErr = fmt.Errorf("failed to load %s: %w", path, err)
		return
	}
	w.dll = dll

	// Map all procedures
	var procErr error
	w.procCreateAdapter, procErr = dll.FindProc("WintunCreateAdapter")
	if procErr != nil { w.loadErr = procErr; return }

	w.procOpenAdapter, procErr = dll.FindProc("WintunOpenAdapter")
	if procErr != nil { w.loadErr = procErr; return }

	w.procCloseAdapter, procErr = dll.FindProc("WintunCloseAdapter")
	if procErr != nil { w.loadErr = procErr; return }

	w.procDeleteDriver, procErr = dll.FindProc("WintunDeleteDriver")
	if procErr != nil { w.loadErr = procErr; return }

	w.procGetAdapterLUID, procErr = dll.FindProc("WintunGetAdapterLUID")
	if procErr != nil { w.loadErr = procErr; return }

	w.procGetRunningDriverVersion, procErr = dll.FindProc("WintunGetRunningDriverVersion")
	if procErr != nil { w.loadErr = procErr; return }

	w.procStartSession, procErr = dll.FindProc("WintunStartSession")
	if procErr != nil { w.loadErr = procErr; return }

	w.procEndSession, procErr = dll.FindProc("WintunEndSession")
	if procErr != nil { w.loadErr = procErr; return }

	w.procGetReadWaitEvent, procErr = dll.FindProc("WintunGetReadWaitEvent")
	if procErr != nil { w.loadErr = procErr; return }

	w.procReceivePacket, procErr = dll.FindProc("WintunReceivePacket")
	if procErr != nil { w.loadErr = procErr; return }

	w.procReleaseReceivePacket, procErr = dll.FindProc("WintunReleaseReceivePacket")
	if procErr != nil { w.loadErr = procErr; return }

	w.procAllocateSendPacket, procErr = dll.FindProc("WintunAllocateSendPacket")
	if procErr != nil { w.loadErr = procErr; return }

	w.procSendPacket, procErr = dll.FindProc("WintunSendPacket")
	if procErr != nil { w.loadErr = procErr; return }

	w.procSetLogger, procErr = dll.FindProc("WintunSetLogger")
	if procErr != nil { w.loadErr = procErr; return }

	w.isLoaded = true
}

func findWintunDLL() (string, error) {
	// Candidate search paths
	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)

	candidates := []string{
		filepath.Join(exeDir, "wintun.dll"),
		filepath.Join(exeDir, "..", "bin", "wintun.dll"),
		"wintun.dll",
		`C:\Users\SOWLI\Desktop\Dualflow\desktop\bin\wintun.dll`,
		`C:\Users\SOWLI\Desktop\Dualflow\desktop\network-core\wintun.dll`,
		`C:\Windows\System32\wintun.dll`,
	}

	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err == nil {
			if fi, statErr := os.Stat(abs); statErr == nil && !fi.IsDir() {
				return abs, nil
			}
		}
	}

	return "", fmt.Errorf("official wintun.dll not found in standard application paths")
}

// IsAvailable returns true if the wintun.dll library was successfully located and loaded.
func (w *WintunDLL) IsAvailable() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.isLoaded
}

// GetPath returns the resolved filesystem path of the loaded wintun.dll.
func (w *WintunDLL) GetPath() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.dllPath
}

// GetLoadError returns any error that occurred during library discovery or procedure resolution.
func (w *WintunDLL) GetLoadError() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.loadErr
}

// GetRunningDriverVersion queries the Wintun driver running in the Windows kernel.
// Returns "0.14.1" format or "NOT LOADED".
func (w *WintunDLL) GetRunningDriverVersion() (string, error) {
	if !w.IsAvailable() {
		return "UNKNOWN", fmt.Errorf("wintun.dll not loaded: %w", w.loadErr)
	}

	r1, _, err := w.procGetRunningDriverVersion.Call()
	v := uint32(r1)
	if v == 0 {
		return "NOT LOADED", err
	}

	major := (v >> 16) & 0xFFFF
	minor := v & 0xFFFF
	return fmt.Sprintf("%d.%d", major, minor), nil
}

// IsElevated checks whether the current Windows process has Administrator privileges.
func IsElevated() bool {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentProcess := kernel32.NewProc("GetCurrentProcess")
	advapi32 := syscall.NewLazyDLL("advapi32.dll")
	procOpenProcessToken := advapi32.NewProc("OpenProcessToken")
	procGetTokenInformation := advapi32.NewProc("GetTokenInformation")

	hProcess, _, _ := procGetCurrentProcess.Call()
	var token syscall.Handle
	r1, _, _ := procOpenProcessToken.Call(hProcess, syscall.TOKEN_QUERY, uintptr(unsafe.Pointer(&token)))
	if r1 == 0 {
		return false
	}
	defer syscall.CloseHandle(token)

	var elevation uint32
	var returnedLen uint32
	// TokenElevation = 20
	r2, _, _ := procGetTokenInformation.Call(uintptr(token), 20, uintptr(unsafe.Pointer(&elevation)), uintptr(unsafe.Sizeof(elevation)), uintptr(unsafe.Pointer(&returnedLen)))
	if r2 == 0 {
		return false
	}
	return elevation != 0
}
