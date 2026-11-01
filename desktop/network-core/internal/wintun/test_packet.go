package wintun

import (
	"encoding/binary"
	"net"
)

// GenerateTestIPv4Packet synthesizes a valid RFC 791 IPv4 ICMP Echo Request test packet.
// This is strictly used for testing packet validation and ring buffer operations
// without modifying host routes or generating public network traffic.
func GenerateTestIPv4Packet(srcIP, dstIP net.IP, payloadSize int) []byte {
	src := srcIP.To4()
	if src == nil {
		src = net.IPv4(10, 254, 1, 1).To4()
	}
	dst := dstIP.To4()
	if dst == nil {
		dst = net.IPv4(10, 254, 1, 2).To4()
	}

	totalLen := 20 + 8 + payloadSize
	pkt := make([]byte, totalLen)

	// IPv4 Header
	pkt[0] = 0x45 // Version 4, Header Length 5 (20 bytes)
	pkt[1] = 0x00 // DSCP / ECN
	binary.BigEndian.PutUint16(pkt[2:4], uint16(totalLen))
	binary.BigEndian.PutUint16(pkt[4:6], 0x1234) // Identification
	binary.BigEndian.PutUint16(pkt[6:8], 0x4000) // Flags (Don't Fragment)
	pkt[8] = 64                                 // TTL
	pkt[9] = 1                                  // Protocol: ICMP
	copy(pkt[12:16], src)
	copy(pkt[16:20], dst)

	// Compute IP header checksum
	csum := computeChecksum(pkt[:20])
	binary.BigEndian.PutUint16(pkt[10:12], csum)

	// ICMP Header (Echo Request)
	icmpOffset := 20
	pkt[icmpOffset] = 8     // Type: Echo Request
	pkt[icmpOffset+1] = 0   // Code: 0
	binary.BigEndian.PutUint16(pkt[icmpOffset+4:icmpOffset+6], 0x0001) // Identifier
	binary.BigEndian.PutUint16(pkt[icmpOffset+6:icmpOffset+8], 0x0001) // Sequence number

	// Payload fill
	for i := 0; i < payloadSize; i++ {
		pkt[icmpOffset+8+i] = byte('A' + (i % 26))
	}

	// Compute ICMP checksum
	icmpCsum := computeChecksum(pkt[icmpOffset:])
	binary.BigEndian.PutUint16(pkt[icmpOffset+2:icmpOffset+4], icmpCsum)

	return pkt
}

func computeChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i < len(b)-1; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for (sum >> 16) > 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

