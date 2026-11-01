package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sync"
)

// EncodeFrame serializes a message using DFTP v1 for backward-compatible unencrypted transports.
func EncodeFrame(msgType uint8, flags uint16, sessionID uint32, seq uint64, payload []byte) ([]byte, error) {
	return EncodeFrameV1(msgType, flags, sessionID, seq, payload)
}

// EncodeFrameV3 serializes a message into a 32-byte header with explicit PathID under DFTP v3.
func EncodeFrameV3(msgType uint8, flags uint16, sessionID uint32, seq uint64, pathID uint16, payload []byte) ([]byte, error) {
	payloadLen := len(payload)
	if uint32(payloadLen) > MaxPayloadLength {
		return nil, ErrPayloadTooLarge
	}

	frameLen := int(HeaderLength) + payloadLen
	buf := make([]byte, frameLen)

	// Magic [0:4]
	copy(buf[0:4], MagicBytes[:])

	// Version [4] & MsgType [5]
	buf[4] = CurrentProtocolVersion
	buf[5] = msgType

	// Flags [6:8]
	binary.BigEndian.PutUint16(buf[6:8], flags)

	// HeaderLength [8:10] & Reserved [10:12]
	binary.BigEndian.PutUint16(buf[8:10], HeaderLength)
	binary.BigEndian.PutUint16(buf[10:12], 0)

	// SessionID [12:16]
	binary.BigEndian.PutUint32(buf[12:16], sessionID)

	// SequenceNumber [16:24]
	binary.BigEndian.PutUint64(buf[16:24], seq)

	// PayloadLength [24:28]
	binary.BigEndian.PutUint32(buf[24:28], uint32(payloadLen))

	// PathID [28:30] & Reserved2 [30:32]
	binary.BigEndian.PutUint16(buf[28:30], pathID)
	binary.BigEndian.PutUint16(buf[30:32], 0)

	if payloadLen > 0 {
		copy(buf[HeaderLength:], payload)
	}

	return buf, nil
}

// EncodeFrameV1 serializes a legacy DFTP v1 frame with CRC32 checksum in bytes [28:32].
func EncodeFrameV1(msgType uint8, flags uint16, sessionID uint32, seq uint64, payload []byte) ([]byte, error) {
	payloadLen := len(payload)
	if uint32(payloadLen) > MaxPayloadLength {
		return nil, ErrPayloadTooLarge
	}

	frameLen := int(HeaderLength) + payloadLen
	buf := make([]byte, frameLen)

	copy(buf[0:4], MagicBytes[:])
	buf[4] = ProtocolVersion1
	buf[5] = msgType
	binary.BigEndian.PutUint16(buf[6:8], flags)
	binary.BigEndian.PutUint16(buf[8:10], HeaderLength)
	binary.BigEndian.PutUint16(buf[10:12], 0)
	binary.BigEndian.PutUint32(buf[12:16], sessionID)
	binary.BigEndian.PutUint64(buf[16:24], seq)
	binary.BigEndian.PutUint32(buf[24:28], uint32(payloadLen))

	if payloadLen > 0 {
		csum := crc32.ChecksumIEEE(payload)
		binary.BigEndian.PutUint32(buf[28:32], csum)
		copy(buf[HeaderLength:], payload)
	} else {
		binary.BigEndian.PutUint32(buf[28:32], 0)
	}

	return buf, nil
}

// DecodeHeader extracts the 32-byte header from the beginning of a buffer.
// It accepts both Version 1 and Version 3 frames.
func DecodeHeader(data []byte) (*FrameHeader, error) {
	if len(data) < int(HeaderLength) {
		return nil, ErrIncompleteHeader
	}

	if data[0] != MagicBytes[0] || data[1] != MagicBytes[1] || data[2] != MagicBytes[2] || data[3] != MagicBytes[3] {
		return nil, ErrInvalidMagic
	}

	version := data[4]
	if version != ProtocolVersion1 && version != ProtocolVersion3 {
		return nil, ErrUnsupportedVersion
	}

	hdrLen := binary.BigEndian.Uint16(data[8:10])
	if hdrLen != HeaderLength {
		return nil, ErrInvalidHeaderLength
	}

	hdr := &FrameHeader{
		Magic:          MagicBytes,
		Version:        version,
		MsgType:        data[5],
		Flags:          binary.BigEndian.Uint16(data[6:8]),
		HeaderLength:   hdrLen,
		Reserved:       binary.BigEndian.Uint16(data[10:12]),
		SessionID:      binary.BigEndian.Uint32(data[12:16]),
		SequenceNumber: binary.BigEndian.Uint64(data[16:24]),
		PayloadLength:  binary.BigEndian.Uint32(data[24:28]),
	}

	if version == ProtocolVersion1 {
		hdr.Checksum = binary.BigEndian.Uint32(data[28:32])
	} else {
		hdr.PathID = binary.BigEndian.Uint16(data[28:30])
		hdr.Reserved2 = binary.BigEndian.Uint16(data[30:32])
	}

	if hdr.PayloadLength > MaxPayloadLength {
		return nil, ErrPayloadTooLarge
	}

	return hdr, nil
}

