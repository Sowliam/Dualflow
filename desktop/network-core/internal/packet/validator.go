package packet

import (
	"encoding/binary"
	"net"
	"time"
)

// InternetChecksum computes the RFC 1071 one's complement sum over a byte slice.
func InternetChecksum(data []byte) uint16 {
	var sum uint32
	n := len(data)
	for i := 0; i < n-1; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i : i+2]))
	}
	if n%2 == 1 {
		sum += uint32(data[n-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

// ValidateIPPacket strictly checks IPv4 and IPv6 packets against standard RFC specifications.
// It guards against buffer overruns, truncated headers, invalid options, and length mismatches.
func ValidateIPPacket(buf []byte, verifyChecksum bool) (*Packet, error) {
	bufLen := len(buf)
	if bufLen < MinIPv4HeaderLen {
		return nil, ErrPacketTooShort
	}
	if bufLen > MaxPacketSize {
		return nil, ErrPacketTooLarge
	}

	version := (buf[0] >> 4) & 0x0F

	switch version {
	case IPv4Version:
		return validateIPv4(buf, bufLen, verifyChecksum)
	case IPv6Version:
		return validateIPv6(buf, bufLen)
	default:
		return nil, ErrUnsupportedIPVersion
	}
}

func validateIPv4(buf []byte, bufLen int, verifyChecksum bool) (*Packet, error) {
	ihl := int(buf[0]&0x0F) * 4
	if ihl < MinIPv4HeaderLen || ihl > bufLen {
		return nil, ErrInvalidIHL
	}

	totalLength := int(binary.BigEndian.Uint16(buf[2:4]))
	if totalLength < ihl || totalLength > bufLen {
		return nil, ErrIPv4LengthMismatch
	}

	if verifyChecksum {
		// RFC 791 / RFC 1071 checksum over the IPv4 header
		csum := InternetChecksum(buf[:ihl])
		if csum != 0 {
			return nil, ErrInvalidIPv4Checksum
		}
	}

	protocol := buf[9]
	srcIP := make(net.IP, 4)
	dstIP := make(net.IP, 4)
	copy(srcIP, buf[12:16])
	copy(dstIP, buf[16:20])

	// Trim trailing ethernet or padding bytes if buffer exceeds totalLength
	validRaw := buf[:totalLength]

	return &Packet{
		Raw:          validRaw,
		Version:      IPv4Version,
		Protocol:     protocol,
		SrcIP:        srcIP,
		DstIP:        dstIP,
		TotalLength:  totalLength,
		HeaderLength: ihl,
		CapturedAt:   time.Now(),
	}, nil
}

func validateIPv6(buf []byte, bufLen int) (*Packet, error) {
	if bufLen < MinIPv6HeaderLen {
		return nil, ErrPacketTooShort
	}

	payloadLength := int(binary.BigEndian.Uint16(buf[4:6]))
	totalLength := MinIPv6HeaderLen + payloadLength
	if totalLength > bufLen {
		return nil, ErrIPv6PayloadMismatch
	}

	nextHeader := buf[6]
	offset := MinIPv6HeaderLen
	const maxExtensionHeaders = 8
	extensionHdrCount := 0

	// Walk IPv6 extension header chain safely with strict bounds and recursion/loop limits
	for {
		switch nextHeader {
		case IPv6HopByHop, IPv6Routing, IPv6DestOpts:
			if extensionHdrCount >= maxExtensionHeaders {
				return nil, ErrExtensionHeaderLimit
			}
			if offset+2 > totalLength {
				return nil, ErrCorruptExtensionHdr
			}
			extLen := (int(buf[offset+1]) + 1) * 8
			if offset+extLen > totalLength {
				return nil, ErrCorruptExtensionHdr
			}
			nextHeader = buf[offset]
			offset += extLen
			extensionHdrCount++

		case IPv6Fragment:
			if extensionHdrCount >= maxExtensionHeaders {
				return nil, ErrExtensionHeaderLimit
			}
			const fragHdrLen = 8
			if offset+fragHdrLen > totalLength {
				return nil, ErrCorruptExtensionHdr
			}
			nextHeader = buf[offset]
			offset += fragHdrLen
			extensionHdrCount++

		case IPv6AH:
			if extensionHdrCount >= maxExtensionHeaders {
				return nil, ErrExtensionHeaderLimit
			}
			if offset+2 > totalLength {
				return nil, ErrCorruptExtensionHdr
			}
			ahLen := (int(buf[offset+1]) + 2) * 4
			if offset+ahLen > totalLength {
				return nil, ErrCorruptExtensionHdr
			}
			nextHeader = buf[offset]
			offset += ahLen
			extensionHdrCount++

		case IPv6ESP, IPv6NoNextHdr:
			// Terminal headers
			goto DoneExtensions

		default:
			// Upper layer protocol reached (e.g. TCP, UDP, ICMPv6)
			goto DoneExtensions
		}
	}

DoneExtensions:
	srcIP := make(net.IP, 16)
	dstIP := make(net.IP, 16)
	copy(srcIP, buf[8:24])
	copy(dstIP, buf[24:40])

	validRaw := buf[:totalLength]

	return &Packet{
		Raw:          validRaw,
		Version:      IPv6Version,
		Protocol:     nextHeader,
		SrcIP:        srcIP,
		DstIP:        dstIP,
		TotalLength:  totalLength,
		HeaderLength: offset,
		CapturedAt:   time.Now(),
	}, nil
}

