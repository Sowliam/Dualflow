package discovery

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"dualflow-core/internal/diagnostics"
)

type NetworkAdapter struct {
	ID                       string   `json:"id"`
	Name                     string   `json:"name"`
	Description              string   `json:"description"`
	Manufacturer             string   `json:"manufacturer,omitempty"`
	PnPDeviceID              string   `json:"pnpId,omitempty"`
	IfIndex                  int      `json:"ifIndex"`
	Status                   string   `json:"status"` // "connected" | "disconnected"
	MAC                      string   `json:"mac"`
	MediaType                string   `json:"mediaType"`
	Classification           string   `json:"classification"`
	ClassificationConfidence string   `json:"classificationConfidence"`
	ClassificationReasons    []string `json:"classificationReasons"`
	IPv4                     *string  `json:"ipv4"`
	Netmask                  *string  `json:"netmask"`
	Gateway                  *string  `json:"gateway"`
	DNS                      []string `json:"dns"`
	Metric                   int      `json:"metric"`
	MTU                      int      `json:"mtu"`
}

type DiscoveryEngine struct{}

func NewDiscoveryEngine() *DiscoveryEngine {
	return &DiscoveryEngine{}
}

func (e *DiscoveryEngine) Discover() ([]NetworkAdapter, error) {
	diagnostics.Debug("Beginning Windows network adapter enumeration...")

	// 1. Gather Registry Mapping: Name -> GUID
	nameToGuid := make(map[string]string)
	netRegOut, err := execHiddenCmd("reg", "query", `HKLM\SYSTEM\CurrentControlSet\Control\Network\{4D36E972-E325-11CE-BFC1-08002BE10318}`, "/s", "/v", "Name")
	if err == nil {
		blocks := strings.Split(netRegOut, `HKEY_LOCAL_MACHINE\`)
		guidRegex := regexp.MustCompile(`(?i)\{([0-9A-Fa-f-]{36})\}\\Connection`)
		nameRegex := regexp.MustCompile(`(?i)Name\s+REG_SZ\s+(.*)`)
		for _, b := range blocks {
			gMatch := guidRegex.FindStringSubmatch(b)
			nMatch := nameRegex.FindStringSubmatch(b)
			if len(gMatch) >= 2 && len(nMatch) >= 2 {
				connName := strings.ToLower(strings.TrimSpace(nMatch[1]))
				nameToGuid[connName] = "{" + strings.ToLower(gMatch[1]) + "}"
			}
		}
	}

	// 2. Gather Registry Mapping: GUID -> DriverDesc, MatchingDeviceId (PnP ID), ProviderName (Mfg)
	type HwInfo struct {
		Desc string
		PnP  string
		Mfg  string
	}
	guidToHw := make(map[string]HwInfo)
	classRegOut, err := execHiddenCmd("reg", "query", `HKLM\SYSTEM\CurrentControlSet\Control\Class\{4d36e972-e325-11ce-bfc1-08002be10318}`, "/s")
	if err == nil {
		blocks := strings.Split(classRegOut, `HKEY_LOCAL_MACHINE\`)
		descRegex := regexp.MustCompile(`(?i)DriverDesc\s+REG_SZ\s+(.*)`)
		pnpRegex := regexp.MustCompile(`(?i)MatchingDeviceId\s+REG_SZ\s+(.*)`)
		mfgRegex := regexp.MustCompile(`(?i)ProviderName\s+REG_SZ\s+(.*)`)
		netCfgRegex := regexp.MustCompile(`(?i)NetCfgInstanceId\s+REG_SZ\s+(.*)`)
		for _, b := range blocks {
			netMatch := netCfgRegex.FindStringSubmatch(b)
			if len(netMatch) >= 2 {
				guid := strings.ToLower(strings.TrimSpace(netMatch[1]))
				hw := HwInfo{}
				if m := descRegex.FindStringSubmatch(b); len(m) >= 2 {
					hw.Desc = strings.TrimSpace(m[1])
				}
				if m := pnpRegex.FindStringSubmatch(b); len(m) >= 2 {
					hw.PnP = strings.TrimSpace(m[1])
				}
				if m := mfgRegex.FindStringSubmatch(b); len(m) >= 2 {
					hw.Mfg = strings.TrimSpace(m[1])
				}
				guidToHw[guid] = hw
			}
		}
	}

	// 3. Subinterfaces: MTU, MediaSenseState, Bytes
	type SubInfo struct {
		MTU             int
		MediaSenseState int
		BytesIn         uint64
		BytesOut        uint64
	}
	subMap := make(map[string]SubInfo)
	subOut, err := execHiddenCmd("netsh", "interface", "ipv4", "show", "subinterfaces")
	if err == nil {
		lines := strings.Split(subOut, "\n")
		isTable := false
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if strings.Contains(l, "---") {
				isTable = true
				continue
			}
			if !isTable || l == "" {
				continue
			}
			fields := strings.Fields(l)
			if len(fields) >= 5 {
				mtu, _ := strconv.Atoi(fields[0])
				state, _ := strconv.Atoi(fields[1])
				inB, _ := strconv.ParseUint(fields[2], 10, 64)
				outB, _ := strconv.ParseUint(fields[3], 10, 64)
				ifName := strings.ToLower(strings.Join(fields[4:], " "))
				subMap[ifName] = SubInfo{
					MTU:             mtu,
					MediaSenseState: state,
					BytesIn:         inB,
					BytesOut:        outB,
				}
			}
		}
	}

	// 4. getmac CSV
	type MacInfo struct {
		Name           string
		Description    string
		MAC            string
		IsDisconnected bool
	}
	macMap := make(map[string]MacInfo)
	getmacOut, err := execHiddenCmd("getmac", "/fo", "csv", "/v")
	if err == nil {
		r := csv.NewReader(strings.NewReader(getmacOut))
		records, _ := r.ReadAll()
		for i, rec := range records {
			if i == 0 || len(rec) < 4 {
				continue
			}
			conn := rec[0]
			desc := rec[1]
			mac := rec[2]
			trans := rec[3]

			cleanMac := "—"
			if mac != "" && mac != "N/A" && !strings.Contains(mac, "?") {
				cleanMac = strings.ToUpper(mac)
			}
			isDisc := strings.Contains(strings.ToLower(trans), "disconnected") || strings.Contains(strings.ToLower(trans), "not present")
			macMap[strings.ToLower(conn)] = MacInfo{
				Name:           conn,
				Description:    desc,
				MAC:            cleanMac,
				IsDisconnected: isDisc,
			}
		}
	}

	// 5. netsh addresses
	type NetshInfo struct {
		IPv4    *string
		Netmask *string
		Gateway *string
		Metric  int
	}
	netshMap := make(map[string]NetshInfo)
	addrOut, err := execHiddenCmd("netsh", "interface", "ipv4", "show", "addresses")
	if err == nil {
		blocks := strings.Split(addrOut, "Configuration for interface ")
		for _, b := range blocks {
			lines := strings.Split(b, "\n")
			if len(lines) == 0 {
				continue
			}
			header := strings.TrimSpace(lines[0])
			header = strings.Trim(header, `"`)
			if header == "" {
				continue
			}
			ifName := strings.ToLower(header)

			var ipv4, netmask, gateway *string
			metric := 0

			for _, l := range lines {
				l = strings.TrimSpace(l)
				if strings.HasPrefix(l, "IP Address:") {
					v := strings.TrimSpace(strings.TrimPrefix(l, "IP Address:"))
					ipv4 = &v
				} else if strings.Contains(l, "Subnet Prefix:") {
					m := regexp.MustCompile(`(?i)mask\s+([0-9.]+)`).FindStringSubmatch(l)
					if len(m) >= 2 {
						v := strings.TrimSpace(m[1])
						netmask = &v
					}
				} else if strings.HasPrefix(l, "Default Gateway:") {
					v := strings.TrimSpace(strings.TrimPrefix(l, "Default Gateway:"))
					gateway = &v
				} else if strings.HasPrefix(l, "InterfaceMetric:") {
					metric, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l, "InterfaceMetric:")))
				}
			}

			netshMap[ifName] = NetshInfo{
				IPv4:    ipv4,
				Netmask: netmask,
				Gateway: gateway,
				Metric:  metric,
			}
		}
	}

	// 6. netsh DNS
	dnsMap := make(map[string][]string)
	dnsOut, err := execHiddenCmd("netsh", "interface", "ipv4", "show", "dnsservers")
	if err == nil {
		blocks := strings.Split(dnsOut, "Configuration for interface ")
		ipRegex := regexp.MustCompile(`^([0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3})$`)
		for _, b := range blocks {
			lines := strings.Split(b, "\n")
			if len(lines) == 0 {
				continue
			}
			header := strings.Trim(strings.TrimSpace(lines[0]), `"`)
			if header == "" {
				continue
			}
			ifName := strings.ToLower(header)
			var servers []string
			for _, l := range lines {
				l = strings.TrimSpace(l)
				if strings.Contains(l, "configured through DHCP:") || strings.Contains(l, "Configured DNS Servers:") {
					parts := strings.Split(l, ":")
					if len(parts) >= 2 {
						ip := strings.TrimSpace(parts[1])
						if ipRegex.MatchString(ip) {
							servers = append(servers, ip)
						}
					}
				} else if ipRegex.MatchString(l) {
					servers = append(servers, l)
				}
			}
			dnsMap[ifName] = servers
		}
	}

	// 7. Cross-check with Go standard library net.Interfaces
	goIfaces, _ := net.Interfaces()
	goIpv4Map := make(map[string]string)
	goMaskMap := make(map[string]string)
	goMacMap := make(map[string]string)
	goIdxMap := make(map[string]int)

	for _, gi := range goIfaces {
		name := strings.ToLower(gi.Name)
		goIdxMap[name] = gi.Index
		if gi.HardwareAddr != nil && len(gi.HardwareAddr) > 0 {
			goMacMap[name] = strings.ToUpper(strings.ReplaceAll(gi.HardwareAddr.String(), ":", "-"))
		}
		addrs, _ := gi.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					goIpv4Map[name] = ip4.String()
					mask := ipnet.Mask
					goMaskMap[name] = fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
				}
			}
		}
	}

	// 8. Combine All Interfaces
	keys := make(map[string]bool)
	for k := range macMap {
		keys[k] = true
	}
	for k := range netshMap {
		keys[k] = true
	}
	for _, gi := range goIfaces {
		keys[strings.ToLower(gi.Name)] = true
	}

	var results []NetworkAdapter

	for k := range keys {
		if strings.Contains(k, "loopback") || strings.Contains(k, "pseudo") || strings.Contains(k, "teredo") || strings.Contains(k, "6to4") || strings.Contains(k, "isatap") {
			continue
		}

		macInfo := macMap[k]
		if macInfo.Name == "" {
			macInfo.Name = k
			macInfo.Description = k
			macInfo.MAC = "—"
		}
		netInfo := netshMap[k]
		subInfo, hasSub := subMap[k]
		if !hasSub {
			subInfo = SubInfo{MTU: 1500, MediaSenseState: 1}
		}
		guid := nameToGuid[k]
		hwInfo := guidToHw[guid]
		dnsList := dnsMap[k]

		finalIPv4 := netInfo.IPv4
		if finalIPv4 == nil {
			if ip, ok := goIpv4Map[k]; ok {
				finalIPv4 = &ip
			}
		}
		finalNetmask := netInfo.Netmask
		if finalNetmask == nil {
			if m, ok := goMaskMap[k]; ok {
				finalNetmask = &m
			}
		}

		finalMAC := macInfo.MAC
		if finalMAC == "—" {
			if m, ok := goMacMap[k]; ok && m != "00-00-00-00-00-00" {
				finalMAC = m
			}
		}

		isConnected := !macInfo.IsDisconnected && subInfo.MediaSenseState == 1 && finalIPv4 != nil

		desc := hwInfo.Desc
		if desc == "" {
			desc = macInfo.Description
		}
		pnpId := hwInfo.PnP
		mfg := hwInfo.Mfg

		// Strict Multi-factor Classification
		classification := "unassigned"
		mediaType := "other"
		confidence := "low"
		var reasons []string

		descLower := strings.ToLower(desc)
		pnpUpper := strings.ToUpper(pnpId)

		isUsbBus := strings.HasPrefix(pnpUpper, `USB\`)
		isPciBus := strings.HasPrefix(pnpUpper, `PCI\`)
		isRootBus := strings.HasPrefix(pnpUpper, `ROOT\`)

		isAndroidDesc := strings.Contains(descLower, "remote ndis") ||
			strings.Contains(descLower, "rndis") ||
			strings.Contains(descLower, "ncm") ||
			strings.Contains(descLower, "android") ||
			strings.Contains(descLower, "samsung mobile")

		isAndroidPnp := strings.Contains(pnpUpper, "CLASS_EF&SUBCLASS_04&PROT_01") ||
			strings.Contains(pnpUpper, "MS_COMP_RNDIS") ||
			strings.Contains(pnpUpper, "VID_18D1") ||
			strings.Contains(pnpUpper, "VID_04E8")

		isAndroidSubnet := false
		if finalIPv4 != nil {
			ip := *finalIPv4
			isAndroidSubnet = strings.HasPrefix(ip, "192.168.42.") || strings.HasPrefix(ip, "192.168.43.") || strings.HasPrefix(ip, "192.168.44.")
		}
		if netInfo.Gateway != nil {
			gw := *netInfo.Gateway
			if strings.HasPrefix(gw, "192.168.42.") || strings.HasPrefix(gw, "192.168.43.") || strings.HasPrefix(gw, "192.168.44.") {
				isAndroidSubnet = true
			}
		}

		if isAndroidDesc || isAndroidPnp {
			classification = "android"
			if strings.Contains(descLower, "ncm") {
				mediaType = "ncm_android"
			} else {
				mediaType = "rndis_android"
			}
			if isUsbBus || isAndroidPnp {
				confidence = "high"
			} else {
				confidence = "medium"
			}
			if pnpId != "" {
				reasons = append(reasons, fmt.Sprintf("PnP Hardware ID: %s", pnpId))
			}
			if mfg != "" {
				reasons = append(reasons, fmt.Sprintf("Device Manufacturer: %s", mfg))
			}
			reasons = append(reasons, fmt.Sprintf("Driver: %s", desc))
		} else if isAndroidSubnet && !isUsbBus && !isPciBus {
			// IP matched tether range but missing USB/RNDIS hardware identifier -> UNKNOWN
			classification = "unknown"
			confidence = "low"
			reasons = append(reasons, fmt.Sprintf("IP subnet %s matches tether range, but lacks verified USB/RNDIS hardware identifier.", *finalIPv4))
		} else if strings.Contains(descLower, "tap-windows") || strings.Contains(descLower, "openvpn") || strings.Contains(descLower, "wireguard") || strings.Contains(descLower, "wiresock") || strings.Contains(descLower, "hotspotshield") || isRootBus {
			classification = "vpn"
			mediaType = "vpn"
			confidence = "high"
			if pnpId != "" {
				reasons = append(reasons, fmt.Sprintf("PnP ID: %s", pnpId))
			}
			reasons = append(reasons, fmt.Sprintf("VPN virtual adapter: %s", desc))
		} else if strings.Contains(descLower, "hyper-v") || strings.Contains(descLower, "vethernet") || strings.Contains(descLower, "virtualbox") || strings.Contains(descLower, "vmware") || strings.Contains(strings.ToLower(macInfo.Name), "vethernet") {
			classification = "virtual"
			mediaType = "virtual"
			confidence = "high"
			reasons = append(reasons, fmt.Sprintf("Virtual hypervisor switch: %s", desc))
		} else if strings.Contains(descLower, "wi-fi") || strings.Contains(descLower, "wireless") || strings.Contains(descLower, "802.11") {
			mediaType = "wifi"
			if netInfo.Gateway != nil && isConnected {
				classification = "modem"
				confidence = "high"
				reasons = append(reasons, fmt.Sprintf("Active Wi-Fi gateway: %s", *netInfo.Gateway))
			} else {
				reasons = append(reasons, "Wi-Fi radio interface")
			}
		} else if strings.Contains(descLower, "pcie") || strings.Contains(descLower, "gigabit") || strings.Contains(descLower, "gbe") || strings.Contains(descLower, "ethernet") || strings.Contains(descLower, "family controller") || strings.Contains(descLower, "realtek") || strings.Contains(descLower, "intel") || isPciBus {
			mediaType = "ethernet"
			if netInfo.Gateway != nil && isConnected {
				classification = "modem"
				confidence = "high"
				if pnpId != "" {
					reasons = append(reasons, fmt.Sprintf("PnP Hardware ID: %s", pnpId))
				}
				if mfg != "" {
					reasons = append(reasons, fmt.Sprintf("Manufacturer: %s", mfg))
				}
				reasons = append(reasons, fmt.Sprintf("Physical PCI/PCIe Ethernet NIC with active gateway %s (Home Modem/Router)", *netInfo.Gateway))
			} else {
				if pnpId != "" {
					reasons = append(reasons, fmt.Sprintf("PnP Hardware ID: %s", pnpId))
				}
				statusStr := "Disconnected"
				if isConnected {
					statusStr = "Up"
				}
				reasons = append(reasons, fmt.Sprintf("Physical Ethernet NIC (Link status: %s)", statusStr))
			}
		}

		cleanId := "adapter-" + strings.ToLower(regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(macInfo.Name, "-"))

		status := "disconnected"
		if isConnected {
			status = "connected"
		}

		results = append(results, NetworkAdapter{
			ID:                       cleanId,
			Name:                     macInfo.Name,
			Description:              desc,
			Manufacturer:             mfg,
			PnPDeviceID:              pnpId,
			IfIndex:                  goIdxMap[k],
			Status:                   status,
			MAC:                      finalMAC,
			MediaType:                mediaType,
			Classification:           classification,
			ClassificationConfidence: confidence,
			ClassificationReasons:    reasons,
			IPv4:                     finalIPv4,
			Netmask:                  finalNetmask,
			Gateway:                  netInfo.Gateway,
			DNS:                      dnsList,
			Metric:                   netInfo.Metric,
			MTU:                      subInfo.MTU,
		})
	}

	diagnostics.Info("Enumerated %d Windows network adapters via IP Helper & Registry", len(results))
	return results, nil
}

func execHiddenCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	err := cmd.Run()
	return out.String(), err
}