// DecodeFrame deserializes a single complete frame from a byte buffer.
func DecodeFrame(data []byte) (*Frame, error) {
	hdr, err := DecodeHeader(data)
	if err != nil {
		return nil, err
	}

	totalLen := int(hdr.HeaderLength) + int(hdr.PayloadLength)
	if len(data) < totalLen {
		return nil, ErrIncompletePayload
	}

	payload := data[hdr.HeaderLength:totalLen]

	// Version 1 requires CRC32 validation
	if hdr.Version == ProtocolVersion1 && hdr.PayloadLength > 0 {
		calcCsum := crc32.ChecksumIEEE(payload)
		if calcCsum != hdr.Checksum {
			return nil, ErrChecksumMismatch
		}
	}

	return &Frame{
		Header:  *hdr,
		Payload: payload,
	}, nil
}

// EncodeStreamFrame encodes an inner 16-byte stream header plus application chunk (<= 1280B).
func EncodeStreamFrame(flowID uint32, streamOffset uint64, flags uint16, data []byte) ([]byte, error) {
	dataLen := len(data)
	if dataLen > MaxStreamChunkSize {
		return nil, ErrChunkTooLarge
	}

	buf := make([]byte, InnerStreamHeaderLen+dataLen)
	binary.BigEndian.PutUint32(buf[0:4], flowID)
	binary.BigEndian.PutUint64(buf[4:12], streamOffset)
	binary.BigEndian.PutUint16(buf[12:14], flags)
	binary.BigEndian.PutUint16(buf[14:16], uint16(dataLen))

	if dataLen > 0 {
		copy(buf[InnerStreamHeaderLen:], data)
	}

	return buf, nil
}

// DecodeStreamFrame parses the inner 16-byte stream framing.
func DecodeStreamFrame(raw []byte) (*StreamFrame, error) {
	if len(raw) < InnerStreamHeaderLen {
		return nil, ErrIncompleteStreamHeader
	}

	flowID := binary.BigEndian.Uint32(raw[0:4])
	offset := binary.BigEndian.Uint64(raw[4:12])
	flags := binary.BigEndian.Uint16(raw[12:14])
	pLen := binary.BigEndian.Uint16(raw[14:16])

	if len(raw) < InnerStreamHeaderLen+int(pLen) {
		return nil, ErrIncompletePayload
	}

	payload := raw[InnerStreamHeaderLen : InnerStreamHeaderLen+int(pLen)]

	return &StreamFrame{
		Header: StreamHeader{
			FlowID:        flowID,
			StreamOffset:  offset,
			StreamFlags:   flags,
			PayloadLength: pLen,
		},
		Data: payload,
	}, nil
}

// EncodeStreamAck serializes a selective acknowledgment payload.
func EncodeStreamAck(flowID uint32, cumulativeAck uint64, sackBlocks []SackBlock) ([]byte, error) {
	numBlocks := len(sackBlocks)
	if numBlocks > MaxSackBlocks {
		return nil, ErrTooManySackBlocks
	}

	buf := make([]byte, 16+(numBlocks*16))
	binary.BigEndian.PutUint32(buf[0:4], flowID)
	binary.BigEndian.PutUint64(buf[4:12], cumulativeAck)
	binary.BigEndian.PutUint16(buf[12:14], uint16(numBlocks))
	binary.BigEndian.PutUint16(buf[14:16], 0)

	for i, block := range sackBlocks {
		offset := 16 + (i * 16)
		binary.BigEndian.PutUint64(buf[offset:offset+8], block.StartOffset)
		binary.BigEndian.PutUint64(buf[offset+8:offset+16], block.EndOffset)
	}

	return buf, nil
}

// DecodeStreamAck deserializes a selective acknowledgment payload.
func DecodeStreamAck(raw []byte) (*StreamAckPayload, error) {
	if len(raw) < 16 {
		return nil, ErrMalformedSackPayload
	}

	flowID := binary.BigEndian.Uint32(raw[0:4])
	cumulativeAck := binary.BigEndian.Uint64(raw[4:12])
	numBlocks := binary.BigEndian.Uint16(raw[12:14])
	reserved := binary.BigEndian.Uint16(raw[14:16])

	if numBlocks > MaxSackBlocks {
		return nil, ErrTooManySackBlocks
	}

	expectedLen := 16 + int(numBlocks)*16
	if len(raw) < expectedLen {
		return nil, ErrMalformedSackPayload
	}

	blocks := make([]SackBlock, numBlocks)
	for i := 0; i < int(numBlocks); i++ {
		offset := 16 + (i * 16)
		blocks[i] = SackBlock{
			StartOffset: binary.BigEndian.Uint64(raw[offset : offset+8]),
			EndOffset:   binary.BigEndian.Uint64(raw[offset+8 : offset+16]),
		}
	}

	return &StreamAckPayload{
		FlowID:              flowID,
		CumulativeAckOffset: cumulativeAck,
		NumSackBlocks:       numBlocks,
		Reserved:            reserved,
		SackBlocks:          blocks,
	}, nil
}

// EncodeFlowOpen serializes a SOCKS5 connect request across the tunnel.
func EncodeFlowOpen(flowID uint32, port uint16, addrType uint8, addr string) ([]byte, error) {
	addrBytes := []byte(addr)
	addrLen := len(addrBytes)
	if addrLen > 255 {
		return nil, fmt.Errorf("address length %d exceeds 255", addrLen)
	}

	buf := make([]byte, 8+addrLen)
	binary.BigEndian.PutUint32(buf[0:4], flowID)
	binary.BigEndian.PutUint16(buf[4:6], port)
	buf[6] = addrType
	buf[7] = uint8(addrLen)
	copy(buf[8:], addrBytes)

	return buf, nil
}

// DecodeFlowOpen deserializes a FlowOpen payload.
func DecodeFlowOpen(raw []byte) (*FlowOpenPayload, error) {
	if len(raw) < 8 {
		return nil, ErrMalformedFlowOpen
	}

	flowID := binary.BigEndian.Uint32(raw[0:4])
	port := binary.BigEndian.Uint16(raw[4:6])
	addrType := raw[6]
	addrLen := int(raw[7])

	if len(raw) < 8+addrLen {
		return nil, ErrMalformedFlowOpen
	}

	addr := string(raw[8 : 8+addrLen])

	return &FlowOpenPayload{
		FlowID:   flowID,
		Port:     port,
		AddrType: addrType,
		Addr:     addr,
	}, nil
}

// EncodeFlowOpenAck serializes a flow connection confirmation.
func EncodeFlowOpenAck(flowID uint32, status uint8) []byte {
	buf := make([]byte, 5)
	binary.BigEndian.PutUint32(buf[0:4], flowID)
	buf[4] = status
	return buf
}

// DecodeFlowOpenAck deserializes a flow connection confirmation.
func DecodeFlowOpenAck(raw []byte) (*FlowOpenAckPayload, error) {
	if len(raw) < 5 {
		return nil, errors.New("malformed flow open ack payload")
	}

	return &FlowOpenAckPayload{
		FlowID: binary.BigEndian.Uint32(raw[0:4]),
		Status: raw[4],
	}, nil
}

// EncodePathChallenge serializes a path validation challenge or response.
func EncodePathChallenge(pathID uint16, nonce [8]byte) []byte {
	buf := make([]byte, 10)
	binary.BigEndian.PutUint16(buf[0:2], pathID)
	copy(buf[2:10], nonce[:])
	return buf
}

// DecodePathChallenge deserializes a path validation challenge or response.
func DecodePathChallenge(raw []byte) (*PathChallengePayload, error) {
	if len(raw) < 10 {
		return nil, ErrMalformedPathChallenge
	}

	var nonce [8]byte
	copy(nonce[:], raw[2:10])

	return &PathChallengePayload{
		PathID: binary.BigEndian.Uint16(raw[0:2]),
		Nonce:  nonce,
	}, nil
}

// StreamDecoder handles arbitrary stream chunks, coalesced packets, and partial-frame fragmentation.
type StreamDecoder struct {
	mu  sync.Mutex
	buf []byte
}

// NewStreamDecoder creates a decoder with an empty accumulation buffer.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{
		buf: make([]byte, 0, 65536),
	}
}

// Feed ingests a byte slice from a stream read and returns all completed frames.
func (d *StreamDecoder) Feed(chunk []byte) ([]*Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.buf = append(d.buf, chunk...)
	var frames []*Frame

	for {
		if len(d.buf) < int(HeaderLength) {
			break
		}

		magicIdx := bytes.Index(d.buf, MagicBytes[:])
		if magicIdx < 0 {
			if len(d.buf) > 3 {
				d.buf = d.buf[len(d.buf)-3:]
			}
			break
		}

		if magicIdx > 0 {
			d.buf = d.buf[magicIdx:]
			if len(d.buf) < int(HeaderLength) {
				break
			}
		}

		hdr, err := DecodeHeader(d.buf)
		if err != nil {
			d.buf = d.buf[1:]
			continue
		}

		totalLen := int(hdr.HeaderLength) + int(hdr.PayloadLength)
		if len(d.buf) < totalLen {
			break
		}

		frameData := d.buf[:totalLen]
		frame, err := DecodeFrame(frameData)
		if err != nil {
			d.buf = d.buf[4:]
			continue
		}

		frames = append(frames, frame)
		d.buf = d.buf[totalLen:]
	}

	return frames, nil
}

// Reset clears the decoder buffer.
func (d *StreamDecoder) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf = d.buf[:0]
}
